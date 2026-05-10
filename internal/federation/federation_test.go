package federation

import (
	"bytes"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewSecretIs32BytesHex(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		s, err := NewSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 64 {
			t.Errorf("expected 64 hex chars, got %d", len(s))
		}
		raw, err := hex.DecodeString(s)
		if err != nil || len(raw) != 32 {
			t.Errorf("not 32 raw bytes: %v %d", err, len(raw))
		}
	}
}

func TestSignAndVerifyRoundtrip(t *testing.T) {
	t.Parallel()
	secret, _ := NewSecret()
	body := []byte(`{"q":"foo","kind":"all"}`)
	ts := time.Now().UnixMilli()
	sig := func() string {
		raw, _ := hex.DecodeString(secret)
		return signRequest(raw, "POST", "/api/v1/federate/search", ts, body)
	}()
	if err := VerifyRequest(secret, "POST", "/api/v1/federate/search", ts, body, sig); err != nil {
		t.Errorf("verify happy path: %v", err)
	}

	// Wrong body → mismatch.
	if err := VerifyRequest(secret, "POST", "/api/v1/federate/search", ts, []byte(`{"q":"bar"}`), sig); err != ErrSignatureMismatch {
		t.Errorf("expected sig mismatch, got %v", err)
	}
	// Wrong path → mismatch.
	if err := VerifyRequest(secret, "POST", "/api/v1/federate/peers", ts, body, sig); err != ErrSignatureMismatch {
		t.Errorf("path tamper: expected mismatch, got %v", err)
	}
	// Wrong method → mismatch.
	if err := VerifyRequest(secret, "GET", "/api/v1/federate/search", ts, body, sig); err != ErrSignatureMismatch {
		t.Errorf("method tamper: expected mismatch, got %v", err)
	}
	// Wrong secret → mismatch.
	other, _ := NewSecret()
	if err := VerifyRequest(other, "POST", "/api/v1/federate/search", ts, body, sig); err != ErrSignatureMismatch {
		t.Errorf("wrong secret: expected mismatch, got %v", err)
	}
}

func TestVerifyRejectsBadSecretFormat(t *testing.T) {
	t.Parallel()
	cases := []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("a", 63)}
	for _, s := range cases {
		err := VerifyRequest(s, "POST", "/p", time.Now().UnixMilli(), nil, "sha256=...")
		if err != ErrSecretFormat {
			t.Errorf("secret=%q: expected ErrSecretFormat, got %v", s, err)
		}
	}
}

func TestVerifyRejectsStaleTimestamp(t *testing.T) {
	t.Parallel()
	secret, _ := NewSecret()
	stale := time.Now().Add(-1 * time.Hour).UnixMilli() // 1h drift
	raw, _ := hex.DecodeString(secret)
	sig := signRequest(raw, "POST", "/p", stale, nil)
	err := VerifyRequest(secret, "POST", "/p", stale, nil, sig)
	if err != ErrTimestampStale {
		t.Errorf("expected ErrTimestampStale, got %v", err)
	}
}

