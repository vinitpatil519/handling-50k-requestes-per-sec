package worker

import (
	"testing"
	"time"

	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/events"
)

func TestAggregator(t *testing.T) {
	a := NewAggregator()
	t0 := time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC)
	a.Add(events.Event{Type: "click", Time: t0})
	a.Add(events.Event{Type: "click", Time: t0.Add(20 * time.Second)})
	a.Add(events.Event{Type: "click", Time: t0.Add(70 * time.Second)})
	a.Add(events.Event{Type: "view", Time: t0})

	if a.Len() != 4 {
		t.Fatalf("Len = %d", a.Len())
	}
	got := map[string]int64{}
	for _, r := range a.Rollups() {
		got[r.Type+"@"+r.Bucket.Format("15:04")] = r.Count
	}
	want := map[string]int64{"click@12:00": 2, "click@12:01": 1, "view@12:00": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("rollup %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	c := a.Counters()
	if c[cache.KeyEventsTotal] != 4 || c[cache.KeyEventsPrefix+"click"] != 3 {
		t.Fatalf("counters = %v", c)
	}
	a.Reset()
	if a.Len() != 0 || len(a.Rollups()) != 0 {
		t.Fatal("reset did not clear state")
	}
}
