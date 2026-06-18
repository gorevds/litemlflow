// Acceptance test for v1.3 federation: 3 LiteMLflow instances, mutual
// peer registration, federated search returns experiments from all three.
//
// Each instance is a real httptest.Server (the Go *Server.New chain)
// with its own SQLite + workspace. The test:
//
//  1. Brings up 3 servers (A, B, C).
//  2. Calls POST /api/v1/federate/peers on A to add B and C as peers.
//  3. Symmetrically adds A as a peer on B and C (using the same secrets
//     that A's POST returned — same shape an operator would use to
//     paste them between dashboards).
//  4. Probes connectivity via POST /api/v1/federate/peers/{id}/echo on A.
//  5. Logs experiments on A, B, C with names that share a substring.
//  6. Calls GET /api/v1/search?q=<substring>&federated=1 on A.
//  7. Asserts the response carries hits from A, B, AND C, with their
//     `instance` fields set to the names A registered.
//
// This is the headline acceptance criterion in docs/roadmap-y2.md:
//
//	"3 LiteMLflow instances behind a single UI; searching ... returns
//	 experiments from all three."
package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// fedNode is one instance brought up by the test.
type fedNode struct {
	name string
	ts   *httptest.Server
}

// startFedNode spins up a server with its FederationName set so other
// peers can identify it.
func startFedNode(t *testing.T, name string) *fedNode {
	t.Helper()
	// Federation acceptance tests register peers at loopback (httptest)
	// addresses, which the SSRF guard (independent-review 2.3) blocks unless
	// the operator opts in. Setenv requires these tests to run non-parallel.
	t.Setenv("LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE", "1")
	ts, _ := newTestServer(t, config.Config{FederationName: name})
	return &fedNode{name: name, ts: ts}
}

// addPeerOn calls POST /api/v1/federate/peers on `caller`, registering
// `target` as a peer with the given secret. Returns the assigned peer ID.
func addPeerOn(t *testing.T, caller, target *fedNode, secret string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":   target.name,
		"url":    target.ts.URL,
		"secret": secret,
	})
	resp, err := http.Post(caller.ts.URL+"/api/v1/federate/peers",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("addPeer %s on %s: %v", target.name, caller.name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("addPeer status: %d body=%s", resp.StatusCode, raw)
	}
	var got struct {
		Peer struct {
			ID int64 `json:"id"`
		} `json:"peer"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode addPeer: %v body=%s", err, raw)
	}
	return got.Peer.ID
}

// echoPeer triggers the connectivity probe and asserts the resulting
// status is "connected".
func echoPeer(t *testing.T, caller *fedNode, peerID int64) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/federate/peers/%d/echo", caller.ts.URL, peerID)
	resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("echo status: %d body=%s", resp.StatusCode, raw)
	}
	var got struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	_ = json.Unmarshal(raw, &got)
	if got.Status != "connected" {
		t.Fatalf("expected connected, got %+v", got)
	}
}

// createExperiment asks the MLflow create-experiment endpoint on the
// given node to make an experiment with the given name.
func createExperiment(t *testing.T, node *fedNode, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"name": %q}`, name)
	resp, err := http.Post(node.ts.URL+"/api/2.0/mlflow/experiments/create",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("createExperiment %s on %s: %v", name, node.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("createExperiment status: %d body=%s", resp.StatusCode, raw)
	}
}

// federatedSearch calls GET /api/v1/search?q=...&federated=1 on `from`
// and decodes the response.
func federatedSearch(t *testing.T, from *fedNode, q string) struct {
	Items []struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Instance string `json:"instance"`
	} `json:"items"`
	Federated bool     `json:"federated"`
	Instances []string `json:"instances"`
	Partial   bool     `json:"partial"`
} {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/search?q=%s&federated=1", from.ts.URL, q)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status: %d body=%s", resp.StatusCode, raw)
	}
	var got struct {
		Items []struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			Instance string `json:"instance"`
		} `json:"items"`
		Federated bool     `json:"federated"`
		Instances []string `json:"instances"`
		Partial   bool     `json:"partial"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode search: %v body=%s", err, raw)
	}
	return got
}

// createPrompt registers a prompt by name on a node. v1 always.
func createPrompt(t *testing.T, node *fedNode, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"content":"hello %s","description":""}`, name, name)
	resp, err := http.Post(node.ts.URL+"/api/v1/prompts",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("createPrompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("createPrompt status: %d body=%s", resp.StatusCode, raw)
	}
}

