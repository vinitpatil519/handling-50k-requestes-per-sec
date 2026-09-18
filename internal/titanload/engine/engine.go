// Package engine is titanload's load generator: a pool of goroutines that
// issue requests either as fast as possible (closed model) or at a fixed
// arrival rate (open model) with coordinated-omission-corrected latency.
package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/titanedge/titanedge/internal/titanload/stats"
)

// Mode selects the wire protocol.
type Mode string

const (
	ModeHTTP1 Mode = "http1" // HTTP/1.1 (TLS or cleartext)
	ModeHTTP2 Mode = "http2" // HTTP/2 over TLS, or h2c prior-knowledge for http:// URLs
	ModeGRPC  Mode = "grpc"  // grpc.health.v1.Health/Check
)

// ParseMode normalises user input; "h2c" and "h2" are aliases of http2.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "http1", "http", "h1", "http/1.1":
		return ModeHTTP1, nil
	case "http2", "h2", "h2c", "http/2":
		return ModeHTTP2, nil
	case "grpc":
		return ModeGRPC, nil
	}
	return "", fmt.Errorf("unknown mode %q (http1 | http2 | grpc)", s)
}

// Target is one request template. Targets are used round-robin.
type Target struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Body    []byte      `json:"body,omitempty"`
	Headers http.Header `json:"headers,omitempty"`
}

// Config fully describes a run. It is JSON-serialisable because the
// coordinator ships it to remote workers.
type Config struct {
	Targets          []Target      `json:"targets"`
	Mode             Mode          `json:"mode"`
	Concurrency      int           `json:"concurrency"`
	Connections      int           `json:"connections"`
	Rate             float64       `json:"rate"`
	Duration         time.Duration `json:"duration"`
	MaxRequests      uint64        `json:"max_requests"`
	Timeout          time.Duration `json:"timeout"`
	Ramp             time.Duration `json:"ramp"`
	Insecure         bool          `json:"insecure"`
	DisableKeepAlive bool          `json:"disable_keepalive"`
	GRPCService      string        `json:"grpc_service"`
}

// Validate applies defaults and checks invariants.
func (c *Config) Validate() error {
	if len(c.Targets) == 0 {
		return errors.New("at least one target URL is required")
	}
	if c.Mode == "" {
		c.Mode = ModeHTTP1
		if strings.HasPrefix(c.Targets[0].URL, "grpc") {
			c.Mode = ModeGRPC
		}
	}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Method == "" {
			t.Method = http.MethodGet
		}
		t.Method = strings.ToUpper(t.Method)
		if c.Mode != ModeGRPC && !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") {
			return fmt.Errorf("target %q must start with http:// or https://", t.URL)
		}
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be >= 1")
	}
	if c.Rate < 0 {
		return errors.New("rate must be >= 0")
	}
	if c.Duration <= 0 && c.MaxRequests == 0 {
		return errors.New("set a duration or a request count")
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	return nil
}

// Requester executes one request.
type Requester interface {
	Do(ctx context.Context, t *Target) (status int, bytes int64, err error)
	Close()
}

// Engine runs a load test.
type Engine struct {
	cfg    Config
	req    Requester
	shards []*stats.Shard

	issued  atomic.Uint64
	startNs atomic.Int64
	endNs   atomic.Int64
	cancel  atomic.Pointer[context.CancelFunc]
	done    chan struct{}
}

// New validates cfg and builds the protocol-specific requester.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var (
		req Requester
		err error
	)
	switch cfg.Mode {
	case ModeGRPC:
		req, err = newGRPCRequester(cfg)
	default:
		req = newHTTPRequester(cfg)
	}
	if err != nil {
		return nil, err
	}
	return NewWithRequester(cfg, req), nil
}

// NewWithRequester is used by tests to inject a fake transport.
func NewWithRequester(cfg Config, req Requester) *Engine {
	e := &Engine{cfg: cfg, req: req, done: make(chan struct{})}
	e.shards = make([]*stats.Shard, cfg.Concurrency)
	for i := range e.shards {
		e.shards[i] = stats.NewShard()
	}
	return e
}

// Config returns the effective (validated) configuration.
func (e *Engine) Config() Config { return e.cfg }

// Done is closed when Run returns.
func (e *Engine) Done() <-chan struct{} { return e.done }

// Stop ends the run early; in-flight requests complete normally.
func (e *Engine) Stop() {
	if c := e.cancel.Load(); c != nil {
		(*c)()
	}
}

// Elapsed since start (frozen once the run is finished).
func (e *Engine) Elapsed() time.Duration {
	start := e.startNs.Load()
	if start == 0 {
		return 0
	}
	if end := e.endNs.Load(); end != 0 {
		return time.Duration(end - start)
	}
	return time.Duration(time.Now().UnixNano() - start)
}

// Snapshot aggregates all shards. Safe to call concurrently with Run.
func (e *Engine) Snapshot() *stats.Snapshot {
	sn := stats.NewSnapshot()
	for _, s := range e.shards {
		sn.AddShard(s)
	}
	sn.Elapsed = e.Elapsed()
	return sn
}

