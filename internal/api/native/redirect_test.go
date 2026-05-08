package native

import (
	"testing"
)

// Test the open-redirect defense (gosec/semgrep finding).
// Anything that isn't a single-leading-slash absolute path must fall back
// to the safe default ("/ui/").
func TestSafeReturnTo(t *testing.T) {
	const fallback = "/ui/"
	cases := []struct {
		in   string
		want string
	}{
		// safe absolute paths
		{"/ui/", "/ui/"},
		{"/ui/experiments/3", "/ui/experiments/3"},
		{"/api/v1/something", "/api/v1/something"},

		// fallback cases
		{"", fallback},
		{"http://evil.com/path", fallback},
		{"https://evil.com", fallback},
		{"//evil.com/path", fallback}, // protocol-relative
		{"//evil", fallback},
		{"/\\evil.com", fallback}, // backslash-prefixed (Windows path traversal-ish)
		{"javascript:alert(1)", fallback},
		{"relative/path", fallback},
		{"\r\nLocation: http://evil.com\r\n", fallback}, // CRLF injection
		{"/\rinjection", fallback},
		{"/\ninjection", fallback},
		{"/\x00null", fallback},
	}
	for _, tc := range cases {
		if got := safeReturnTo(tc.in); got != tc.want {
			t.Errorf("safeReturnTo(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}

	// Length cap (>2048 chars).
	long := "/" + string(make([]byte, 2048))
	if got := safeReturnTo(long); got != fallback {
		t.Errorf("safeReturnTo(long): expected fallback, got %q", got[:min(len(got), 50)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
