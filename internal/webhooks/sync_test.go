package webhooks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

func TestSyncDeliveryEchoPath(t *testing.T) {
	t.Parallel()
	echo := NewEchoLog(10)
	d := &SyncDelivery{Echo: echo}
	wh := &model.Webhook{ID: 7, Name: "demo", URL: "lmf://echo", Secret: "s"}
	r := &model.Run{ID: "rid", ExperimentID: 1, Status: "FINISHED"}

	status, err := d.Deliver(wh, "run_finished", r)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200, got %d", status)
	}
	entries := echo.List(5)
	if len(entries) != 1 {
		t.Fatalf("expected 1 echo entry, got %d", len(entries))
	}
	if entries[0].Event != "run_finished" || entries[0].RunID != "rid" || entries[0].WebhookID != 7 {
		t.Errorf("entry mismatch: %+v", entries[0])
	}
	if !strings.Contains(entries[0].Body, "rid") {
		t.Errorf("body should contain run id, got %q", entries[0].Body)
	}
}

func TestSyncDeliveryEchoSubpath(t *testing.T) {
	t.Parallel()
	echo := NewEchoLog(10)
	d := &SyncDelivery{Echo: echo}
	wh := &model.Webhook{ID: 1, URL: "lmf://echo/named-channel"}
	if _, err := d.Deliver(wh, "run_started", &model.Run{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := echo.List(0); len(got) != 1 {
		t.Errorf("expected 1 entry on echo subpath, got %d", len(got))
	}
}

func TestSyncDeliveryEchoNilLog(t *testing.T) {
	t.Parallel()
	// Echo path with nil log should still return 200, just no recording.
	d := &SyncDelivery{Echo: nil}
	wh := &model.Webhook{ID: 1, URL: "lmf://echo"}
	status, err := d.Deliver(wh, "run_started", &model.Run{ID: "x"})
	if err != nil || status != http.StatusOK {
		t.Errorf("nil-echo deliver: status=%d err=%v", status, err)
	}
}

func TestSyncDeliveryHTTPRoundtrip(t *testing.T) {
	t.Parallel()
	var (
		gotEvent  string
		gotSig    string
		gotBodyOK bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvent = r.Header.Get("X-LiteMLflow-Event")
		gotSig = r.Header.Get("X-LiteMLflow-Signature")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "rid42") {
			gotBodyOK = true
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	d := &SyncDelivery{}
	wh := &model.Webhook{URL: srv.URL, Secret: "secret-key"}
	status, err := d.Deliver(wh, "run_finished", &model.Run{ID: "rid42", ExperimentID: 1, Status: "FINISHED"})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if status != http.StatusAccepted {
		t.Errorf("expected 202, got %d", status)
	}
	if gotEvent != "run_finished" {
		t.Errorf("event header missing/wrong: %q", gotEvent)
	}
	if !strings.HasPrefix(gotSig, "sha256=") || len(gotSig) != len("sha256=")+64 {
		t.Errorf("signature header malformed: %q", gotSig)
	}
	if !gotBodyOK {
		t.Error("expected body to contain run id")
	}
}

func TestSyncDeliveryNetworkError(t *testing.T) {
	t.Parallel()
	d := &SyncDelivery{}
	// 127.0.0.1:1 is reserved/closed → connection refused.
	wh := &model.Webhook{URL: "http://127.0.0.1:1/never"}
	status, err := d.Deliver(wh, "run_started", &model.Run{ID: "x"})
	if err == nil {
		t.Error("expected network error")
	}
	if status != 0 {
		t.Errorf("expected status=0 on network error, got %d", status)
	}
}

func TestSyncDeliveryBuildRequestError(t *testing.T) {
	t.Parallel()
	d := &SyncDelivery{}
	wh := &model.Webhook{URL: "://broken-url"}
	status, err := d.Deliver(wh, "run_started", &model.Run{ID: "x"})
	if err == nil {
		t.Error("expected build-request error")
	}
	if status != 0 {
		t.Errorf("expected status=0, got %d", status)
	}
}
