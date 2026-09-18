package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/titanedge/titanedge/internal/titanload/engine"
)

// WorkerOptions configures a remote load worker.
type WorkerOptions struct {
	Coordinator string // http://host:7070
	Name        string
	Token       string
	// Loop keeps the worker registered for subsequent runs (daemon mode for
	// systemd / Kubernetes Deployments).
	Loop bool
}

type client struct {
	base  string
	token string
	http  *http.Client
}

func (c *client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-Titan-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

// RunWorker registers with the coordinator, executes the job and streams
// cumulative snapshots every second until the run finishes.
func RunWorker(ctx context.Context, opts WorkerOptions, log *slog.Logger) error {
	host, _ := os.Hostname()
	if opts.Name == "" {
		opts.Name = host
	}
	c := &client{
		base:  strings.TrimRight(opts.Coordinator, "/"),
		token: opts.Token,
		http:  &http.Client{Timeout: 40 * time.Second},
	}
	for {
		if err := runOnce(ctx, c, opts.Name, host, log); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !opts.Loop {
				return err
			}
			log.Warn("run failed; re-registering", "err", err)
		}
		if !opts.Loop || ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func runOnce(ctx context.Context, c *client, name, host string, log *slog.Logger) error {
	var reg registerResponse
	for attempt := 1; ; attempt++ {
		_, err := c.do(ctx, http.MethodPost, "/v1/register", registerRequest{Name: name, Host: host, CPUs: runtime.NumCPU()}, &reg)
		if err == nil {
			break
		}
		log.Info("waiting for coordinator", "url", c.base, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(time.Duration(attempt)*time.Second, 5*time.Second)):
		}
	}
	log.Info("registered", "id", reg.ID, "coordinator", c.base)

	var job Job
	for {
		code, err := c.do(ctx, http.MethodGet, "/v1/job?id="+reg.ID, nil, &job)
		if err != nil {
			return err
		}
		if code == http.StatusOK {
			break
		}
	}
	log.Info("job received", "run_id", job.RunID, "rate", job.Config.Rate,
		"concurrency", job.Config.Concurrency, "duration", job.Config.Duration, "start_in", job.StartDelay)

	e, err := engine.New(job.Config)
	if err != nil {
		_, _ = c.do(ctx, http.MethodPost, "/v1/report", reportRequest{ID: reg.ID, Error: err.Error()}, nil)
		return err
	}
	select {
	case <-time.After(job.StartDelay):
	case <-ctx.Done():
		return ctx.Err()
	}

	go e.Run(ctx)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-e.Done():
			final := e.Snapshot()
			// Retry the final report: losing it would drop this worker's data.
			var err error
			for i := 0; i < 5; i++ {
				rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err = c.do(rctx, http.MethodPost, "/v1/report", reportRequest{ID: reg.ID, Snapshot: final, Done: true}, nil)
				cancel()
				if err == nil {
					break
				}
				time.Sleep(time.Second)
			}
			log.Info("run complete", "requests", final.Requests, "rps", int(final.RPS()))
			return err
		case <-tick.C:
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := c.do(rctx, http.MethodPost, "/v1/report", reportRequest{ID: reg.ID, Snapshot: e.Snapshot()}, nil)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("report failed", "err", err)
			}
		}
	}
}
