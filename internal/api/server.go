// Package api implements the TitanEdge HTTP API.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/config"
	"github.com/titanedge/titanedge/internal/events"
	"github.com/titanedge/titanedge/internal/store"
)

// Deps are optional backends. A nil dependency disables the routes that need
// it (they return 503) while /api/v1/ping keeps working.
type Deps struct {
	Store     store.Store
	Cache     cache.Cache
	Publisher events.Publisher
	// Checks are run by /readyz. Keys are dependency names.
	Checks map[string]func(context.Context) error
}

// Server holds handler state.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	deps     Deps
	inflight chan struct{}
	started  time.Time
	requests atomic.Uint64
	pingBody []byte

	readyMu      sync.Mutex
	readyAt      time.Time
	readyOK      bool
	readyDetails map[string]string

	countMu sync.Mutex
	countAt time.Time
	count   int64
}

// New builds a Server.
func New(cfg config.Config, log *slog.Logger, deps Deps) *Server {
	body, _ := json.Marshal(map[string]any{"pong": true, "pod": cfg.PodName})
	return &Server{
		cfg:      cfg,
		log:      log,
		deps:     deps,
		inflight: make(chan struct{}, cfg.MaxInflight),
		started:  time.Now(),
		pingBody: body,
	}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern, name string, h http.HandlerFunc) {
		mux.Handle(pattern, s.instrument(name, h))
	}

	// Probes bypass load shedding so an overloaded pod is not killed by the
	// kubelet; it is only removed from endpoints when truly not ready.
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", s.healthz)
	probes.HandleFunc("GET /readyz", s.readyz)

	route("GET /api/v1/ping", "ping", s.ping)
	route("GET /api/v1/items", "items_list", s.listItems)
	route("POST /api/v1/items", "items_create", s.createItem)
	route("GET /api/v1/items/{id}", "items_get", s.getItem)
	route("POST /api/v1/events", "events_publish", s.publishEvent)
	route("GET /api/v1/stats", "stats", s.stats)

	app := otelhttp.NewHandler(s.shed(mux), "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/api/v1/ping" }),
	)

	root := http.NewServeMux()
	root.Handle("GET /healthz", probes)
	root.Handle("GET /readyz", probes)
	root.Handle("/", app)
	return s.recoverer(requestID(root))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ok, details := s.Ready(r.Context())
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"ready": ok, "checks": details})
}

// Ready runs dependency checks, cached for one second so that aggressive
// probes and load balancers cannot turn into a thundering herd on backends.
func (s *Server) Ready(ctx context.Context) (bool, map[string]string) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if time.Since(s.readyAt) < time.Second && s.readyDetails != nil {
		return s.readyOK, s.readyDetails
	}
	ok := true
	details := make(map[string]string, len(s.deps.Checks))
	for name, check := range s.deps.Checks {
		cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := check(cctx)
		cancel()
		if err != nil {
			ok = false
			details[name] = err.Error()
			continue
		}
		details[name] = "ok"
	}
	s.readyAt, s.readyOK, s.readyDetails = time.Now(), ok, details
	return ok, details
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		code = http.StatusInternalServerError
		b = []byte(`{"error":"encoding failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
