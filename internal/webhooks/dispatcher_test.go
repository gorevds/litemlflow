package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/webhooks"
)

// mockStore implements WebhookLookup for tests.
type mockStore struct {
	webhooks []*model.Webhook
	mu       sync.Mutex
	attempts []int
}

func (m *mockStore) ListWebhooks(_ context.Context, _ string, _ *int64) ([]*model.Webhook, error) {
	return m.webhooks, nil
}

func (m *mockStore) RecordWebhookAttempt(_ context.Context, _ int64, status int, _ int64) error {
	m.mu.Lock()
	m.attempts = append(m.attempts, status)
	m.mu.Unlock()
	return nil
}

func TestDispatcherDeliversOnRunFinished(t *testing.T) {
	var receivedCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &model.Webhook{
		ID:      1,
		Name:    "test",
		URL:     srv.URL,
		Events:  "run_finished",
		Enabled: true,
	}
	ms := &mockStore{webhooks: []*model.Webhook{wh}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{RetryBase: 10 * time.Millisecond})

	run := &model.Run{ID: "abc123", Status: model.StatusFinished}
	d.Notify(ctx, "run_finished", run)

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if receivedCount.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("dispatcher did not deliver within 2s")
}

func TestDispatcherSignatureVerification(t *testing.T) {
	secret := "my-secret-key"
	type delivery struct {
		sig  string
		body []byte
	}
	received := make(chan delivery, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.Header.Get("X-LiteMLflow-Signature")
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		received <- delivery{sig: sig, body: buf.Bytes()}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &model.Webhook{
		ID:      2,
		Name:    "signed",
		URL:     srv.URL,
		Events:  "run_finished",
		Secret:  secret,
		Enabled: true,
	}
	ms := &mockStore{webhooks: []*model.Webhook{wh}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{RetryBase: 10 * time.Millisecond})

	run := &model.Run{ID: "signed-run", Status: model.StatusFinished}
	d.Notify(ctx, "run_finished", run)

	select {
	case got := <-received:
		if !webhooks.VerifySignature(secret, got.body, got.sig) {
			t.Errorf("signature verification failed: sig=%q", got.sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery received within 2s")
	}
}

func TestDispatcherRetries(t *testing.T) {
	var callCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			// Fail the first 2 attempts.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &model.Webhook{
		ID:      3,
		Name:    "retry-test",
		URL:     srv.URL,
		Events:  "run_finished",
		Enabled: true,
	}
	ms := &mockStore{webhooks: []*model.Webhook{wh}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use 10ms base so retries happen quickly: 10ms → 50ms.
	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{RetryBase: 10 * time.Millisecond})

	run := &model.Run{ID: "retry-run", Status: model.StatusFinished}
	d.Notify(ctx, "run_finished", run)

	// Wait for all 3 attempts (backoff 10ms,50ms gives ~60ms, allow 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("dispatcher made %d calls, expected 3", callCount.Load())
}

func TestDispatcherSkipsDisabledWebhook(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &model.Webhook{
		ID:      4,
		Name:    "disabled",
		URL:     srv.URL,
		Events:  "run_finished",
		Enabled: false, // disabled!
	}
	ms := &mockStore{webhooks: []*model.Webhook{wh}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{RetryBase: 10 * time.Millisecond})

	run := &model.Run{ID: "noop-run", Status: model.StatusFinished}
	d.Notify(ctx, "run_finished", run)

	time.Sleep(100 * time.Millisecond)
	if called.Load() {
		t.Error("disabled webhook should not be called")
	}
}

func TestDispatcherBackpressureDrop(t *testing.T) {
	// We verify drop behavior by creating more webhooks than queueCapacity (1024)
	// while also keeping workers busy so the queue fills up.
	// Strategy: use a custom store that returns 1082 webhooks (queue + workers + extra).
	// A custom ListWebhooks blocks the enqueue loop long enough to saturate.
	// Simpler approach: just verify Dispatcher.DroppedCount works when the queue
	// cannot accept more items (we saturate by using 2000 webhooks with a
	// non-existent URL so workers fail fast and re-drain but we confirm at
	// least 1 drop occurred during the initial Notify burst).

	total := 2000 // > 1024 queue capacity
	whs := make([]*model.Webhook, total)
	for i := range whs {
		whs[i] = &model.Webhook{
			ID:      int64(i + 1),
			Name:    "flood",
			URL:     "http://127.0.0.1:1", // port 1 is unreachable; fail fast
			Events:  "run_finished",
			Enabled: true,
		}
	}
	ms := &mockStore{webhooks: whs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shortClient := &http.Client{Timeout: 1 * time.Millisecond}
	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{
		RetryBase:  1 * time.Millisecond,
		HTTPClient: shortClient,
	})

	run := &model.Run{ID: "flood-run", Status: model.StatusFinished}
	d.Notify(ctx, "run_finished", run)

	// Wait briefly for the queue to partially drain.
	time.Sleep(50 * time.Millisecond)

	// At least some should have been dropped since 2000 > 1024 queue capacity.
	if d.DroppedCount() == 0 {
		t.Error("expected some drops under backpressure, got 0")
	}
}

func TestSyncDeliveryPayload(t *testing.T) {
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		received <- buf.Bytes()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &model.Webhook{
		ID:      99,
		Name:    "sync-test",
		URL:     srv.URL,
		Events:  "run_finished",
		Enabled: true,
	}
	run := &model.Run{
		ID:     "sync-run-id",
		Status: model.StatusFinished,
		Kind:   model.KindClassic,
	}

	sd := &webhooks.SyncDelivery{}
	status, err := sd.Deliver(wh, "run_finished", run)
	if err != nil {
		t.Fatalf("deliver error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status: got %d want 200", status)
	}

	select {
	case payload := <-received:
		var p map[string]any
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p["event"] != "run_finished" {
			t.Errorf("event: got %v", p["event"])
		}
		runObj, ok := p["run"].(map[string]any)
		if !ok {
			t.Fatal("run field missing in payload")
		}
		if runObj["id"] != "sync-run-id" {
			t.Errorf("run.id: got %v", runObj["id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no payload received")
	}
}