func TestClientDoSendsHeaders(t *testing.T) {
	t.Parallel()
	secret, _ := NewSecret()
	var (
		seenSig  string
		seenPeer string
		seenTs   string
		seenBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSig = r.Header.Get(HeaderSignature)
		seenPeer = r.Header.Get(HeaderPeer)
		seenTs = r.Header.Get(HeaderTimestamp)
		buf, _ := io.ReadAll(r.Body)
		seenBody = buf
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "lmf-A", secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"q":"hi"}`)
	resp, respBody, err := c.Do("POST", "/api/v1/federate/search", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	if !bytes.Equal(respBody, []byte(`{"ok":true}`)) {
		t.Errorf("respBody: %q", respBody)
	}
	if seenPeer != "lmf-A" {
		t.Errorf("peer header: %q", seenPeer)
	}
	if !strings.HasPrefix(seenSig, "sha256=") {
		t.Errorf("sig header: %q", seenSig)
	}
	ts, _ := strconv.ParseInt(seenTs, 10, 64)
	now := time.Now().UnixMilli()
	if abs64(now-ts) > 5_000 {
		t.Errorf("ts skew %dms vs now", now-ts)
	}
	if !bytes.Equal(seenBody, body) {
		t.Errorf("body roundtrip mismatch")
	}

	// Verify the receiver could validate the signature — proving the
	// canonical-input shape on both sides agrees.
	if err := VerifyRequest(secret, "POST", "/api/v1/federate/search", ts, body, seenSig); err != nil {
		t.Errorf("client-server round-trip verify: %v", err)
	}
}

func TestClientRejectsBadSecret(t *testing.T) {
	t.Parallel()
	if _, err := NewClient("http://x", "p", "shortsecret", 0); err != ErrSecretFormat {
		t.Errorf("expected ErrSecretFormat, got %v", err)
	}
}

func TestCachePutGetTTL(t *testing.T) {
	t.Parallel()
	c := NewCache(2, 50*time.Millisecond)
	c.Put("k1", []byte("v1"))
	if got, ok := c.Get("k1"); !ok || string(got) != "v1" {
		t.Errorf("get k1: %q ok=%v", got, ok)
	}
	// Different mutations don't blast existing keys.
	c.Put("k1", []byte("v1b"))
	if got, _ := c.Get("k1"); string(got) != "v1b" {
		t.Errorf("update: %q", got)
	}
	// TTL expiry.
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("k1"); ok {
		t.Errorf("expected expired")
	}
}

func TestCacheFIFOEviction(t *testing.T) {
	t.Parallel()
	c := NewCache(2, time.Hour)
	c.Put("a", []byte("1"))
	c.Put("b", []byte("2"))
	c.Put("c", []byte("3")) // evicts a
	if _, ok := c.Get("a"); ok {
		t.Errorf("expected a evicted")
	}
	if got, _ := c.Get("b"); string(got) != "2" {
		t.Errorf("b: %q", got)
	}
	if got, _ := c.Get("c"); string(got) != "3" {
		t.Errorf("c: %q", got)
	}
}

func TestCacheConcurrent(t *testing.T) {
	t.Parallel()
	c := NewCache(64, time.Hour)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				k := strconv.Itoa(w*1000 + i)
				c.Put(k, []byte(k))
				_, _ = c.Get(k)
			}
		}(w)
	}
	wg.Wait()
}

// TestCacheEvictionAfterTTLRePut guards independent-review finding H1:
// before the fix, Get-then-Put of an expired key left a duplicate in the
// FIFO slice, so a later Put could evict the FRESH entry.
func TestCacheEvictionAfterTTLRePut(t *testing.T) {
	t.Parallel()
	c := NewCache(2, 30*time.Millisecond)
	c.Put("a", []byte("v1"))
	time.Sleep(60 * time.Millisecond) // expire a
	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected a expired before re-put")
	}
	// Re-Put under the same key (this is the case that was bugged).
	c.Put("a", []byte("v2"))
	c.Put("b", []byte("vb")) // this Put used to evict a's fresh slot
	if got, ok := c.Get("a"); !ok || string(got) != "v2" {
		t.Errorf("after re-Put + Put(b): expected a=v2, got %q ok=%v", got, ok)
	}
	if got, ok := c.Get("b"); !ok || string(got) != "vb" {
		t.Errorf("b should still be present, got %q ok=%v", got, ok)
	}
}

// TestClientRejectsHugeResponse guards independent-review finding C2:
// peer responses above MaxResponseBytes must be rejected, not slurped
// into memory.
func TestClientRejectsHugeResponse(t *testing.T) {
	t.Parallel()
	secret, _ := NewSecret()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stream just over the limit using chunked encoding.
		w.WriteHeader(http.StatusOK)
		buf := bytes.Repeat([]byte{'x'}, 1<<20) // 1 MiB
		for written := 0; written <= MaxResponseBytes; written += len(buf) {
			_, _ = w.Write(buf)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "lmf-A", secret, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Do("POST", "/oversize", []byte(`{}`))
	if err != ErrResponseTooLarge {
		t.Errorf("expected ErrResponseTooLarge, got %v", err)
	}
}

func TestQueryHashStable(t *testing.T) {
	t.Parallel()
	a := QueryHash(map[string]any{"x": 1, "y": 2})
	b := QueryHash(map[string]any{"y": 2, "x": 1})
	if a != b {
		t.Errorf("expected stable hash across map orderings: %s vs %s", a, b)
	}
	c := QueryHash(map[string]any{"x": 2, "y": 2})
	if a == c {
		t.Errorf("hash collided on different inputs")
	}
}
