package dashboard

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/titanedge/titanedge/internal/titanload/stats"
)

// Exporter publishes live titanload results as Prometheus metrics so the
// "TitanLoad" Grafana dashboard can overlay generated load against server
// side RED metrics.
type Exporter struct {
	reg       *prometheus.Registry
	rps       prometheus.Gauge
	requests  prometheus.Gauge
	failures  prometheus.Gauge
	errRatio  prometheus.Gauge
	workers   prometheus.Gauge
	latency   *prometheus.GaugeVec
	byteRate  prometheus.Gauge
	lastBytes uint64
	lastAt    time.Time
}

// NewExporter creates an exporter labelled with the run id.
func NewExporter(runID string) *Exporter {
	reg := prometheus.NewRegistry()
	labels := prometheus.Labels{"run_id": runID}
	f := func(name, help string) prometheus.Gauge {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: labels})
		reg.MustRegister(g)
		return g
	}
	e := &Exporter{
		reg:      reg,
		rps:      f("titanload_rps", "Requests per second over the last interval."),
		requests: f("titanload_requests", "Requests completed since start."),
		failures: f("titanload_failures", "Failed requests since start."),
		errRatio: f("titanload_error_ratio", "Failed / total requests since start."),
		workers:  f("titanload_workers", "Connected distributed workers."),
		byteRate: f("titanload_bytes_per_second", "Response bytes per second."),
		latency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "titanload_latency_seconds", Help: "Latency quantiles over the last interval.", ConstLabels: labels,
		}, []string{"quantile"}),
	}
	reg.MustRegister(e.latency)
	return e
}

// Update records the latest values.
func (e *Exporter) Update(total, interval *stats.Snapshot, rps float64, workers int) {
	e.rps.Set(rps)
	e.requests.Set(float64(total.Requests))
	e.failures.Set(float64(total.Failures))
	e.errRatio.Set(total.ErrorRate())
	e.workers.Set(float64(workers))
	for _, q := range []struct {
		label string
		q     float64
	}{{"0.5", .5}, {"0.9", .9}, {"0.95", .95}, {"0.99", .99}, {"0.999", .999}} {
		e.latency.WithLabelValues(q.label).Set(interval.Percentile(q.q).Seconds())
	}
	now := time.Now()
	if !e.lastAt.IsZero() {
		if dt := now.Sub(e.lastAt).Seconds(); dt > 0 && total.Bytes >= e.lastBytes {
			e.byteRate.Set(float64(total.Bytes-e.lastBytes) / dt)
		}
	}
	e.lastBytes, e.lastAt = total.Bytes, now
}

// Handler serves /metrics.
func (e *Exporter) Handler() http.Handler {
	return promhttp.HandlerFor(e.reg, promhttp.HandlerOpts{})
}
