// provider_test.go — unit tests for the LiteMLflow Terraform provider.
//
// All tests use httptest.NewServer with an in-memory fake LiteMLflow server.
// No Terraform binary or live infrastructure is required.
//
// The tests exercise the HTTP client layer and the provider schema/resource
// logic directly:
//
//  1. Experiment CRUD via the client.
//  2. Prompt create, version handling, alias create/update/delete.
//  3. Tag drift detection: mutate out-of-band, verify client reflects new state.
//  4. Provider config validation: missing URL → error.
//  5. Registered model CRUD.
//  6. Workspace CRUD.
package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ── fake LiteMLflow server ───────────────────────────────────────────────────

// fakeServer is an in-memory LiteMLflow that implements the subset of endpoints
// used by the provider.
type fakeServer struct {
	mu sync.Mutex

	experiments      map[string]*experimentInfo
	nextExpID        int
	experimentByName map[string]string // name → id

	// prompts: name → []promptInfo (index+1 = version)
	prompts       map[string][]*promptInfo
	promptAliases map[string]int // "name/alias" → version

	registeredModels map[string]*registeredModelInfo
	workspaces       map[string]*workspaceInfo
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		experiments:      make(map[string]*experimentInfo),
		experimentByName: make(map[string]string),
		prompts:          make(map[string][]*promptInfo),
		promptAliases:    make(map[string]int),
		registeredModels: make(map[string]*registeredModelInfo),
		workspaces:       make(map[string]*workspaceInfo),
		nextExpID:        1,
	}
}

