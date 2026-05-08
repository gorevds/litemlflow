package native_test

// Fuzz test for the IngestOTLP HTTP handler.
//
// IngestOTLP accepts OTLP/JSON. The fuzz test feeds arbitrary JSON bodies and
// verifies that the handler never panics. Errors (4xx, 5xx) are acceptable.
//
// Run as regular unit test (seed corpus only):
//
//	go test -count=1 ./internal/api/native/
//
// Run with fuzzing:
//
//	go test -fuzz=FuzzIngestOTLP -fuzztime=60s ./internal/api/native/
//
// See docs/contributing-fuzz.md for extended guidance.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/gorevds/litemlflow/internal/api/native"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
	"github.com/gorevds/litemlflow/internal/store/storetest"
)

// --- minimal in-memory store stub ---

// stubStore wraps storetest.NopStore + records spans for the fuzz target.
// All other Store methods are inherited from NopStore (no-op / ErrNotFound).
type stubStore struct {
	storetest.NopStore
	spans []model.Span
}

// Override CreateExperiment so the embedded NopStore's panic doesn't fire
// during fuzz setup (stubStore is created with `&stubStore{}` and the OTLP
// path doesn't legitimately call it).
func (s *stubStore) CreateExperiment(_ context.Context, _ *model.Experiment) (int64, error) {
	return 0, store.ErrNotFound
}

func (s *stubStore) InsertSpans(_ context.Context, spans []model.Span) error {
	s.spans = append(s.spans, spans...)
	return nil
}

// --- fuzz setup ---

func newTestHandler() *native.Handler {
	return &native.Handler{Store: &stubStore{}}
}

func newTestRouter(h *native.Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

// validOTLPBody returns a well-formed minimal OTLP/JSON request body.
func validOTLPBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{
							"key":   "service.name",
							"value": map[string]any{"stringValue": "my-svc"},
						},
					},
				},
				"scopeSpans": []map[string]any{
					{
						"spans": []map[string]any{
							{
								"traceId":            "abc123",
								"spanId":             "span001",
								"name":               "test-span",
								"kind":               2,
								"startTimeUnixNano":  "1000000000",
								"endTimeUnixNano":    "2000000000",
								"attributes":         []map[string]any{},
								"status":             map[string]any{"code": 1, "message": "OK"},
							},
						},
					},
				},
			},
		},
	})
	return body
}

// FuzzIngestOTLP exercises the IngestOTLP handler with arbitrary JSON bodies.
//
// Oracles:
//  1. The handler must never panic.
//  2. The response status must be a valid HTTP status code (200–599).
//  3. The response body must be valid JSON (the handler always writes JSON).
func FuzzIngestOTLP(f *testing.F) {
	// Seed corpus: valid, partially valid, and adversarial inputs.
	f.Add(validOTLPBody())
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"resourceSpans":[]}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{}]}]}]}`))

	// Oversized array.
	bigSpans := make([]map[string]any, 2000)
	for i := range bigSpans {
		bigSpans[i] = map[string]any{
			"traceId":           fmt.Sprintf("trace%d", i),
			"spanId":            fmt.Sprintf("span%d", i),
			"name":              "s",
			"startTimeUnixNano": "1000",
			"endTimeUnixNano":   "2000",
		}
	}
	bigBody, _ := json.Marshal(map[string]any{
		"resourceSpans": []map[string]any{
			{"scopeSpans": []map[string]any{{"spans": bigSpans}}},
		},
	})
	f.Add(bigBody)

	// Deeply nested structure.
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"k","value":{"stringValue":` +
		strings.Repeat(`{"nested":`, 30) + `"deep"` + strings.Repeat(`}`, 30) + `}}]},"scopeSpans":[]}]}`))

	// Wrong field types.
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"startTimeUnixNano":true,"endTimeUnixNano":null}]}]}]}`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"kind":"not-a-number"}]}]}]}`))

	// SQL injection attempt in span attributes.
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"litemlflow.run_id","value":{"stringValue":"' OR 1=1 --"}}]},"scopeSpans":[{"spans":[{"traceId":"t1","spanId":"s1","name":"n","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`))

	// Zero-length body.
	f.Add([]byte(``))

	// Timestamps at extreme values.
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"t","spanId":"s","name":"n",` +
		`"startTimeUnixNano":"9999999999999999999","endTimeUnixNano":"-9999999999999999999"}]}]}]}`))

	h := newTestHandler()
	router := newTestRouter(h)

	f.Fuzz(func(t *testing.T, body []byte) {
		// Only fuzz valid UTF-8 JSON-like inputs; binary bodies are not
		// something OTLP/JSON clients send.
		if !utf8.Valid(body) {
			t.Skip("non-UTF-8 body")
		}

		// Oracle 1: handler must not panic.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("IngestOTLP panicked on body (len=%d): %v", len(body), r)
			}
		}()

		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		// Oracle 2: HTTP status must be in a valid range.
		status := w.Code
		if status < 200 || status > 599 {
			t.Errorf("unexpected HTTP status %d", status)
		}

		// Oracle 3: response body must be valid JSON (or empty for 204/etc).
		respBody := w.Body.Bytes()
		if len(respBody) > 0 {
			if !json.Valid(respBody) {
				t.Errorf("response body is not valid JSON (status=%d): %q", status, respBody)
			}
		}
	})
}

// FuzzIngestTraces exercises the IngestTraces handler (POST /api/v1/traces).
// This endpoint uses a different JSON schema from OTLP.
func FuzzIngestTraces(f *testing.F) {
	validTrace, _ := json.Marshal(map[string]any{
		"trace_id": "abc123",
		"spans": []map[string]any{
			{
				"id":            "span001",
				"trace_id":      "abc123",
				"name":          "root",
				"start_time_ns": 1000000000,
				"end_time_ns":   2000000000,
			},
		},
	})
	f.Add(validTrace)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"spans":[]}`))
	f.Add([]byte(`{"spans":null}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"trace_id":"t","spans":[{"name":"s","start_time_ns":"not-a-number"}]}`))
	f.Add([]byte(`{"spans":[{"start_time_ns":-9999999999999999999,"name":"x"}]}`))

	// Injection attempt.
	f.Add([]byte(`{"trace_id":"' OR 1=1","spans":[{"name":"x","start_time_ns":1}]}`))

	h := newTestHandler()
	router := newTestRouter(h)

	f.Fuzz(func(t *testing.T, body []byte) {
		if !utf8.Valid(body) {
			t.Skip("non-UTF-8 body")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("IngestTraces panicked on body (len=%d): %v", len(body), r)
			}
		}()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code < 200 || w.Code > 599 {
			t.Errorf("unexpected HTTP status %d", w.Code)
		}
		if rb := w.Body.Bytes(); len(rb) > 0 && !json.Valid(rb) {
			t.Errorf("response body is not valid JSON (status=%d): %q", w.Code, rb)
		}
	})
}
