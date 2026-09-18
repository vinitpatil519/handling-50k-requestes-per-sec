// Package stats provides a lock-free, mergeable latency histogram and the
// Snapshot type exchanged between titanload workers and the coordinator.
package stats

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Buckets are logarithmic in microseconds with 2% relative width, covering
// 1µs .. ~60s in ~900 buckets. Percentiles are therefore accurate to ±2%,
// and histograms from many workers merge by plain addition.
const (
	growth     = 1.02
	maxMicros  = 60_000_000
	NumBuckets = 906 // bucketFor(maxMicros) + 1
)

var logGrowth = math.Log(growth)

func bucketFor(us float64) int {
	if us < 1 {
		return 0
	}
	i := int(math.Log(us)/logGrowth) + 1
	if i >= NumBuckets {
		return NumBuckets - 1
	}
	return i
}

// BucketUpper returns the upper bound of bucket i in microseconds.
func BucketUpper(i int) float64 {
	if i <= 0 {
		return 1
	}
	return math.Pow(growth, float64(i))
}

// Status classes tracked per response.
const (
	Class1xx = iota
	Class2xx
	Class3xx
	Class4xx
	Class5xx
	ClassOther
	numClasses
)

// Shard is written by exactly one load goroutine and read concurrently by the
// reporter. Atomics are uncontended, so recording costs a few nanoseconds.
type Shard struct {
	hist     [NumBuckets]atomic.Uint64
	requests atomic.Uint64
	failures atomic.Uint64
	bytes    atomic.Uint64
	sumUs    atomic.Uint64
	maxUs    atomic.Uint64
	minUs    atomic.Uint64
	classes  [numClasses]atomic.Uint64
	errMu    sync.Mutex
	errKinds map[string]uint64
}

// NewShard allocates a shard.
func NewShard() *Shard {
	s := &Shard{errKinds: map[string]uint64{}}
	s.minUs.Store(math.MaxUint64)
	return s
}

// Record stores one completed request. errKind is empty on transport success.
func (s *Shard) Record(latency time.Duration, status int, bytes int64, errKind string) {
	us := uint64(latency / time.Microsecond)
	s.hist[bucketFor(float64(us))].Add(1)
	s.requests.Add(1)
	s.sumUs.Add(us)
	if bytes > 0 {
		s.bytes.Add(uint64(bytes))
	}
	for {
		cur := s.maxUs.Load()
		if us <= cur || s.maxUs.CompareAndSwap(cur, us) {
			break
		}
	}
	for {
		cur := s.minUs.Load()
		if us >= cur || s.minUs.CompareAndSwap(cur, us) {
			break
		}
	}
	if errKind != "" {
		s.failures.Add(1)
		s.classes[ClassOther].Add(1)
		s.errMu.Lock()
		s.errKinds[errKind]++
		s.errMu.Unlock()
		return
	}
	c := ClassOther
	if status >= 100 && status < 600 {
		c = status/100 - 1
	}
	s.classes[c].Add(1)
	if status >= 400 {
		s.failures.Add(1)
	}
}

// Snapshot is a point-in-time, serialisable view of one or more shards.
type Snapshot struct {
	Requests uint64            `json:"requests"`
	Failures uint64            `json:"failures"`
	Bytes    uint64            `json:"bytes"`
	SumUs    uint64            `json:"sum_us"`
	MaxUs    uint64            `json:"max_us"`
	MinUs    uint64            `json:"min_us"`
	Classes  [6]uint64         `json:"classes"`
	ErrKinds map[string]uint64 `json:"err_kinds,omitempty"`
	Hist     []uint64          `json:"hist"`
	Elapsed  time.Duration     `json:"elapsed"`
}

// NewSnapshot returns an empty snapshot.
func NewSnapshot() *Snapshot {
	return &Snapshot{Hist: make([]uint64, NumBuckets), ErrKinds: map[string]uint64{}, MinUs: math.MaxUint64}
}

// AddShard folds a live shard into the snapshot.
func (sn *Snapshot) AddShard(s *Shard) {
	for i := range s.hist {
		sn.Hist[i] += s.hist[i].Load()
	}
	sn.Requests += s.requests.Load()
	sn.Failures += s.failures.Load()
	sn.Bytes += s.bytes.Load()
	sn.SumUs += s.sumUs.Load()
	sn.MaxUs = max(sn.MaxUs, s.maxUs.Load())
	sn.MinUs = min(sn.MinUs, s.minUs.Load())
	for i := range s.classes {
		sn.Classes[i] += s.classes[i].Load()
	}
	s.errMu.Lock()
	for k, v := range s.errKinds {
		sn.ErrKinds[k] += v
	}
	s.errMu.Unlock()
}

