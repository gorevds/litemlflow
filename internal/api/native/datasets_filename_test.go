package native

import "testing"

// Regression for v1.2 review MAJOR #4: sanitizeFilename must strip
// bidirectional-override codepoints (U+202E) and other Unicode/ASCII
// confusables that could break Content-Disposition parsing or fool the
// user into mis-recognising the file.
func TestSanitizeFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"my dataset", "my dataset"},
		{"with.dots-and_underscores", "with.dots-and_underscores"},

		// Header-injection attempts.
		{`evil"; filename=x`, `evil__ filename=x`},
		{"foo\r\nLocation: x", "foo__Location: x"},
		{"foo\x00null", "foo_null"},

		// Path-traversal attempts.
		{"../etc/passwd", ".._etc_passwd"},
		{`..\windows`, ".._windows"[:0] + ".._windows"},
		{`a;rm -rf /`, "a_rm -rf _"},

		// Bidi override (U+202E) and other non-printable Unicode → '_'.
		{"file‮gnp.exe", "file_gnp.exe"},
		{"file​title", "file_title"},

		// Non-ASCII letters fall back to '_' (RFC 5987 filename* not
		// implemented yet).
		{"русский", "_______"},

		// Empty stays empty (default to "dataset").
		{"", "dataset"},
	}
	for _, tc := range cases {
		got := sanitizeFilename(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
