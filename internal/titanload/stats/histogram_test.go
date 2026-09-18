package stats

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestBucketCount(t *testing.T) {
	if got := bucketFor(maxMicros) + 1; got != NumBuckets {
		t.Fatalf("NumBuckets = %d, want %d", NumBuckets, got)
	}
}

func TestPercentilesWithinTwoPercent(t *testing.T) {
	s := NewShard()
	for i := 1; i <= 10000; i++ { // 1ms .. 10s uniform
		s.Record(time.Duration(i)*time.Millisecond, 200, 10, "")
	}
	sn := NewSnapshot()
	sn.AddShard(s)
	for _, tc := range []struct {
		q    float64
		want time.Duration
	}{{0.5, 5000 * time.Millisecond}, {0.95, 9500 * time.Millisecond}, {0.99, 9900 * time.Millisecond}} {
		got := sn.Percentile(tc.q)
		if rel := math.Abs(float64(got-tc.want)) / float64(tc.want); rel > 0.021 {
			t.Errorf("p%.0f = %v, want %v (±2%%), rel err %.4f", tc.q*100, got, tc.want, rel)
		}
	}
	if sn.Max() != 10*time.Second || sn.Min() != time.Millisecond {
		t.Fatalf("min/max = %v/%v", sn.Min(), sn.Max())
	}
	if sn.Classes[Class2xx] != 10000 || sn.Bytes != 100000 {
		t.Fatalf("classes = %v bytes = %d", sn.Classes, sn.Bytes)
	}
}

func TestFailuresAndErrorKinds(t *testing.T) {
	s := NewShard()
	s.Record(time.Millisecond, 200, 0, "")
	s.Record(time.Millisecond, 503, 0, "")
	s.Record(time.Millisecond, 0, 0, "timeout")
	s.Record(time.Millisecond, 0, 0, "timeout")
	sn := NewSnapshot()
	sn.AddShard(s)
	if sn.Failures != 3 || sn.ErrorRate() != 0.75 {
		t.Fatalf("failures = %d rate = %v", sn.Failures, sn.ErrorRate())
	}
	if kinds := sn.ErrKindsSorted(); len(kinds) != 1 || kinds[0] != (KV{"timeout", 2}) {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestMergeSubAndJSON(t *testing.T) {
	a, b := NewShard(), NewShard()
	a.Record(time.Millisecond, 200, 1, "")
	b.Record(3*time.Millisecond, 200, 1, "")
	sa, sb := NewSnapshot(), NewSnapshot()
	sa.AddShard(a)
	sb.AddShard(b)

	// Round-trip through JSON as the distributed protocol does.
	raw, err := json.Marshal(sb)
	if err != nil {
		t.Fatal(err)
	}
	var remote Snapshot
	if err := json.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	total := NewSnapshot()
	total.Merge(sa)
	total.Merge(&remote)
	if total.Requests != 2 || total.Min() != time.Millisecond || total.Max() != 3*time.Millisecond {
		t.Fatalf("merged = %+v", total)
	}

	delta := total.Sub(sa)
	if delta.Requests != 1 || delta.Percentile(1) < 2900*time.Microsecond {
		t.Fatalf("delta requests=%d p100=%v", delta.Requests, delta.Percentile(1))
	}
}

func BenchmarkRecord(b *testing.B) {
	s := NewShard()
	for i := 0; i < b.N; i++ {
		s.Record(time.Duration(i%5000)*time.Microsecond, 200, 128, "")
	}
}