// TestFederationAddPeerRejectsSSRF guards independent-review finding 2.3:
// peer URLs were stored verbatim with no SSRF validation, so AddPeer could be
// pointed at the cloud-metadata endpoint or internal services. Without the
// allow-private override these must be rejected at registration with 400.
// (Uses newTestServer directly — NOT startFedNode — so the override is unset.)
func TestFederationAddPeerRejectsSSRF(t *testing.T) {
	ts, _ := newTestServer(t, config.Config{FederationName: "lmf-guard"})
	// Use only literal IPs so the assertions are hermetic: Go resolves literal
	// IPs without a DNS lookup, and validateOutboundURL fails open on resolve
	// errors — a hostname case would be non-deterministic in offline CI.
	for _, badURL := range []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://127.0.0.1:9999/",                   // loopback
		"http://10.0.0.5/admin",                    // RFC1918
	} {
		body := fmt.Sprintf(`{"name":"evil","url":%q}`, badURL)
		resp, err := http.Post(ts.URL+"/api/v1/federate/peers",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post %s: %v", badURL, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("AddPeer(%s): want 400, got %d: %s", badURL, resp.StatusCode, raw)
		}
	}

	// A public address must still be accepted (positive control). Literal
	// public IP keeps the test hermetic — no DNS dependency.
	body := `{"name":"ok","url":"http://93.184.216.34/"}`
	resp, err := http.Post(ts.URL+"/api/v1/federate/peers",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post good url: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("AddPeer(public url): want 200, got %d: %s", resp.StatusCode, raw)
	}
}