// Run blocks until the duration elapses, MaxRequests are issued, Stop is
// called or ctx is cancelled. It returns the final snapshot.
func (e *Engine) Run(ctx context.Context) *stats.Snapshot {
	defer e.req.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if e.cfg.Duration > 0 {
		var c2 context.CancelFunc
		ctx, c2 = context.WithTimeout(ctx, e.cfg.Duration)
		defer c2()
	}
	e.cancel.Store(&cancel)

	start := time.Now()
	e.startNs.Store(start.UnixNano())

	var tokens chan time.Time
	if e.cfg.Rate > 0 {
		tokens = make(chan time.Time, e.cfg.Concurrency)
		go e.pace(ctx, start, tokens)
	}

	var wg sync.WaitGroup
	for i, shard := range e.shards {
		var delay time.Duration
		if tokens == nil && e.cfg.Ramp > 0 {
			delay = time.Duration(int64(e.cfg.Ramp) * int64(i) / int64(len(e.shards)))
		}
		wg.Go(func() { e.worker(ctx, i, shard, tokens, delay) })
	}
	wg.Wait()
	e.endNs.Store(time.Now().UnixNano())
	close(e.done)
	return e.Snapshot()
}

func (e *Engine) worker(ctx context.Context, id int, shard *stats.Shard, tokens <-chan time.Time, delay time.Duration) {
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	targets := e.cfg.Targets
	idx := id % len(targets)
	for {
		var scheduled time.Time
		if tokens != nil {
			var ok bool
			select {
			case <-ctx.Done():
				return
			case scheduled, ok = <-tokens:
				if !ok {
					return
				}
			}
		} else {
			if ctx.Err() != nil {
				return
			}
			scheduled = time.Now()
		}
		if max := e.cfg.MaxRequests; max > 0 && e.issued.Add(1) > max {
			e.Stop()
			return
		}

		t := &targets[idx]
		idx = (idx + 1) % len(targets)

		// Requests use their own context so that the end of the run does not
		// turn in-flight requests into spurious "canceled" errors.
		rctx, rcancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
		status, n, err := e.req.Do(rctx, t)
		rcancel()
		// Latency is measured from the *scheduled* send time. In rate mode
		// this includes time spent queued behind a slow server, which is
		// what a real user would experience (no coordinated omission).
		lat := time.Since(scheduled)
		kind := ""
		if err != nil {
			kind = Classify(err)
		}
		shard.Record(lat, status, n, kind)
	}
}

// pace emits one token per request at the intended send time. Tokens carry
// their schedule so a stalled pacer never hides queueing delay.
func (e *Engine) pace(ctx context.Context, start time.Time, tokens chan<- time.Time) {
	defer close(tokens)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var sent uint64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			want := uint64(ExpectedRequests(e.cfg.Rate, e.cfg.Ramp, now.Sub(start)))
			for sent < want {
				at := start.Add(ScheduleOffset(e.cfg.Rate, e.cfg.Ramp, sent))
				select {
				case tokens <- at:
					sent++
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// ExpectedRequests is the integral of the arrival rate from 0 to t, with an
// optional linear ramp from 0 to rate over the ramp duration.
func ExpectedRequests(rate float64, ramp, t time.Duration) float64 {
	s := t.Seconds()
	if ramp <= 0 {
		return rate * s
	}
	r := ramp.Seconds()
	if s < r {
		return rate * s * s / (2 * r)
	}
	return rate*r/2 + rate*(s-r)
}

// ScheduleOffset is the inverse of ExpectedRequests: when request n is due.
func ScheduleOffset(rate float64, ramp time.Duration, n uint64) time.Duration {
	x := float64(n)
	if ramp <= 0 {
		return time.Duration(x / rate * float64(time.Second))
	}
	r := ramp.Seconds()
	if x < rate*r/2 {
		return time.Duration(math.Sqrt(2*r*x/rate) * float64(time.Second))
	}
	return time.Duration((r + (x-rate*r/2)/rate) * float64(time.Second))
}

// ParseTargetsFile reads "METHOD URL [BODY|@file]" lines. Blank lines and
// lines starting with # are ignored.
func ParseTargetsFile(path string, headers http.Header) ([]Target, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Target
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 {
			return nil, fmt.Errorf("%s:%d: want \"METHOD URL [BODY]\"", path, line)
		}
		t := Target{Method: fields[0], URL: fields[1], Headers: headers}
		// Everything after the URL (which may contain spaces) is the body.
		rest := text[strings.Index(text, fields[1])+len(fields[1]):]
		if body := strings.TrimSpace(rest); body != "" {
			if strings.HasPrefix(body, "@") {
				b, err := os.ReadFile(body[1:])
				if err != nil {
					return nil, fmt.Errorf("%s:%d: %w", path, line, err)
				}
				t.Body = b
			} else {
				t.Body = []byte(body)
			}
		}
		out = append(out, t)
	}
	return out, sc.Err()
}
