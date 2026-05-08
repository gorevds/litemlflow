package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorevds/litemlflow/internal/api/native"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func openWebhookStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wh_test.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func buildNativeTestHandler(st *store.SQLiteStore) http.Handler {
	r := chi.NewRouter()
	h := &native.Handler{Store: st}
	h.Mount(r)
	return r
}

// TestWebhookCRUD exercises the create/list/update/delete lifecycle.
func TestWebhookCRUD(t *testing.T) {
	st := openWebhookStore(t)
	srv := httptest.NewServer(buildNativeTestHandler(st))
	defer srv.Close()

	client := srv.Client()

	// Create.
	body := `{"name":"slack","url":"http://localhost:9999","events":"run_finished,run_failed"}`
	resp, err := client.Post(srv.URL+"/api/v1/webhooks", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: got %d want 200", resp.StatusCode)
	}
	var created model.Webhook
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero id")
	}
	if created.Name != "slack" {
		t.Errorf("name: got %q want %q", created.Name, "slack")
	}

	// List.
	resp2, err := client.Get(srv.URL + "/api/v1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var listResp struct {
		Webhooks []model.Webhook `json:"webhooks"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Webhooks) != 1 {
		t.Fatalf("list: got %d webhooks want 1", len(listResp.Webhooks))
	}

	// Update (enable toggle via PATCH).
	patch := `{"enabled":false}`
	req, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/api/v1/webhooks/1",
		bytes.NewBufferString(patch))
	req.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("update: got %d want 200", resp3.StatusCode)
	}
	var updated model.Webhook
	if err := json.NewDecoder(resp3.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Error("expected webhook to be disabled after PATCH")
	}

	// Delete.
	req4, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/webhooks/1", nil)
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("delete: got %d want 200", resp4.StatusCode)
	}

	// List should be empty.
	resp5, err := client.Get(srv.URL + "/api/v1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	var listResp2 struct {
		Webhooks []model.Webhook `json:"webhooks"`
	}
	if err := json.NewDecoder(resp5.Body).Decode(&listResp2); err != nil {
		t.Fatal(err)
	}
	if len(listResp2.Webhooks) != 0 {
		t.Errorf("after delete: got %d webhooks want 0", len(listResp2.Webhooks))
	}
}

// TestWebhookTestEndpoint exercises POST /api/v1/webhooks/{id}/test against a
// real httptest server acting as the webhook receiver.
func TestWebhookTestEndpoint(t *testing.T) {
	// Start a mock receiver.
	received := make(chan []byte, 1)
	mockReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		received <- buf.Bytes()
		w.WriteHeader(http.StatusOK)
	}))
	defer mockReceiver.Close()

	st := openWebhookStore(t)
	srv := httptest.NewServer(buildNativeTestHandler(st))
	defer srv.Close()
	client := srv.Client()

	// Create webhook pointing at mock receiver.
	body := `{"name":"test","url":"` + mockReceiver.URL + `","events":"run_finished"}`
	resp, err := client.Post(srv.URL+"/api/v1/webhooks", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create webhook: got %d", resp.StatusCode)
	}

	// Call /test.
	resp2, err := client.Post(srv.URL+"/api/v1/webhooks/1/test", "application/json", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("test endpoint: got %d", resp2.StatusCode)
	}

	// Verify the mock receiver got the delivery.
	select {
	case payload := <-received:
		var p map[string]any
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("invalid payload: %v", err)
		}
		if p["event"] != "run_finished" {
			t.Errorf("event: got %v want run_finished", p["event"])
		}
	default:
		t.Error("mock receiver did not receive delivery")
	}
}
