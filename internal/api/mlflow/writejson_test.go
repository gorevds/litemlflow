package mlflow

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteJSONNonFiniteIs500 guards independent-review 2.5: an un-encodable
// (non-finite) value must produce a clean 500, never a silent empty 200.
func TestWriteJSONNonFiniteIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]any{"value": math.NaN()})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("writeJSON(NaN): want 500, got %d (body=%q)", rec.Code, rec.Body.String())
	}
}