// TestFederationSearchPrompts guards v1.3 deferred item M6: a federated
// search with kind=prompts (or default kind=all) returns prompt hits from
// peers, not just runs/experiments.
func TestFederationSearchPrompts(t *testing.T) {
	a := startFedNode(t, "lmf-prom-A")
	b := startFedNode(t, "lmf-prom-B")
	sec := strings.Repeat("ab", 32)
	addPeerOn(t, a, b, sec)
	addPeerOn(t, b, a, sec)
	echoPeer(t, a, 1)

	// B has the prompt the user is searching for; A has nothing.
	createPrompt(t, b, "fed-prompt-rag")

	got := federatedSearch(t, a, "fed-prompt-rag")
	foundRemote := false
	for _, item := range got.Items {
		if item.Kind == "prompt" && item.Instance == "lmf-prom-B" {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Errorf("expected to find prompt on lmf-prom-B via federated search; got items=%+v", got.Items)
	}
}

// TestFederationAcceptance — the headline: 3 instances, federated search.
func TestFederationAcceptance(t *testing.T) {
	a := startFedNode(t, "lmf-A")
	b := startFedNode(t, "lmf-B")
	c := startFedNode(t, "lmf-C")

	// Generate two shared secrets — one per (A,B) and (A,C) relationship.
	// In real usage, the caller's POST returns the secret; we pre-make
	// matching pairs so the test deterministically registers both sides.
	secAB := strings.Repeat("ab", 32) // 64-hex → 32 bytes
	secAC := strings.Repeat("cd", 32)

	// A learns about B and C.
	pidB := addPeerOn(t, a, b, secAB)
	pidC := addPeerOn(t, a, c, secAC)

	// B learns about A (using the same A↔B secret).
	addPeerOn(t, b, a, secAB)
	// C learns about A.
	addPeerOn(t, c, a, secAC)

	// Echo to verify mutual HMAC works and to flip status to "connected".
	echoPeer(t, a, pidB)
	echoPeer(t, a, pidC)

	// Each node logs an experiment with the same substring.
	createExperiment(t, a, "fed-shared-exp-A")
	createExperiment(t, b, "fed-shared-exp-B")
	createExperiment(t, c, "fed-shared-exp-C")

	// Federated search from A should see all three.
	got := federatedSearch(t, a, "fed-shared")

	if !got.Federated {
		t.Errorf("expected federated=true in response, got %+v", got)
	}
	if got.Partial {
		t.Errorf("expected no partial errors, got partial=true")
	}

	// Collect distinct instance names from the items.
	gotInstances := map[string]int{}
	for _, item := range got.Items {
		if item.Kind == "experiment" {
			gotInstances[item.Instance]++
		}
	}
	for _, want := range []string{"lmf-A", "lmf-B", "lmf-C"} {
		if gotInstances[want] == 0 {
			t.Errorf("expected at least one experiment from %q, items=%+v", want, got.Items)
		}
	}
	t.Logf("federated search returned %d items across instances %v",
		len(got.Items), got.Instances)
}

// TestFederationDoesNotLeakPeerNames guards independent-review finding H2.
// Both an unknown-peer-name request and a valid-name-but-wrong-secret
// request must return the same opaque error body so an attacker cannot
// enumerate registered peer names by error shape.
func TestFederationDoesNotLeakPeerNames(t *testing.T) {
	a := startFedNode(t, "lmf-leak-A")
	b := startFedNode(t, "lmf-leak-B")
	good := strings.Repeat("ab", 32)
	addPeerOn(t, a, b, good)

	post := func(peerHeader string) (status int, body string) {
		req, _ := http.NewRequest("POST",
			a.ts.URL+"/api/v1/federate/search",
			bytes.NewReader([]byte(`{"q":"x"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LiteMLflow-Federate-Peer", peerHeader)
		req.Header.Set("X-LiteMLflow-Federate-Ts",
			fmt.Sprintf("%d", 1747000000000))
		req.Header.Set("X-LiteMLflow-Federate-Sig", "sha256=deadbeef")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// Both should be 401 with the same opaque body.
	unknownStatus, unknownBody := post("nobody-here")
	wrongSecretStatus, wrongSecretBody := post("lmf-leak-B") // valid name but bad sig

	if unknownStatus != http.StatusUnauthorized || wrongSecretStatus != http.StatusUnauthorized {
		t.Fatalf("expected both 401; got %d / %d", unknownStatus, wrongSecretStatus)
	}
	if unknownBody != wrongSecretBody {
		t.Errorf("error body should be identical to prevent peer-name enumeration:\n  unknown=%q\n  wrong-secret=%q",
			unknownBody, wrongSecretBody)
	}
}

// TestFederationRejectsBadHMAC ensures a peer with the wrong secret can't
// invoke /api/v1/federate/search.
func TestFederationRejectsBadHMAC(t *testing.T) {
	a := startFedNode(t, "lmf-A-rej")
	b := startFedNode(t, "lmf-B-rej")
	good := strings.Repeat("ee", 32)
	addPeerOn(t, a, b, good)
	addPeerOn(t, b, a, good)
	echoPeer(t, a, 1)

	// Now hand-craft a request with a wrong signature header.
	body := []byte(`{"q":"x"}`)
	req, _ := http.NewRequest("POST",
		a.ts.URL+"/api/v1/federate/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LiteMLflow-Federate-Peer", "lmf-B-rej")
	req.Header.Set("X-LiteMLflow-Federate-Ts", "1")
	req.Header.Set("X-LiteMLflow-Federate-Sig", "sha256=deadbeef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 401 for tampered request, got %d (%s)", resp.StatusCode, raw)
	}
}
