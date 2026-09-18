package api

import (
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/titanedge/titanedge/internal/events"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "titan_http_requests_total",
		Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "titan_http_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route", "method"})

	httpInflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "titan_http_inflight_requests",
		Help: "Requests currently being served.",
	})

	httpShed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "titan_http_shed_total",
		Help: "Requests rejected with 503 because MAX_INFLIGHT was reached.",
	})
)

// statusRecorder captures the response code for metrics and logs.
type statusRecorder struct {
	http.ResponseWriter
	code  int
	bytes int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// instrument records RED metrics (rate, errors, duration) for one route and
// emits a sampled access log line. Logging every request at 100k+ RPS would
// cost more than serving it, hence LOG_SAMPLE_RATE.
func (s *Server) instrument(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		h(rec, r)
		if rec.code == 0 {
			rec.code = http.StatusOK
		}
		elapsed := time.Since(start)
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		httpDuration.WithLabelValues(route, r.Method).Observe(elapsed.Seconds())
		s.requests.Add(1)

		if rec.code >= 500 || rand.Float64() < s.cfg.LogSampleRate {
			lvl := slog.LevelInfo
			if rec.code >= 500 {
				lvl = slog.LevelWarn
			}
			s.log.LogAttrs(r.Context(), lvl, "http request",
				slog.String("route", route),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.code),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
				slog.String("request_id", w.Header().Get("X-Request-ID")),
			)
		}
	})
}

// shed is an admission controller: a non-blocking semaphore that rejects work
// beyond MAX_INFLIGHT instead of letting latency explode for everyone.
func (s *Server) shed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.inflight <- struct{}{}:
			httpInflight.Inc()
			defer func() {
				<-s.inflight
				httpInflight.Dec()
			}()
			next.ServeHTTP(w, r)
		default:
			httpShed.Inc()
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "overloaded")
		}
	})
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			id = events.NewID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic recovered", "panic", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
