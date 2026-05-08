package webhooks

import (
	"strings"
	"sync"
	"testing"
)

func TestIsEchoURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"lmf://echo", true},
		{"lmf://echo/", true},
		{"lmf://echo/anything", true},
		{"lmf://echo/sub/path?q=1", true},
		{"lmf://echofoo", false},        // closing the M1 review-finding loop
		{"lmf://echo@evil.com:80/", false},
		{"https://example.com/lmf://echo", false},
		{"http://lmf://echo", false},
		{"", false},
		{"lmf://other", false},
		{"lmf:/echo", false}, // single slash
	}
	for _, tc := range cases {
		if got := IsEchoURL(tc.in); got != tc.want {
			t.Errorf("IsEchoURL(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestEchoLogRecordList(t *testing.T) {
	t.Parallel()
	log := NewEchoLog(3) // small ring to test eviction
	for i := 0; i < 5; i++ {
		log.Record(EchoEntry{
			Event:     "run_finished",
			WebhookID: int64(i),
			Body:      `{"i":` + string(rune('0'+i)) + `}`,
			RunID:     "r" + string(rune('0'+i)),
		})
	}
	got := log.List(10)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries (cap), got %d", len(got))
	}
	// Newest first.
	if got[0].WebhookID != 4 || got[1].WebhookID != 3 || got[2].WebhookID != 2 {
		t.Errorf("ordering wrong: %+v", got)
	}
	if got[0].Timestamp == 0 {
		t.Error("expected timestamp auto-set")
	}
}

func TestEchoLogDefaultCapacity(t *testing.T) {
	t.Parallel()
	for _, c := range []int{-1, 0} {
		log := NewEchoLog(c)
		// Capacity should default; insert >100 entries and check we don't
		// keep them all.
		for i := 0; i < 250; i++ {
			log.Record(EchoEntry{Event: "x", WebhookID: int64(i)})
		}
		if got := log.List(0); len(got) != 100 {
			t.Errorf("capacity=%d: expected default cap 100, got %d", c, len(got))
		}
	}
}

func TestEchoLogBodyTruncation(t *testing.T) {
	t.Parallel()
	log := NewEchoLog(5)
	big := strings.Repeat("a", 10_000)
	log.Record(EchoEntry{Event: "x", Body: big})
	got := log.List(1)
	if len(got[0].Body) > 4_500 {
		t.Errorf("body should be truncated, got %d bytes", len(got[0].Body))
	}
	if !strings.HasSuffix(got[0].Body, "(truncated)") {
		t.Errorf("missing truncation suffix, got tail %q", got[0].Body[len(got[0].Body)-30:])
	}
}

func TestEchoLogListMax(t *testing.T) {
	t.Parallel()
	log := NewEchoLog(20)
	for i := 0; i < 10; i++ {
		log.Record(EchoEntry{Event: "x", WebhookID: int64(i)})
	}
	if n := len(log.List(3)); n != 3 {
		t.Errorf("max=3 expected 3, got %d", n)
	}
	if n := len(log.List(0)); n != 10 {
		t.Errorf("max=0 (=all) expected 10, got %d", n)
	}
	if n := len(log.List(100)); n != 10 {
		t.Errorf("max>len expected 10, got %d", n)
	}
}

func TestEchoLogConcurrent(t *testing.T) {
	t.Parallel()
	log := NewEchoLog(1000)
	var wg sync.WaitGroup
	const writers = 8
	const perWriter = 200
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				log.Record(EchoEntry{Event: "x", WebhookID: int64(w*perWriter + i)})
			}
		}(w)
	}
	// Reader contending for the same lock.
	for i := 0; i < 50; i++ {
		go func() { _ = log.List(50) }()
	}
	wg.Wait()
	got := len(log.List(0))
	// 8 writers × 200 = 1600 records; ring cap is 1000. Whichever 1000
	// survive is fine — the point is no panic / data race.
	if got != 1000 {
		t.Errorf("expected 1000 (ring cap), got %d", got)
	}
}
