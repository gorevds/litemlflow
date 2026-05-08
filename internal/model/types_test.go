package model_test

import (
	"strings"
	"testing"

	"github.com/litemlflow/litemlflow/internal/model"
)

func TestValidName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		ok     bool
		maxLen int
	}{
		{"alpha", true, 0},
		{"", false, 0},
		{"two words", true, 0},
		{strings.Repeat("a", 251), false, 250},
		{strings.Repeat("a", 250), true, 250},
		{"with\x00null", false, 0},
		{"with\nnewline", false, 0},
	}
	for _, c := range cases {
		err := model.ValidName(c.in, c.maxLen)
		if (err == nil) != c.ok {
			t.Fatalf("name %q (max %d) expected ok=%v, got err=%v", c.in, c.maxLen, c.ok, err)
		}
	}
}

func TestValidKey(t *testing.T) {
	t.Parallel()
	if err := model.ValidKey("loss"); err != nil {
		t.Fatalf("loss should be a valid key: %v", err)
	}
	if err := model.ValidKey(" loss"); err == nil {
		t.Fatal("leading whitespace should be rejected")
	}
	if err := model.ValidKey(""); err == nil {
		t.Fatal("empty key should be rejected")
	}
}

func TestRunIDIsHex32(t *testing.T) {
	t.Parallel()
	id := model.NewRunID()
	if len(id) != 32 {
		t.Fatalf("want 32 hex chars, got %d (%s)", len(id), id)
	}
	for _, r := range id {
		if !isHex(r) {
			t.Fatalf("non-hex character %q in id %s", r, id)
		}
	}
	// Two consecutive IDs must differ (collision detection).
	if id == model.NewRunID() {
		t.Fatal("consecutive IDs collided")
	}
}

func TestSpanAndTraceIDs(t *testing.T) {
	t.Parallel()
	if got := model.NewSpanID(); len(got) != 16 {
		t.Fatalf("span id length = %d", len(got))
	}
	if got := model.NewTraceID(); len(got) != 32 {
		t.Fatalf("trace id length = %d", len(got))
	}
}

func isHex(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'f':
		return true
	case r >= 'A' && r <= 'F':
		return true
	}
	return false
}