// Merge adds another snapshot (e.g. from a remote worker).
func (sn *Snapshot) Merge(o *Snapshot) {
	if o == nil {
		return
	}
	if len(sn.Hist) < NumBuckets {
		sn.Hist = append(sn.Hist, make([]uint64, NumBuckets-len(sn.Hist))...)
	}
	for i := 0; i < len(o.Hist) && i < NumBuckets; i++ {
		sn.Hist[i] += o.Hist[i]
	}
	sn.Requests += o.Requests
	sn.Failures += o.Failures
	sn.Bytes += o.Bytes
	sn.SumUs += o.SumUs
	sn.MaxUs = max(sn.MaxUs, o.MaxUs)
	if o.Requests > 0 {
		sn.MinUs = min(sn.MinUs, o.MinUs)
	}
	for i := range o.Classes {
		sn.Classes[i] += o.Classes[i]
	}
	if sn.ErrKinds == nil {
		sn.ErrKinds = map[string]uint64{}
	}
	for k, v := range o.ErrKinds {
		sn.ErrKinds[k] += v
	}
	sn.Elapsed = max(sn.Elapsed, o.Elapsed)
}

// Sub returns the delta sn - prev, used for per-interval rates and
// percentiles. Min/Max are not derivable from deltas and are left as-is.
func (sn *Snapshot) Sub(prev *Snapshot) *Snapshot {
	d := NewSnapshot()
	if prev == nil {
		d.Merge(sn)
		return d
	}
	for i := range sn.Hist {
		if i < len(prev.Hist) {
			d.Hist[i] = sn.Hist[i] - prev.Hist[i]
		} else {
			d.Hist[i] = sn.Hist[i]
		}
	}
	d.Requests = sn.Requests - prev.Requests
	d.Failures = sn.Failures - prev.Failures
	d.Bytes = sn.Bytes - prev.Bytes
	d.SumUs = sn.SumUs - prev.SumUs
	d.MaxUs, d.MinUs = sn.MaxUs, sn.MinUs
	for i := range sn.Classes {
		d.Classes[i] = sn.Classes[i] - prev.Classes[i]
	}
	for k, v := range sn.ErrKinds {
		if dv := v - prev.ErrKinds[k]; dv > 0 {
			d.ErrKinds[k] = dv
		}
	}
	d.Elapsed = sn.Elapsed - prev.Elapsed
	return d
}

// Percentile returns the q-quantile (0..1) latency.
func (sn *Snapshot) Percentile(q float64) time.Duration {
	var total uint64
	for _, c := range sn.Hist {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := uint64(math.Ceil(q * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for i, c := range sn.Hist {
		seen += c
		if seen >= rank {
			us := BucketUpper(i)
			if sn.MaxUs > 0 && us > float64(sn.MaxUs) {
				us = float64(sn.MaxUs) // never report above the observed max
			}
			return time.Duration(us * float64(time.Microsecond))
		}
	}
	return time.Duration(sn.MaxUs) * time.Microsecond
}

// Mean latency.
func (sn *Snapshot) Mean() time.Duration {
	if sn.Requests == 0 {
		return 0
	}
	return time.Duration(sn.SumUs/sn.Requests) * time.Microsecond
}

// Min latency (0 when nothing recorded).
func (sn *Snapshot) Min() time.Duration {
	if sn.Requests == 0 || sn.MinUs == math.MaxUint64 {
		return 0
	}
	return time.Duration(sn.MinUs) * time.Microsecond
}

// Max latency.
func (sn *Snapshot) Max() time.Duration { return time.Duration(sn.MaxUs) * time.Microsecond }

// ErrorRate is failures / requests in [0,1].
func (sn *Snapshot) ErrorRate() float64 {
	if sn.Requests == 0 {
		return 0
	}
	return float64(sn.Failures) / float64(sn.Requests)
}

// RPS over the snapshot's elapsed time.
func (sn *Snapshot) RPS() float64 {
	if sn.Elapsed <= 0 {
		return 0
	}
	return float64(sn.Requests) / sn.Elapsed.Seconds()
}

// ErrKindsSorted returns error kinds ordered by count, descending.
func (sn *Snapshot) ErrKindsSorted() []KV {
	out := make([]KV, 0, len(sn.ErrKinds))
	for k, v := range sn.ErrKinds {
		out = append(out, KV{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].V != out[j].V {
			return out[i].V > out[j].V
		}
		return out[i].K < out[j].K
	})
	return out
}

// KV is a name/count pair.
type KV struct {
	K string
	V uint64
}
