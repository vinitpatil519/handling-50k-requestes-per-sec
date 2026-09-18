package distributed

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/titanedge/titanedge/internal/titanload/engine"
)

func TestCoordinatorAggregatesWorkers(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := engine.Config{
		Targets:     []engine.Target{{URL: target.URL}},
		Concurrency: 2,
		MaxRequests: 300,
		Timeout:     2 * time.Second,
	}
	coord, err := NewCoordinator(cfg, CoordinatorOptions{Expect: 3, Token: "s3cret", RunID: "t", StartDelay: 100 * time.Millisecond}, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(coord.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		wg.Go(func() {
			if err := RunWorker(ctx, WorkerOptions{Coordinator: srv.URL, Name: name, Token: "s3cret"}, log); err != nil {
				t.Errorf("worker %s: %v", name, err)
			}
		})
	}
	if err := coord.Run(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	sn := coord.Snapshot()
	if sn.Requests != 300 {
		t.Fatalf("aggregated requests = %d, want 300", sn.Requests)
	}
	for _, w := range coord.Workers() {
		if w.State != "done" || w.Snapshot.Requests != 100 {
			t.Errorf("worker %s state=%s requests=%d", w.Name, w.State, w.Snapshot.Requests)
		}
	}
}

func TestTokenRequired(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	coord, _ := NewCoordinator(engine.Config{Targets: []engine.Target{{URL: "http://x"}}, Concurrency: 1, Duration: time.Second},
		CoordinatorOptions{Expect: 1, Token: "t"}, log)
	rec := httptest.NewRecorder()
	coord.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/register", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}
