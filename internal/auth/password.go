package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of plaintext, suitable for the
// --basic-pass-hash flag / LITEMLFLOW_BASIC_PASS_HASH. bcrypt is salted and
// deliberately slow, so a leaked hash is not trivially brute-forceable.
func HashPassword(plaintext string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyBasicCredentials reports whether (gotUser, gotPass) match the configured
// (wantUser, wantHash). wantHash may be either:
//
//   - a bcrypt hash ($2a$/$2b$/$2y$…) — preferred, salted and slow; or
//   - a legacy hex-encoded SHA-256 digest — DEPRECATED: unsalted and fast, so a
//     leaked hash is cheap to brute-force. Supported only for backward compat.
//
// The username and the SHA-256 path are compared in constant time; bcrypt is
// constant-time internally. The username check short-circuits, which can leak
// whether the username matched via timing — the username is not a secret.
func VerifyBasicCredentials(wantUser, wantHash, gotUser, gotPass string) bool {
	if subtle.ConstantTimeCompare([]byte(gotUser), []byte(wantUser)) != 1 {
		return false
	}
	return verifyPasswordHash(wantHash, gotPass)
}

func verifyPasswordHash(wantHash, gotPass string) bool {
	if isBcryptHash(wantHash) {
		return bcrypt.CompareHashAndPassword([]byte(wantHash), []byte(gotPass)) == nil
	}
	got := sha256.Sum256([]byte(gotPass))
	want, err := hex.DecodeString(wantHash)
	if err != nil || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// isBcryptHash reports whether s carries a bcrypt identifier prefix.
func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}
