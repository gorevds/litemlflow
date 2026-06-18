package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/gorevds/litemlflow/internal/auth"
)

func legacySHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestHashPasswordRoundTripsBcrypt(t *testing.T) {
	h, err := auth.HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if len(h) < 4 || h[:4] != "$2a$" && h[:4] != "$2b$" && h[:4] != "$2y$" {
		t.Fatalf("expected a bcrypt hash, got %q", h)
	}
	if !auth.VerifyBasicCredentials("admin", h, "admin", "hunter2") {
		t.Error("correct bcrypt credentials rejected")
	}
	if auth.VerifyBasicCredentials("admin", h, "admin", "wrong") {
		t.Error("wrong password accepted (bcrypt)")
	}
}

func TestVerifyBasicCredentials(t *testing.T) {
	bcryptHash, err := auth.HashPassword("s3cret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	legacyHash := legacySHA256Hex("s3cret")

	cases := []struct {
		name               string
		wantUser, wantHash string
		gotUser, gotPass   string
		want               bool
	}{
		{"bcrypt ok", "admin", bcryptHash, "admin", "s3cret", true},
		{"bcrypt bad pass", "admin", bcryptHash, "admin", "nope", false},
		{"bcrypt bad user", "admin", bcryptHash, "root", "s3cret", false},
		{"legacy sha256 ok", "admin", legacyHash, "admin", "s3cret", true},
		{"legacy sha256 bad pass", "admin", legacyHash, "admin", "nope", false},
		{"legacy sha256 bad user", "admin", legacyHash, "root", "s3cret", false},
		{"malformed hash", "admin", "not-a-hash", "admin", "s3cret", false},
		{"empty hash", "admin", "", "admin", "s3cret", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := auth.VerifyBasicCredentials(c.wantUser, c.wantHash, c.gotUser, c.gotPass); got != c.want {
				t.Errorf("VerifyBasicCredentials = %v, want %v", got, c.want)
			}
		})
	}
}
