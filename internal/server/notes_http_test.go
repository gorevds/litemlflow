package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// TestRunNotesHTTP exercises the PUT/GET/DELETE (empty PUT) note endpoints.
func TestRunNotesHTTP(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// 1. Create an experiment.
	expBody := `{"name":"notes-http-exp"}`
	resp, err := http.Post(ts.URL+"/api/2.0/mlflow/experiments/create", "application/json", strings.NewReader(expBody))
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	var expResp struct {
		ExperimentID string `json:"experiment_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&expResp)
	resp.Body.Close()
	if expResp.ExperimentID == "" {
		t.Fatal("no experiment_id returned")
	}

	// 2. Create a run.
	runBody := `{"experiment_id":"` + expResp.ExperimentID + `"}`
	resp, err = http.Post(ts.URL+"/api/2.0/mlflow/runs/create", "application/json", strings.NewReader(runBody))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	var runResp struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&runResp)
	resp.Body.Close()
	runID := runResp.Run.Info.RunID
	if runID == "" {
		t.Fatal("no run_id returned")
	}

	// 3. GET note before any PUT → 404.
	resp, err = http.Get(ts.URL + "/api/v1/runs/" + runID + "/note")
	if err != nil {
		t.Fatalf("GET note (empty): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing note, got %d", resp.StatusCode)
	}

	// 4. PUT a note.
	const noteContent = "# Test note\n\nWith **markdown**."
	putBodyBytes, _ := json.Marshal(map[string]string{"content": noteContent})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/runs/"+runID+"/note", bytes.NewReader(putBodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT note: %v", err)
	}
	var putResp struct {
		Content   string `json:"content"`
		UpdatedAt int64  `json:"updated_at"`
		UpdatedBy string `json:"updated_by"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&putResp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT note: want 200, got %d", resp.StatusCode)
	}
	if putResp.Content != noteContent {
		t.Errorf("content mismatch: got %q, want %q", putResp.Content, noteContent)
	}
	if putResp.UpdatedAt <= 0 {
		t.Error("updated_at should be positive")
	}

	// 5. GET note → should return the note.
	resp, err = http.Get(ts.URL + "/api/v1/runs/" + runID + "/note")
	if err != nil {
		t.Fatalf("GET note: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET note: want 200, got %d; body=%s", resp.StatusCode, body)
	}
	var getResp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getResp.Content != noteContent {
		t.Errorf("GET content mismatch: got %q, want %q", getResp.Content, noteContent)
	}

	// 6. Empty PUT → deletes the note.
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/v1/runs/"+runID+"/note", strings.NewReader(`{"content":""}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT empty note: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT empty note: want 200, got %d", resp.StatusCode)
	}

	// 7. GET note after delete → 404.
	resp, err = http.Get(ts.URL + "/api/v1/runs/" + runID + "/note")
	if err != nil {
		t.Fatalf("GET note after delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete: expected 404, got %d", resp.StatusCode)
	}
}
