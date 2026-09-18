// Package distributed implements titanload's coordinator/worker mode.
//
// Protocol (JSON over HTTP, optional shared token in X-Titan-Token):
//
//	POST /v1/register          worker -> coordinator   {name, host, cpus} => {id}
//	GET  /v1/job?id=ID         long-poll until the job is released (204 = keep polling)
//	POST /v1/report            worker -> coordinator   cumulative snapshot every second
//	GET  /v1/status            human / CI readable aggregate
//	GET  /metrics              Prometheus metrics for Grafana
//
// The coordinator waits for --expect-workers registrations, splits the target
// rate and request budget evenly, and releases a job with a common start
// delay so all workers begin together. Snapshots are cumulative and
// histogram-mergeable, so aggregation is exact rather than averaged.
package distributed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/titanedge/titanedge/internal/titanload/dashboard"
	"github.com/titanedge/titanedge/internal/titanload/engine"
	"github.com/titanedge/titanedge/internal/titanload/stats"
)

// Job is sent to every worker.
type Job struct {
	RunID      string        `json:"run_id"`
	Config     engine.Config `json:"config"`
	StartDelay time.Duration `json:"start_delay"`
}

type registerRequest struct {
	Name string `json:"name"`
	Host string `json:"host"`
	CPUs int    `json:"cpus"`
}

type registerResponse struct {
	ID string `json:"id"`
}

type reportRequest struct {
	ID       string          `json:"id"`
	Snapshot *stats.Snapshot `json:"snapshot"`
	Done     bool            `json:"done"`
	Error    string          `json:"error,omitempty"`
}

type workerState struct {
	id, name, host string
	cpus           int
	snap           *stats.Snapshot
	state          string
	lastSeen       time.Time
	err            string
}

// Coordinator orchestrates remote workers and aggregates their results.
type Coordinator struct {
	log     *slog.Logger
	token   string
	expect  int
	cfg     engine.Config
	runID   string
	delay   time.Duration
	metrics http.Handler

	mu       sync.Mutex
	workers  map[string]*workerState
	order    []string
	job      *Job
	released chan struct{}
	started  time.Time
	done     chan struct{}
	doneOnce sync.Once
}

// CoordinatorOptions configures a coordinator.
type CoordinatorOptions struct {
	Expect     int
	Token      string
	RunID      string
	StartDelay time.Duration
	Metrics    http.Handler
}

// NewCoordinator creates a coordinator for cfg, which is split across workers.
func NewCoordinator(cfg engine.Config, opts CoordinatorOptions, log *slog.Logger) (*Coordinator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if opts.Expect < 1 {
		return nil, errors.New("expect-workers must be >= 1")
	}
	if opts.StartDelay <= 0 {
		opts.StartDelay = 3 * time.Second
	}
	return &Coordinator{
		log: log, token: opts.Token, expect: opts.Expect, cfg: cfg,
		runID: opts.RunID, delay: opts.StartDelay, metrics: opts.Metrics,
		workers: map[string]*workerState{}, released: make(chan struct{}), done: make(chan struct{}),
	}, nil
}

// Handler returns the coordinator's HTTP API.
func (c *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", c.auth(c.handleRegister))
	mux.HandleFunc("GET /v1/job", c.auth(c.handleJob))
	mux.HandleFunc("POST /v1/report", c.auth(c.handleReport))
	mux.HandleFunc("GET /v1/status", c.handleStatus)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if c.metrics != nil {
		mux.Handle("GET /metrics", c.metrics)
	}
	return mux
}

func (c *Coordinator) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.token != "" && r.Header.Get("X-Titan-Token") != c.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (c *Coordinator) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job != nil {
		http.Error(w, "run already started", http.StatusConflict)
		return
	}
	id := fmt.Sprintf("w%03d", len(c.order)+1)
	name := req.Name
	if name == "" {
		name = id
	}
	c.workers[id] = &workerState{id: id, name: name, host: req.Host, cpus: req.CPUs, state: "ready", lastSeen: time.Now()}
	c.order = append(c.order, id)
	c.log.Info("worker registered", "id", id, "name", name, "host", req.Host, "cpus", req.CPUs,
		"registered", len(c.order), "expected", c.expect)
	writeJSON(w, registerResponse{ID: id})
}

