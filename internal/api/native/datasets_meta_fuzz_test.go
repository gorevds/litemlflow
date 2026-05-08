package native

import (
	"encoding/json"
	"testing"
)

// FuzzUploadMeta exercises the json.Unmarshal of the dataset upload meta
// field (datasets.go:CreateDatasetVersion). The field is user-controlled
// (multipart form-value), so a malformed payload must never panic the
// handler — it should either succeed parsing into uploadMeta or return a
// JSON error. This target was added per the Quality auditor's gap list
// (see docs/reports/2026-05-08-deep-review.md).
//
// Run as a regular unit test (uses seed corpus only):
//
//	go test -count=1 ./internal/api/native/ -run TestFuzzUploadMeta
//
// Run with fuzzing:
//
//	go test -fuzz=FuzzUploadMeta -fuzztime=20s ./internal/api/native/
func FuzzUploadMeta(f *testing.F) {
	// Seed corpus — known-good and obvious adversarial inputs.
	seeds := []string{
		"",
		"{}",
		`{"description":"hi","schema_json":"{\"cols\":[]}","parents":[1,2,3]}`,
		`{"description":"` + repeatStr("x", 1024) + `"}`,
		`{"parents":[]}`,
		`{"parents":null}`,
		`{"parents":[-1]}`,
		`{"parents":[9223372036854775807]}`,
		`{"description":null}`,
		`null`,
		`[]`, // wrong shape: array, not object
		`{"unknown_field":42,"description":"ok"}`,
		// Adversarial: deep nesting (json.Unmarshal capped at ~10000 levels).
		`{"a":` + repeatStr(`{"a":`, 100) + `null` + repeatStr("}", 100) + "}",
		// Adversarial: malformed JSON.
		`{"description":`,
		`{"description":"x"`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Empty strings short-circuit in the handler before Unmarshal; we
		// fuzz Unmarshal directly to mirror that path.
		var meta uploadMeta
		// Must not panic. Errors are fine — handler returns 400.
		_ = json.Unmarshal([]byte(s), &meta)
		// Sanity bound: parsed parents slice should never grow without
		// bound. Json's recursion limit prevents stack overflow on us.
		if len(meta.Parents) > 1_000_000 {
			t.Fatalf("absurd Parents length: %d", len(meta.Parents))
		}
	})
}

func repeatStr(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
