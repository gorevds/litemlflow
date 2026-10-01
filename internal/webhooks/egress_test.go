package webhooks_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/webhooks"
)

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", // AWS/GCP metadata
		"100.100.100.200", // Alibaba metadata (CGNAT range)
		"0.0.0.1",         // 0.0.0.0/8 reaches localhost on Linux
		"0.0.0.0", "255.255.255.255", "224.0.0.1", "240.0.0.1",
		"::1", "::", "fe80::1", "fd00::1",
		"::ffff:127.0.0.1",       // IPv4-mapped loopback
		"64:ff9b::a9fe:a9fe",     // NAT64 of 169.254.169.254
		"2002:7f00:1::1",         // 6to4 of 127.0.0.1
		"64:ff9b::c0a8:101",      // NAT64 of 192.168.1.1
		"::ffff:169.254.169.254", // IPv4-mapped metadata
	}
	for _, s := range blocked {
		if !webhooks.IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "64:ff9b::808:808"}
	for _, s := range allowed {
		if webhooks.IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
}

// TestGuardedClientBlocksLoopbackAtDial is the DNS-rebinding / stale-validation
// regression: URL validation only happens when a webhook is saved, so the
// connection itself must refuse private targets.
func TestGuardedClientBlocksLoopbackAtDial(t *testing.T) {
	t.Setenv(webhooks.AllowPrivateEnv, "")
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	_, err := webhooks.NewGuardedClient(2*time.Second).Post(srv.URL, "application/json", nil)
	if !errors.Is(err, webhooks.ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback receiver was reached %d times", hits.Load())
	}

	// SyncDelivery (the /test endpoint) uses the guarded client by default.
	status, err := (&webhooks.SyncDelivery{}).Deliver(&model.Webhook{URL: srv.URL}, "run_finished", &model.Run{ID: "r"})
	if !errors.Is(err, webhooks.ErrBlockedAddress) || status != 0 {
		t.Fatalf("SyncDelivery default client: status=%d err=%v", status, err)
	}
	if hits.Load() != 0 {
		t.Fatal("SyncDelivery reached loopback receiver")
	}
}

// TestGuardedClientDoesNotFollowRedirects: a public endpoint must not be able
// to bounce the (signed) delivery to another host.
func TestGuardedClientDoesNotFollowRedirects(t *testing.T) {
	t.Setenv(webhooks.AllowPrivateEnv, "1")
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	resp, err := webhooks.NewGuardedClient(2*time.Second).Post(redirector.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want 307 returned as-is", resp.StatusCode)
	}
	if targetHits.Load() != 0 {
		t.Error("redirect was followed")
	}
}

type wsRecordingStore struct {
	mu  sync.Mutex
	got []string
}

func (s *wsRecordingStore) ListWebhooks(_ context.Context, ws string, _ *int64) ([]*model.Webhook, error) {
	s.mu.Lock()
	s.got = append(s.got, ws)
	s.mu.Unlock()
	return nil, nil
}

func (s *wsRecordingStore) RecordWebhookAttempt(context.Context, int64, int, int64) error {
	return nil
}

// TestNotifyUsesRequestWorkspace: Notify used to always query with "" (which
// the store maps to "default"), so webhooks in any other workspace never fired.
func TestNotifyUsesRequestWorkspace(t *testing.T) {
	t.Parallel()
	ms := &wsRecordingStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := webhooks.NewWithOptions(ctx, ms, nil, webhooks.Options{})
	defer d.Stop(time.Second)

	d.Notify(webhooks.WithWorkspace(ctx, "team-b"), "run_finished", &model.Run{ID: "r", ExperimentID: 7})
	d.Notify(ctx, "run_finished", &model.Run{ID: "r2", ExperimentID: 7})

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.got) != 2 || ms.got[0] != "team-b" || ms.got[1] != "" {
		t.Fatalf("ListWebhooks workspaces = %q, want [team-b \"\"]", ms.got)
	}
}
