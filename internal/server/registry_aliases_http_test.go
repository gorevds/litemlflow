package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// Registered models and model versions must carry their aliases in every
// read response, matching MLflow's wire shape:
// RegisteredModel.aliases = [{alias, version}], ModelVersion.aliases = ["..."].
func TestRegistryResponsesIncludeAliases(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	call := func(method, path, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out
	}

	call("POST", "/api/2.0/mlflow/registered-models/create", `{"name":"m1"}`)
	call("POST", "/api/2.0/mlflow/registered-models/create", `{"name":"m2"}`)
	for i := 0; i < 2; i++ {
		call("POST", "/api/2.0/mlflow/model-versions/create", `{"name":"m1","source":"s3://x"}`)
	}
	call("POST", "/api/2.0/mlflow/model-versions/create", `{"name":"m2","source":"s3://y","tags":[{"key":"k","value":"v"}]}`)
	call("POST", "/api/2.0/mlflow/registered-models/alias", `{"name":"m1","alias":"champion","version":"2"}`)
	call("POST", "/api/2.0/mlflow/registered-models/alias", `{"name":"m1","alias":"challenger","version":"1"}`)
	call("POST", "/api/2.0/mlflow/registered-models/alias", `{"name":"m1","alias":"best","version":"2"}`)

	wantM1 := []any{
		map[string]any{"alias": "best", "version": "2"},
		map[string]any{"alias": "challenger", "version": "1"},
		map[string]any{"alias": "champion", "version": "2"},
	}

	got := call("GET", "/api/2.0/mlflow/registered-models/get?name=m1", "")
	rm := got["registered_model"].(map[string]any)
	if !reflect.DeepEqual(rm["aliases"], wantM1) {
		t.Errorf("get aliases = %v, want %v", rm["aliases"], wantM1)
	}

	got = call("GET", "/api/2.0/mlflow/registered-models/search", "")
	models := got["registered_models"].([]any)
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
	for _, m := range models {
		m := m.(map[string]any)
		switch m["name"] {
		case "m1":
			if !reflect.DeepEqual(m["aliases"], wantM1) {
				t.Errorf("search m1 aliases = %v", m["aliases"])
			}
		case "m2":
			if _, ok := m["aliases"]; ok {
				t.Errorf("m2 should have no aliases, got %v", m["aliases"])
			}
		}
	}

	got = call("GET", "/api/2.0/mlflow/model-versions/get?name=m1&version=2", "")
	mv := got["model_version"].(map[string]any)
	if !reflect.DeepEqual(mv["aliases"], []any{"best", "champion"}) {
		t.Errorf("version 2 aliases = %v", mv["aliases"])
	}

	got = call("GET", "/api/2.0/mlflow/registered-models/alias?name=m1&alias=challenger", "")
	mv = got["model_version"].(map[string]any)
	if !reflect.DeepEqual(mv["aliases"], []any{"challenger"}) {
		t.Errorf("by-alias aliases = %v", mv["aliases"])
	}

	got = call("GET", "/api/2.0/mlflow/model-versions/search", "")
	versions := got["model_versions"].([]any)
	if len(versions) != 3 {
		t.Fatalf("want 3 versions, got %d", len(versions))
	}
	want := map[string]any{
		"m1/1": []any{"challenger"},
		"m1/2": []any{"best", "champion"},
		"m2/1": nil,
	}
	for _, v := range versions {
		v := v.(map[string]any)
		key := v["name"].(string) + "/" + v["version"].(string)
		if !reflect.DeepEqual(v["aliases"], want[key]) {
			t.Errorf("%s aliases = %v, want %v", key, v["aliases"], want[key])
		}
		if key == "m2/1" {
			if !reflect.DeepEqual(v["tags"], []any{map[string]any{"key": "k", "value": "v"}}) {
				t.Errorf("m2/1 tags = %v", v["tags"])
			}
		}
	}

	// Deleting an alias removes it from responses.
	call("DELETE", "/api/2.0/mlflow/registered-models/alias?name=m1&alias=best", "")
	got = call("GET", "/api/2.0/mlflow/model-versions/get?name=m1&version=2", "")
	mv = got["model_version"].(map[string]any)
	if !reflect.DeepEqual(mv["aliases"], []any{"champion"}) {
		t.Errorf("after delete aliases = %v", mv["aliases"])
	}
}
