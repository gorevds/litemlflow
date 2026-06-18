package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"testing"
)

// TestResolveKeySkipsNonSigning guards independent-review: resolveKey must skip
// encryption keys (use!="sig") and non-RS256 keys, not blindly take the first
// RSA key. The decoy enc key has invalid N/E, so if it were not skipped,
// rsaPublicKeyFromJWK would error.
func TestResolveKeySkipsNonSigning(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())

	p := &Provider{jwks: &jwksCache{keys: []jwk{
		{Kty: "RSA", Use: "enc", Alg: "RSA-OAEP", Kid: "enc1", N: "!!!invalid", E: "!!!"},
		{Kty: "RSA", Use: "sig", Alg: "RS256", Kid: "sig1", N: n, E: e},
	}}}

	// kid-less lookup must land on the signing key, not the (first) enc key.
	pub, err := p.resolveKey("")
	if err != nil {
		t.Fatalf("resolveKey: want signing key, got error %v", err)
	}
	if pub.N.Cmp(priv.N) != 0 {
		t.Errorf("resolveKey returned the wrong key")
	}

	// A non-RS256 alg with a matching kty must not be selected.
	p2 := &Provider{jwks: &jwksCache{keys: []jwk{
		{Kty: "RSA", Alg: "PS256", Kid: "ps", N: n, E: e},
	}}}
	if _, err := p2.resolveKey(""); err == nil {
		t.Errorf("resolveKey should reject a non-RS256 key")
	}
}