func (c *Coordinator) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	c.mu.Lock()
	_, ok := c.workers[id]
	c.mu.Unlock()
	if !ok {
		http.Error(w, "unknown worker id", http.StatusNotFound)
		return
	}
	select {
	case <-c.released:
		c.mu.Lock()
		job := *c.job
		// Each worker's start delay shrinks by the time since release so all
		// workers target the same wall-clock start.
		job.StartDelay = max(0, time.Until(c.started))
		c.workers[id].state = "running"
		c.mu.Unlock()
		writeJSON(w, job)
	case <-time.After(25 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

func (c *Coordinator) handleReport(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	ws, ok := c.workers[req.ID]
	if ok {
		if req.Snapshot != nil {
			ws.snap = req.Snapshot
		}
		ws.lastSeen = time.Now()
		switch {
		case req.Error != "":
			ws.state, ws.err = "failed", req.Error
		case req.Done:
			ws.state = "done"
		}
	}
	allDone := c.allFinishedLocked()
	c.mu.Unlock()
	if !ok {
		http.Error(w, "unknown worker id", http.StatusNotFound)
		return
	}
	if allDone {
		c.finish()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Coordinator) handleStatus(w http.ResponseWriter, _ *http.Request) {
	sn := c.Snapshot()
	type wv struct {
		Name     string  `json:"name"`
		Host     string  `json:"host"`
		State    string  `json:"state"`
		Requests uint64  `json:"requests"`
		Error    string  `json:"error,omitempty"`
		P99Ms    float64 `json:"p99_ms"`
	}
	c.mu.Lock()
	ws := make([]wv, 0, len(c.order))
	for _, id := range c.order {
		s := c.workers[id]
		v := wv{Name: s.name, Host: s.host, State: s.state, Error: s.err}
		if s.snap != nil {
			v.Requests = s.snap.Requests
			v.P99Ms = float64(s.snap.Percentile(.99).Microseconds()) / 1000
		}
		ws = append(ws, v)
	}
	c.mu.Unlock()
	writeJSON(w, map[string]any{
		"run_id": c.runID, "expected_workers": c.expect, "workers": ws,
		"requests": sn.Requests, "failures": sn.Failures, "rps": sn.RPS(),
		"p99_ms": float64(sn.Percentile(.99).Microseconds()) / 1000,
	})
}

func (c *Coordinator) allFinishedLocked() bool {
	if c.job == nil || len(c.order) == 0 {
		return false
	}
	for _, id := range c.order {
		if s := c.workers[id].state; s != "done" && s != "failed" && s != "lost" {
			return false
		}
	}
	return true
}

func (c *Coordinator) finish() { c.doneOnce.Do(func() { close(c.done) }) }

// Done implements dashboard.Source.
func (c *Coordinator) Done() <-chan struct{} { return c.done }

// Released is closed once the job has been handed to workers.
func (c *Coordinator) Released() <-chan struct{} { return c.released }

// Snapshot merges all workers' cumulative snapshots.
func (c *Coordinator) Snapshot() *stats.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := stats.NewSnapshot()
	for _, id := range c.order {
		total.Merge(c.workers[id].snap)
	}
	return total
}

// Workers implements dashboard.Source.
func (c *Coordinator) Workers() []dashboard.WorkerView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]dashboard.WorkerView, 0, len(c.order))
	for _, id := range c.order {
		s := c.workers[id]
		out = append(out, dashboard.WorkerView{Name: s.name, State: s.state, Snapshot: s.snap})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WorkerCount is the number of registered workers.
func (c *Coordinator) WorkerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.order)
}

// Run waits for workers, releases the job and blocks until every worker has
// finished, a worker goes silent past the deadline, or ctx is cancelled.
func (c *Coordinator) Run(ctx context.Context) error {
	c.log.Info("waiting for workers", "expected", c.expect)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for c.WorkerCount() < c.expect {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}

	c.mu.Lock()
	n := len(c.order)
	cfg := c.cfg
	if cfg.Rate > 0 {
		cfg.Rate = cfg.Rate / float64(n)
	}
	if cfg.MaxRequests > 0 {
		cfg.MaxRequests = (cfg.MaxRequests + uint64(n) - 1) / uint64(n)
	}
	c.job = &Job{RunID: c.runID, Config: cfg, StartDelay: c.delay}
	c.started = time.Now().Add(c.delay)
	close(c.released)
	c.mu.Unlock()
	c.log.Info("job released", "workers", n, "rate_per_worker", cfg.Rate, "start_in", c.delay)

	// Deadline: run length + request timeout + generous grace for reporting.
	deadline := time.Duration(0)
	if cfg.Duration > 0 {
		deadline = c.delay + cfg.Duration + cfg.Timeout + 30*time.Second
	}
	var timeout <-chan time.Time
	if deadline > 0 {
		timeout = time.After(deadline)
	}
	for {
		select {
		case <-ctx.Done():
			c.finish()
			return ctx.Err()
		case <-c.done:
			return nil
		case <-timeout:
			c.mu.Lock()
			for _, id := range c.order {
				if s := c.workers[id]; s.state == "running" || s.state == "ready" {
					s.state = "lost"
				}
			}
			c.mu.Unlock()
			c.log.Warn("deadline reached; some workers never reported completion")
			c.finish()
			return nil
		case <-tick.C:
			c.markLost()
		}
	}
}

func (c *Coordinator) markLost() {
	c.mu.Lock()
	for _, id := range c.order {
		s := c.workers[id]
		if s.state == "running" && s.snap != nil && time.Since(s.lastSeen) > 15*time.Second {
			s.state = "lost"
			c.log.Warn("worker lost", "name", s.name)
		}
	}
	all := c.allFinishedLocked()
	c.mu.Unlock()
	if all {
		c.finish()
	}
}

// Listen starts the coordinator HTTP server and returns it.
func (c *Coordinator) Listen(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("coordinator server", "err", err)
		}
	}()
	return srv, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
