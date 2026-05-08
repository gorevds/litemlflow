package store

// Fuzz tests for the filter/predicate parsers.
//
// Run as regular unit tests (seed corpus only):
//
//	go test -count=1 ./internal/store/
//
// Run with fuzzing for a fixed duration:
//
//	go test -fuzz=FuzzParseRunPredicate     -fuzztime=60s ./internal/store/
//	go test -fuzz=FuzzParseRunFilter        -fuzztime=60s ./internal/store/
//	go test -fuzz=FuzzParseExperimentFilter -fuzztime=60s ./internal/store/
//
// See docs/contributing-fuzz.md for extended guidance.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// noUnescapedQuote verifies that the returned SQL clause does not contain a
// bare single-quote character.
//
// The parser functions produce parameterised SQL: all user-supplied values are
// passed as '?' bind parameters, never interpolated into the clause string
// directly. A single-quote in the generated clause would indicate user input
// leaked into the SQL structure, which is a potential injection vector.
func noUnescapedQuote(t testing.TB, clause string) {
	t.Helper()
	if strings.ContainsRune(clause, '\'') {
		t.Errorf("generated SQL clause contains bare single-quote: %q", clause)
	}
}

// FuzzParseRunPredicate exercises the predicate parser with arbitrary input.
// Oracle: must not panic; on success the SQL must not contain bare quotes.
func FuzzParseRunPredicate(f *testing.F) {
	// Seed corpus -- valid inputs.
	seeds := []string{
		"status = 'FINISHED'",
		"status != 'RUNNING'",
		"params.lr > 0.01",
		"params.lr = '0.001'",
		"metrics.acc BETWEEN 0 AND 1",
		"metrics.loss < 0.5",
		"tags.stage = 'prod'",
		"attributes.run_name = 'my-run'",
		"attributes.run_id IN ('abc','def')",
		// Malformed / adversarial inputs.
		"' OR 1=1 --",
		"status = ''; DROP TABLE runs; --",
		"metrics.x BETWEEN ' AND '",
		"",
		strings.Repeat("A", 4096),
		"params.key = 'val'",
		"metrics.loss > 0",
		"attributes.run_id IN ('" + strings.Repeat("x", 1000) + "')",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Skip inputs that are not valid UTF-8; the parser operates on Go
		// strings which are byte sequences, but malformed UTF-8 is not a
		// realistic source of filter expressions and only exercises encoding
		// paths, not parsing logic.
		if !utf8.ValidString(input) {
			t.Skip("non-UTF-8 input")
		}
		// Must not panic (deferred recover turns panics into fatal failures).
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseRunPredicate panicked on %q: %v", input, r)
			}
		}()
		clause, _, err := parseRunPredicate(input)
		if err == nil {
			// On success the generated SQL must be clean.
			noUnescapedQuote(t, clause)
		}
	})
}

// FuzzParseRunFilter exercises the compound-filter parser (AND-joined clauses).
func FuzzParseRunFilter(f *testing.F) {
	seeds := []string{
		"status = 'FINISHED'",
		"params.lr > 0.01 AND metrics.loss < 0.5",
		"metrics.acc BETWEEN 0 AND 1 AND tags.stage = 'prod'",
		"attributes.run_id IN ('a','b') AND params.model = 'gpt2'",
		// Malformed.
		"' OR 1=1",
		"AND AND AND",
		"params.x = 'y' AND",
		"metrics.z BETWEEN AND",
		"attributes.run_id IN ()",
		strings.Repeat("status = 'X' AND ", 100) + "status = 'Y'",
		// Unicode (valid UTF-8).
		"params.eleve = '42'",
		"",
		// Injection attempts.
		"status = 'x' UNION SELECT * FROM runs --",
		"params.a = '; DELETE FROM runs; --'",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) {
			t.Skip("non-UTF-8 input")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseRunFilter panicked on %q: %v", input, r)
			}
		}()
		clause, _, err := parseRunFilter(input)
		if err == nil {
			noUnescapedQuote(t, clause)
		}
	})
}

// FuzzParseExperimentFilter exercises the experiment-level filter parser.
func FuzzParseExperimentFilter(f *testing.F) {
	seeds := []string{
		"name = 'my-experiment'",
		"name LIKE 'my%'",
		"name LIKE '%val%'",
		// Adversarial.
		"name = ''",
		"name = '; DROP TABLE experiments; --'",
		"name LIKE \"foobar\"",
		"name = 'backslash'",
		"not-a-filter",
		// Unsupported operator -- must return error, not panic.
		"name != 'x'",
		"",
		strings.Repeat("name = 'x", 1) + strings.Repeat("x", 500) + "'",
		// Unicode (valid UTF-8).
		"name = 'chinese-chars'",
		"name LIKE '%accent%'",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) {
			t.Skip("non-UTF-8 input")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseExperimentFilter panicked on %q: %v", input, r)
			}
		}()
		clause, _, err := parseExperimentFilter(input)
		if err == nil {
			noUnescapedQuote(t, clause)
		}
	})
}

// FuzzSplitOnAnd exercises the internal AND-splitter with arbitrary inputs.
// Oracle: must not panic and must always return at least one element.
func FuzzSplitOnAnd(f *testing.F) {
	seeds := []string{
		"a AND b AND c",
		"metrics.x BETWEEN 0 AND 1",
		"a AND metrics.x BETWEEN 0 AND 1 AND b",
		"",
		"no-and-at-all",
		"' AND '",
		strings.Repeat("x AND ", 200) + "y",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("splitOnAnd panicked on %q: %v", input, r)
			}
		}()
		parts := splitOnAnd(input)
		if len(parts) == 0 {
			t.Fatalf("splitOnAnd returned empty slice for input %q", input)
		}
	})
}