func (s *fakeServer) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeServer) writeError(w http.ResponseWriter, status int, code, msg string) {
	s.writeJSON(w, status, map[string]string{"error_code": code, "message": msg})
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := r.URL.Path
	method := r.Method

	switch {
	// ── Experiments ──────────────────────────────────────────────────────────
	case method == http.MethodPost && path == "/api/2.0/mlflow/experiments/create":
		var req struct {
			Name             string      `json:"name"`
			ArtifactLocation string      `json:"artifact_location"`
			Tags             []mlflowTag `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, exists := s.experimentByName[req.Name]; exists {
			s.writeError(w, 400, "RESOURCE_ALREADY_EXISTS", "already exists")
			return
		}
		id := strconv.Itoa(s.nextExpID)
		s.nextExpID++
		loc := req.ArtifactLocation
		if loc == "" {
			loc = "mlflow-artifacts:/" + id
		}
		s.experiments[id] = &experimentInfo{
			ExperimentID:     id,
			Name:             req.Name,
			ArtifactLocation: loc,
			LifecycleStage:   "active",
			Tags:             req.Tags,
		}
		s.experimentByName[req.Name] = id
		s.writeJSON(w, 200, map[string]string{"experiment_id": id})

	case method == http.MethodGet && path == "/api/2.0/mlflow/experiments/get":
		id := r.URL.Query().Get("experiment_id")
		exp, ok := s.experiments[id]
		if !ok || exp.LifecycleStage == "deleted" {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		s.writeJSON(w, 200, map[string]interface{}{"experiment": exp})

	case method == http.MethodGet && path == "/api/2.0/mlflow/experiments/get-by-name":
		name := r.URL.Query().Get("experiment_name")
		id, ok := s.experimentByName[name]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		exp := s.experiments[id]
		if exp.LifecycleStage == "deleted" {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		s.writeJSON(w, 200, map[string]interface{}{"experiment": exp})

	case method == http.MethodPost && path == "/api/2.0/mlflow/experiments/update":
		var req struct {
			ExperimentID string `json:"experiment_id"`
			NewName      string `json:"new_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		exp, ok := s.experiments[req.ExperimentID]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		delete(s.experimentByName, exp.Name)
		exp.Name = req.NewName
		s.experimentByName[req.NewName] = req.ExperimentID
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodPost && path == "/api/2.0/mlflow/experiments/set-experiment-tag":
		var req struct {
			ExperimentID string `json:"experiment_id"`
			Key          string `json:"key"`
			Value        string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		exp, ok := s.experiments[req.ExperimentID]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		for i, t := range exp.Tags {
			if t.Key == req.Key {
				exp.Tags[i].Value = req.Value
				s.writeJSON(w, 200, map[string]interface{}{})
				return
			}
		}
		exp.Tags = append(exp.Tags, mlflowTag{Key: req.Key, Value: req.Value})
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodPost && path == "/api/2.0/mlflow/experiments/delete":
		var req struct {
			ExperimentID string `json:"experiment_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		exp, ok := s.experiments[req.ExperimentID]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		exp.LifecycleStage = "deleted"
		s.writeJSON(w, 200, map[string]interface{}{})

	// ── Prompts ──────────────────────────────────────────────────────────────
	case method == http.MethodPost && path == "/api/v1/prompts":
		var req createPromptRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		versions := s.prompts[req.Name]
		for _, v := range versions {
			if v.Content == req.Content {
				s.writeJSON(w, 200, createPromptResponse{Version: v.Version, ContentHash: v.ContentHash})
				return
			}
		}
		ver := len(versions) + 1
		hash := fmt.Sprintf("sha256-%d-%d", len(req.Content), ver)
		p := &promptInfo{
			Name:        req.Name,
			Version:     ver,
			Content:     req.Content,
			ContentHash: hash,
			Description: req.Description,
		}
		s.prompts[req.Name] = append(versions, p)
		s.writeJSON(w, 200, createPromptResponse{Version: ver, ContentHash: hash})

	case method == http.MethodGet && strings.HasPrefix(path, "/api/v1/prompts/"):
		rest := strings.TrimPrefix(path, "/api/v1/prompts/")
		parts := strings.SplitN(rest, "/", 3)
		name := parts[0]

		if len(parts) == 1 {
			versions := s.prompts[name]
			if len(versions) == 0 {
				s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
				return
			}
			s.writeJSON(w, 200, versions[len(versions)-1])
			return
		}
		if len(parts) == 3 && parts[1] == "versions" {
			n, _ := strconv.Atoi(parts[2])
			versions := s.prompts[name]
			if n < 1 || n > len(versions) {
				s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
				return
			}
			s.writeJSON(w, 200, versions[n-1])
			return
		}
		if len(parts) == 3 && parts[1] == "aliases" {
			alias := parts[2]
			ver, ok := s.promptAliases[name+"/"+alias]
			if !ok {
				s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
				return
			}
			s.writeJSON(w, 200, promptAliasInfo{Alias: alias, Version: ver, Name: name})
			return
		}
		s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")

	case method == http.MethodPost && strings.HasSuffix(path, "/aliases"):
		rest := strings.TrimPrefix(path, "/api/v1/prompts/")
		name := strings.TrimSuffix(rest, "/aliases")
		var req struct {
			Alias   string `json:"alias"`
			Version int    `json:"version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.promptAliases[name+"/"+req.Alias] = req.Version
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodDelete && strings.Contains(path, "/aliases/"):
		rest := strings.TrimPrefix(path, "/api/v1/prompts/")
		parts := strings.SplitN(rest, "/aliases/", 2)
		if len(parts) == 2 {
			delete(s.promptAliases, parts[0]+"/"+parts[1])
		}
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/prompts/"):
		// DELETE /api/v1/prompts/{name}/versions/{n} — best-effort.
		s.writeJSON(w, 200, map[string]interface{}{})

	// ── Registered Models ────────────────────────────────────────────────────
	case method == http.MethodPost && path == "/api/2.0/mlflow/registered-models/create":
		var req struct {
			Name        string      `json:"name"`
			Description string      `json:"description"`
			Tags        []mlflowTag `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, exists := s.registeredModels[req.Name]; exists {
			s.writeError(w, 400, "RESOURCE_ALREADY_EXISTS", "already exists")
			return
		}
		s.registeredModels[req.Name] = &registeredModelInfo{
			Name:        req.Name,
			Description: req.Description,
			Tags:        req.Tags,
		}
		s.writeJSON(w, 200, map[string]interface{}{"registered_model": s.registeredModels[req.Name]})

	case method == http.MethodGet && path == "/api/2.0/mlflow/registered-models/get":
		name := r.URL.Query().Get("name")
		m, ok := s.registeredModels[name]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		s.writeJSON(w, 200, map[string]interface{}{"registered_model": m})

	case method == http.MethodPost && path == "/api/2.0/mlflow/registered-models/update":
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m, ok := s.registeredModels[req.Name]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		m.Description = req.Description
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodPost && path == "/api/2.0/mlflow/registered-models/set-tag":
		var req struct {
			Name  string `json:"name"`
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m, ok := s.registeredModels[req.Name]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		for i, t := range m.Tags {
			if t.Key == req.Key {
				m.Tags[i].Value = req.Value
				s.writeJSON(w, 200, map[string]interface{}{})
				return
			}
		}
		m.Tags = append(m.Tags, mlflowTag{Key: req.Key, Value: req.Value})
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodPost && path == "/api/2.0/mlflow/registered-models/delete-tag":
		var req struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m, ok := s.registeredModels[req.Name]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		newTags := m.Tags[:0]
		for _, t := range m.Tags {
			if t.Key != req.Key {
				newTags = append(newTags, t)
			}
		}
		m.Tags = newTags
		s.writeJSON(w, 200, map[string]interface{}{})

	case method == http.MethodPost && path == "/api/2.0/mlflow/registered-models/delete":
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := s.registeredModels[req.Name]; !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		delete(s.registeredModels, req.Name)
		s.writeJSON(w, 200, map[string]interface{}{})

	// ── Workspaces ───────────────────────────────────────────────────────────
	case method == http.MethodPost && path == "/api/v1/workspaces":
		var req createWorkspaceRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, exists := s.workspaces[req.ID]; exists {
			s.writeError(w, 400, "RESOURCE_ALREADY_EXISTS", "already exists")
			return
		}
		ws := &workspaceInfo{ID: req.ID, Name: req.Name, Description: req.Description}
		s.workspaces[req.ID] = ws
		s.writeJSON(w, 201, ws)

	case method == http.MethodGet && strings.HasPrefix(path, "/api/v1/workspaces/"):
		id := strings.TrimPrefix(path, "/api/v1/workspaces/")
		ws, ok := s.workspaces[id]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		s.writeJSON(w, 200, ws)

	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/workspaces/"):
		id := strings.TrimPrefix(path, "/api/v1/workspaces/")
		ws, ok := s.workspaces[id]
		if !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "" {
			ws.Name = req.Name
		}
		ws.Description = req.Description
		s.writeJSON(w, 200, ws)

	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/workspaces/"):
		id := strings.TrimPrefix(path, "/api/v1/workspaces/")
		if _, ok := s.workspaces[id]; !ok {
			s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "not found")
			return
		}
		delete(s.workspaces, id)
		s.writeJSON(w, 200, map[string]interface{}{})

	default:
		s.writeError(w, 404, "RESOURCE_DOES_NOT_EXIST", "unhandled: "+method+" "+path)
	}
}

// ── helper: start a test server and return a client ──────────────────────────

func testClientAndServer(t *testing.T) (*Client, *fakeServer, *httptest.Server) {
	t.Helper()
	fake := newFakeServer()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := newClient(srv.URL, "", "")
	return client, fake, srv
}

// ── Test 1: Create + Read + Delete an experiment ─────────────────────────────

func TestClient_ExperimentCRUD(t *testing.T) {
	client, _, _ := testClientAndServer(t)

	// Create.
	id, err := client.CreateExperiment("my-experiment", "mlflow-artifacts:/test", map[string]string{
		"team": "ml-platform",
		"env":  "test",
	})
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty experiment id")
	}

	// Read by ID.
	info, err := client.GetExperimentByID(id)
	if err != nil {
		t.Fatalf("GetExperimentByID: %v", err)
	}
	if info.Name != "my-experiment" {
		t.Errorf("name: got %q, want %q", info.Name, "my-experiment")
	}
	if info.ArtifactLocation != "mlflow-artifacts:/test" {
		t.Errorf("artifact_location: got %q, want mlflow-artifacts:/test", info.ArtifactLocation)
	}
	tagsMap := make(map[string]string)
	for _, tg := range info.Tags {
		tagsMap[tg.Key] = tg.Value
	}
	if tagsMap["team"] != "ml-platform" {
		t.Errorf("tag team: got %q, want ml-platform", tagsMap["team"])
	}

	// Read by name.
	info2, err := client.GetExperimentByName("my-experiment")
	if err != nil {
		t.Fatalf("GetExperimentByName: %v", err)
	}
	if info2.ExperimentID != id {
		t.Errorf("id mismatch: got %q, want %q", info2.ExperimentID, id)
	}

	// Update (rename).
	if err := client.UpdateExperiment(id, "my-experiment-renamed"); err != nil {
		t.Fatalf("UpdateExperiment: %v", err)
	}
	info3, err := client.GetExperimentByID(id)
	if err != nil {
		t.Fatalf("GetExperimentByID after rename: %v", err)
	}
	if info3.Name != "my-experiment-renamed" {
		t.Errorf("renamed: got %q, want my-experiment-renamed", info3.Name)
	}

	// Delete.
	if err := client.DeleteExperiment(id); err != nil {
		t.Fatalf("DeleteExperiment: %v", err)
	}
	_, err = client.GetExperimentByID(id)
	if !isNotFound(err) {
		t.Errorf("expected not-found after delete, got %v", err)
	}
}

// ── Test 2: Prompt create, versioning, alias lifecycle ───────────────────────

func TestClient_PromptAndAlias(t *testing.T) {
	client, _, _ := testClientAndServer(t)

	// Create prompt v1.
	ver1, hash1, err := client.CreatePrompt("rag.system", "You are a helpful assistant.", "v1", nil)
	if err != nil {
		t.Fatalf("CreatePrompt v1: %v", err)
	}
	if ver1 != 1 {
		t.Errorf("expected version 1, got %d", ver1)
	}
	if hash1 == "" {
		t.Error("expected non-empty content hash")
	}

	// Read latest → should be v1.
	latest, err := client.GetPromptLatest("rag.system")
	if err != nil {
		t.Fatalf("GetPromptLatest: %v", err)
	}
	if latest.Version != 1 {
		t.Errorf("latest version: got %d, want 1", latest.Version)
	}
	if latest.Content != "You are a helpful assistant." {
		t.Errorf("content mismatch: %q", latest.Content)
	}

	// Identical content → same version (content-addressed).
	ver1again, _, err := client.CreatePrompt("rag.system", "You are a helpful assistant.", "v1", nil)
	if err != nil {
		t.Fatalf("CreatePrompt duplicate: %v", err)
	}
	if ver1again != 1 {
		t.Errorf("expected version 1 for duplicate content, got %d", ver1again)
	}

	// New content → v2.
	ver2, _, err := client.CreatePrompt("rag.system", "You are a highly capable assistant.", "v2", nil)
	if err != nil {
		t.Fatalf("CreatePrompt v2: %v", err)
	}
	if ver2 != 2 {
		t.Errorf("expected version 2 for new content, got %d", ver2)
	}

	// Set alias "production" → v1.
	if err := client.SetPromptAlias("rag.system", "production", 1); err != nil {
		t.Fatalf("SetPromptAlias → v1: %v", err)
	}
	aliasInfo, err := client.GetPromptAlias("rag.system", "production")
	if err != nil {
		t.Fatalf("GetPromptAlias: %v", err)
	}
	if aliasInfo.Version != 1 {
		t.Errorf("alias version: got %d, want 1", aliasInfo.Version)
	}

	// Update alias → v2.
	if err := client.SetPromptAlias("rag.system", "production", 2); err != nil {
		t.Fatalf("SetPromptAlias → v2: %v", err)
	}
	aliasInfo2, err := client.GetPromptAlias("rag.system", "production")
	if err != nil {
		t.Fatalf("GetPromptAlias after update: %v", err)
	}
	if aliasInfo2.Version != 2 {
		t.Errorf("alias version after update: got %d, want 2", aliasInfo2.Version)
	}

	// Delete alias.
	if err := client.DeletePromptAlias("rag.system", "production"); err != nil {
		t.Fatalf("DeletePromptAlias: %v", err)
	}
	_, err = client.GetPromptAlias("rag.system", "production")
	if !isNotFound(err) {
		t.Errorf("expected not-found after alias delete, got %v", err)
	}
}

// ── Test 3: Tag drift detection ───────────────────────────────────────────────
//
// Simulates an out-of-band tag change and verifies that the client reflects
// the mutated state (i.e., Read picks up the new value).

func TestClient_ExperimentTagDrift(t *testing.T) {
	client, fake, _ := testClientAndServer(t)

	id, err := client.CreateExperiment("drift-exp", "", map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	// Read back — should see owner=alice.
	info, err := client.GetExperimentByID(id)
	if err != nil {
		t.Fatalf("GetExperimentByID: %v", err)
	}
	tagsMap := tagsSliceToMap(info.Tags)
	if tagsMap["owner"] != "alice" {
		t.Errorf("before drift: owner = %q, want alice", tagsMap["owner"])
	}

	// Out-of-band mutation: change owner to bob directly on the fake server.
	fake.mu.Lock()
	for i, tg := range fake.experiments[id].Tags {
		if tg.Key == "owner" {
			fake.experiments[id].Tags[i].Value = "bob"
		}
	}
	fake.mu.Unlock()

	// Read again — client should now see owner=bob (drift detected).
	info2, err := client.GetExperimentByID(id)
	if err != nil {
		t.Fatalf("GetExperimentByID after drift: %v", err)
	}
	tagsMap2 := tagsSliceToMap(info2.Tags)
	if tagsMap2["owner"] != "bob" {
		t.Errorf("after drift: owner = %q, want bob", tagsMap2["owner"])
	}

	// Apply corrective action (upsert the planned tag back to alice).
	if err := client.SetExperimentTag(id, "owner", "alice"); err != nil {
		t.Fatalf("SetExperimentTag correction: %v", err)
	}
	info3, err := client.GetExperimentByID(id)
	if err != nil {
		t.Fatalf("GetExperimentByID after correction: %v", err)
	}
	tagsMap3 := tagsSliceToMap(info3.Tags)
	if tagsMap3["owner"] != "alice" {
		t.Errorf("after correction: owner = %q, want alice", tagsMap3["owner"])
	}
}

// ── Test 4: Provider config validation — missing URL ─────────────────────────
//
// When no URL is configured, the provider's Configure method emits an error.
// We verify the underlying mechanism: newClient with an empty base URL returns
// errors on every API call because http.NewRequest rejects an empty URL.

func TestProvider_MissingURL(t *testing.T) {
	client := newClient("", "", "")
	_, err := client.GetExperimentByID("1")
	if err == nil {
		t.Error("expected error when using client with empty URL; got nil")
	}
}

// ── Test 5: Registered model CRUD ─────────────────────────────────────────────

func TestClient_RegisteredModelCRUD(t *testing.T) {
	client, _, _ := testClientAndServer(t)

	if err := client.CreateRegisteredModel("my-model", "Initial description", map[string]string{"framework": "sklearn"}); err != nil {
		t.Fatalf("CreateRegisteredModel: %v", err)
	}

	m, err := client.GetRegisteredModel("my-model")
	if err != nil {
		t.Fatalf("GetRegisteredModel: %v", err)
	}
	if m.Name != "my-model" {
		t.Errorf("name: got %q, want my-model", m.Name)
	}
	if m.Description != "Initial description" {
		t.Errorf("description: got %q", m.Description)
	}
	tagsMap := tagsSliceToMap(m.Tags)
	if tagsMap["framework"] != "sklearn" {
		t.Errorf("tag framework: got %q, want sklearn", tagsMap["framework"])
	}

	// Update description.
	if err := client.UpdateRegisteredModel("my-model", "Updated description"); err != nil {
		t.Fatalf("UpdateRegisteredModel: %v", err)
	}

	// Set a new tag, delete the old one.
	if err := client.SetRegisteredModelTag("my-model", "framework", "pytorch"); err != nil {
		t.Fatalf("SetRegisteredModelTag: %v", err)
	}
	if err := client.DeleteRegisteredModelTag("my-model", "framework"); err != nil {
		t.Fatalf("DeleteRegisteredModelTag: %v", err)
	}

	m2, err := client.GetRegisteredModel("my-model")
	if err != nil {
		t.Fatalf("GetRegisteredModel after update: %v", err)
	}
	if m2.Description != "Updated description" {
		t.Errorf("updated description: got %q", m2.Description)
	}
	tagsMap2 := tagsSliceToMap(m2.Tags)
	if _, ok := tagsMap2["framework"]; ok {
		t.Error("expected framework tag to be deleted")
	}

	// Delete.
	if err := client.DeleteRegisteredModel("my-model"); err != nil {
		t.Fatalf("DeleteRegisteredModel: %v", err)
	}
	_, err = client.GetRegisteredModel("my-model")
	if !isNotFound(err) {
		t.Errorf("expected not-found after delete, got %v", err)
	}
}

// ── Test 6: Workspace CRUD ───────────────────────────────────────────────────

func TestClient_WorkspaceCRUD(t *testing.T) {
	client, _, _ := testClientAndServer(t)

	ws, err := client.CreateWorkspace("team-ml", "ML Team", "Main ML workspace")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws.ID != "team-ml" {
		t.Errorf("id: got %q, want team-ml", ws.ID)
	}

	ws2, err := client.GetWorkspace("team-ml")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws2.Name != "ML Team" {
		t.Errorf("name: got %q, want 'ML Team'", ws2.Name)
	}

	ws3, err := client.UpdateWorkspace("team-ml", "ML Team (renamed)", "New description")
	if err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	if ws3.Name != "ML Team (renamed)" {
		t.Errorf("updated name: got %q", ws3.Name)
	}

	if err := client.DeleteWorkspace("team-ml"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	_, err = client.GetWorkspace("team-ml")
	if !isNotFound(err) {
		t.Errorf("expected not-found after delete, got %v", err)
	}
}

// ── Test 7: isNotFound helper ─────────────────────────────────────────────────

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Error("nil should not be not-found")
	}
	if isNotFound(fmt.Errorf("some other error")) {
		t.Error("generic error should not be not-found")
	}
	if !isNotFound(&apiError{Code: "RESOURCE_DOES_NOT_EXIST", Message: "test"}) {
		t.Error("RESOURCE_DOES_NOT_EXIST should be not-found")
	}
}

// ── Test 8: urlEncode helper ──────────────────────────────────────────────────

func TestURLEncode(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"rag.system", "rag.system"},
		{"hello world", "hello%20world"},
		{"production-v2", "production-v2"},
		{"a/b", "a%2Fb"},
	}
	for _, c := range cases {
		got := urlEncode(c.input)
		if got != c.want {
			t.Errorf("urlEncode(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func tagsSliceToMap(tags []mlflowTag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}
