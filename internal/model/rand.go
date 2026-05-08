package model

import "crypto/rand"

// readRand reads len(b) bytes of cryptographic randomness into b.
// Wrapped in a helper to ease testing and to keep the import surface in one place.
func readRand(b []byte) error {
	_, err := rand.Read(b)
	return err
}
