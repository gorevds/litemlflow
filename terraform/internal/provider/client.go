// client.go — thin HTTP client that the provider uses to talk to LiteMLflow.
// No third-party dependencies beyond stdlib; mirrors the pattern in python/litemlflow/client.py.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal HTTP wrapper around the LiteMLflow REST API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	username   string
	password   string
}

// newClient constructs a Client. password may be empty for anonymous access.
func newClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// apiError represents the error shape returned by LiteMLflow.
type apiError struct {
	Code    string `json:"error_code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// do executes an HTTP request and JSON-decodes the response body into out (if not nil).
// Non-2xx responses are decoded as apiError and returned as an error.
func (c *Client) do(method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae apiError
		if jsonErr := json.Unmarshal(raw, &ae); jsonErr == nil && ae.Code != "" {
			return &ae
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// get is a convenience wrapper for GET with query parameters appended to path.
func (c *Client) get(path string, out interface{}) error {
	return c.do(http.MethodGet, path, nil, out)
}

// post is a convenience wrapper for POST.
func (c *Client) post(path string, body, out interface{}) error {
	return c.do(http.MethodPost, path, body, out)
}

// patch is a convenience wrapper for PATCH.
func (c *Client) patch(path string, body, out interface{}) error {
	return c.do(http.MethodPatch, path, body, out)
}

// delete is a convenience wrapper for DELETE.
func (c *Client) delete(path string, body interface{}) error {
	return c.do(http.MethodDelete, path, body, nil)
}

// ── Experiment helpers ───────────────────────────────────────────────────────

type mlflowTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type experimentInfo struct {
	ExperimentID     string      `json:"experiment_id"`
	Name             string      `json:"name"`
	ArtifactLocation string      `json:"artifact_location"`
	LifecycleStage   string      `json:"lifecycle_stage"`
	Tags             []mlflowTag `json:"tags"`
}

type createExperimentRequest struct {
	Name             string      `json:"name"`
	ArtifactLocation string      `json:"artifact_location,omitempty"`
	Tags             []mlflowTag `json:"tags,omitempty"`
}

type createExperimentResponse struct {
	ExperimentID string `json:"experiment_id"`
}

type experimentWrapper struct {
	Experiment experimentInfo `json:"experiment"`
}

func (c *Client) CreateExperiment(name, artifactLocation string, tags map[string]string) (string, error) {
	req := createExperimentRequest{Name: name, ArtifactLocation: artifactLocation}
	for k, v := range tags {
		req.Tags = append(req.Tags, mlflowTag{Key: k, Value: v})
	}
	var resp createExperimentResponse
	if err := c.post("/api/2.0/mlflow/experiments/create", req, &resp); err != nil {
		return "", err
	}
	return resp.ExperimentID, nil
}

func (c *Client) GetExperimentByID(id string) (*experimentInfo, error) {
	var w experimentWrapper
	if err := c.get("/api/2.0/mlflow/experiments/get?experiment_id="+id, &w); err != nil {
		return nil, err
	}
	return &w.Experiment, nil
}

func (c *Client) GetExperimentByName(name string) (*experimentInfo, error) {
	var w experimentWrapper
	if err := c.get("/api/2.0/mlflow/experiments/get-by-name?experiment_name="+urlEncode(name), &w); err != nil {
		return nil, err
	}
	return &w.Experiment, nil
}

func (c *Client) UpdateExperiment(id, newName string) error {
	return c.post("/api/2.0/mlflow/experiments/update", map[string]string{
		"experiment_id": id,
		"new_name":      newName,
	}, nil)
}

func (c *Client) SetExperimentTag(id, key, value string) error {
	return c.post("/api/2.0/mlflow/experiments/set-experiment-tag", map[string]string{
		"experiment_id": id,
		"key":           key,
		"value":         value,
	}, nil)
}

func (c *Client) DeleteExperiment(id string) error {
	return c.post("/api/2.0/mlflow/experiments/delete", map[string]string{
		"experiment_id": id,
	}, nil)
}

// ── Registered Model helpers ─────────────────────────────────────────────────

type registeredModelInfo struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Tags        []mlflowTag `json:"tags"`
}

type registeredModelWrapper struct {
	RegisteredModel registeredModelInfo `json:"registered_model"`
}

func (c *Client) CreateRegisteredModel(name, description string, tags map[string]string) error {
	type req struct {
		Name        string      `json:"name"`
		Description string      `json:"description,omitempty"`
		Tags        []mlflowTag `json:"tags,omitempty"`
	}
	r := req{Name: name, Description: description}
	for k, v := range tags {
		r.Tags = append(r.Tags, mlflowTag{Key: k, Value: v})
	}
	return c.post("/api/2.0/mlflow/registered-models/create", r, nil)
}

func (c *Client) GetRegisteredModel(name string) (*registeredModelInfo, error) {
	var w registeredModelWrapper
	if err := c.get("/api/2.0/mlflow/registered-models/get?name="+urlEncode(name), &w); err != nil {
		return nil, err
	}
	return &w.RegisteredModel, nil
}

func (c *Client) UpdateRegisteredModel(name, description string) error {
	return c.post("/api/2.0/mlflow/registered-models/update", map[string]string{
		"name":        name,
		"description": description,
	}, nil)
}

func (c *Client) SetRegisteredModelTag(name, key, value string) error {
	return c.post("/api/2.0/mlflow/registered-models/set-tag", map[string]string{
		"name":  name,
		"key":   key,
		"value": value,
	}, nil)
}

func (c *Client) DeleteRegisteredModelTag(name, key string) error {
	return c.post("/api/2.0/mlflow/registered-models/delete-tag", map[string]string{
		"name": name,
		"key":  key,
	}, nil)
}

func (c *Client) DeleteRegisteredModel(name string) error {
	return c.post("/api/2.0/mlflow/registered-models/delete", map[string]string{
		"name": name,
	}, nil)
}

// ── Prompt helpers ───────────────────────────────────────────────────────────

type promptInfo struct {
	Name        string      `json:"name"`
	Version     int         `json:"version"`
	Content     string      `json:"content"`
	ContentHash string      `json:"content_hash"`
	Description string      `json:"description"`
	Tags        []mlflowTag `json:"tags"`
}

type createPromptRequest struct {
	Name        string            `json:"name"`
	Content     string            `json:"content"`
	Description string            `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type createPromptResponse struct {
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

func (c *Client) CreatePrompt(name, content, description string, tags map[string]string) (int, string, error) {
	req := createPromptRequest{
		Name:        name,
		Content:     content,
		Description: description,
	}
	if len(tags) > 0 {
		req.Tags = tags
	}
	var resp createPromptResponse
	if err := c.post("/api/v1/prompts", req, &resp); err != nil {
		return 0, "", err
	}
	return resp.Version, resp.ContentHash, nil
}

func (c *Client) GetPromptLatest(name string) (*promptInfo, error) {
	var p promptInfo
	if err := c.get("/api/v1/prompts/"+urlEncode(name), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) GetPromptVersion(name string, version int) (*promptInfo, error) {
	var p promptInfo
	path := fmt.Sprintf("/api/v1/prompts/%s/versions/%d", urlEncode(name), version)
	if err := c.get(path, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePromptVersion is a best-effort destroy — the server may or may not
// support deleting a specific version. If unsupported, we silently succeed
// because prompt versioning is append-only by design.
func (c *Client) DeletePromptVersion(name string, version int) error {
	path := fmt.Sprintf("/api/v1/prompts/%s/versions/%d", urlEncode(name), version)
	err := c.delete(path, nil)
	if err == nil {
		return nil
	}
	// 404 or 405 = server doesn't support delete; treat as success for destroy.
	if ae, ok := err.(*apiError); ok {
		if ae.Code == "RESOURCE_DOES_NOT_EXIST" || ae.Code == "METHOD_NOT_ALLOWED" {
			return nil
		}
	}
	return err
}

// ── Prompt Alias helpers ─────────────────────────────────────────────────────

type promptAliasInfo struct {
	Alias   string `json:"alias"`
	Version int    `json:"version"`
	Name    string `json:"name"`
}

func (c *Client) SetPromptAlias(name, alias string, version int) error {
	return c.post(fmt.Sprintf("/api/v1/prompts/%s/aliases", urlEncode(name)), map[string]interface{}{
		"alias":   alias,
		"version": version,
	}, nil)
}

func (c *Client) GetPromptAlias(name, alias string) (*promptAliasInfo, error) {
	var info promptAliasInfo
	path := fmt.Sprintf("/api/v1/prompts/%s/aliases/%s", urlEncode(name), urlEncode(alias))
	if err := c.get(path, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) DeletePromptAlias(name, alias string) error {
	path := fmt.Sprintf("/api/v1/prompts/%s/aliases/%s", urlEncode(name), urlEncode(alias))
	return c.delete(path, nil)
}

// ── Workspace helpers ────────────────────────────────────────────────────────

type workspaceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type createWorkspaceRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (c *Client) CreateWorkspace(id, name, description string) (*workspaceInfo, error) {
	req := createWorkspaceRequest{ID: id, Name: name, Description: description}
	var w workspaceInfo
	if err := c.post("/api/v1/workspaces", req, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (c *Client) GetWorkspace(id string) (*workspaceInfo, error) {
	var w workspaceInfo
	if err := c.get("/api/v1/workspaces/"+urlEncode(id), &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (c *Client) UpdateWorkspace(id, name, description string) (*workspaceInfo, error) {
	body := map[string]string{"name": name, "description": description}
	var w workspaceInfo
	if err := c.patch("/api/v1/workspaces/"+urlEncode(id), body, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (c *Client) DeleteWorkspace(id string) error {
	return c.delete("/api/v1/workspaces/"+urlEncode(id), nil)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func urlEncode(s string) string {
	// Minimal percent-encoding for path segments (handles spaces and slashes in names).
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isNotFound returns true when err is a RESOURCE_DOES_NOT_EXIST API error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*apiError); ok {
		return ae.Code == "RESOURCE_DOES_NOT_EXIST"
	}
	return false
}
