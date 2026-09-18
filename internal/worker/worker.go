// Package worker consumes titan.events from Kafka, aggregates them in memory
// and flushes per-minute rollups to PostgreSQL and live counters to Redis.
//
// Delivery is at-least-once: offsets are committed only after a successful
// flush, and rebalances are blocked until pending aggregates are persisted.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/config"
	"github.com/titanedge/titanedge/internal/events"
	"github.com/titanedge/titanedge/internal/store"
)

var (
	consumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "titan_worker_events_total",
		Help: "Events consumed by result (ok, invalid).",
	}, []string{"result"})
	flushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "titan_worker_flush_duration_seconds",
		Help:    "Time to persist one aggregated batch.",
		Buckets: prometheus.DefBuckets,
	})
	flushErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "titan_worker_flush_errors_total",
		Help: "Failed flushes (batch retained and retried).",
	})
	batchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "titan_worker_batch_events",
		Help:    "Events per flushed batch.",
		Buckets: prometheus.ExponentialBuckets(1, 4, 10),
	})
)

type rollupKey struct {
	bucket time.Time
	typ    string
}

// Aggregator folds events into per-minute counters. Not safe for concurrent use.
type Aggregator struct {
	rollups map[rollupKey]int64
	types   map[string]int64
	n       int
}

func NewAggregator() *Aggregator {
	return &Aggregator{rollups: map[rollupKey]int64{}, types: map[string]int64{}}
}

// Add counts one event into its minute bucket.
func (a *Aggregator) Add(ev events.Event) {
	ts := ev.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	a.rollups[rollupKey{ts.UTC().Truncate(time.Minute), ev.Type}]++
	a.types[ev.Type]++
	a.n++
}

// Len is the number of events aggregated since the last Reset.
func (a *Aggregator) Len() int { return a.n }

// Rollups returns the pending database deltas.
func (a *Aggregator) Rollups() []store.Rollup {
	out := make([]store.Rollup, 0, len(a.rollups))
	for k, v := range a.rollups {
		out = append(out, store.Rollup{Bucket: k.bucket, Type: k.typ, Count: v})
	}
	return out
}

// Counters returns the pending Redis increments.
func (a *Aggregator) Counters() map[string]int64 {
	out := make(map[string]int64, len(a.types)+1)
	for t, v := range a.types {
		out[cache.KeyEventsPrefix+t] = v
	}
	out[cache.KeyEventsTotal] = int64(a.n)
	return out
}

// Reset clears all pending state.
func (a *Aggregator) Reset() {
	clear(a.rollups)
	clear(a.types)
	a.n = 0
}

// Sink is where aggregates go. Either field may be nil.
type Sink struct {
	Store store.Store
	Cache cache.Cache
}

func (s Sink) flush(ctx context.Context, agg *Aggregator) error {
	if agg.Len() == 0 {
		return nil
	}
	start := time.Now()
	if s.Store != nil {
		if err := s.Store.AddRollups(ctx, agg.Rollups()); err != nil {
			return err
		}
	}
	if s.Cache != nil {
		// Counters are for live display; a Redis failure after a successful
		// database write is logged but must not cause a double-counting retry.
		if err := s.Cache.IncrBy(ctx, agg.Counters()); err != nil {
			slog.WarnContext(ctx, "redis counter update failed", "err", err)
		}
	}
	flushDuration.Observe(time.Since(start).Seconds())
	batchSize.Observe(float64(agg.Len()))
	return nil
}

// Consumer runs the poll/aggregate/flush/commit loop.
type Consumer struct {
	cfg    config.Config
	log    *slog.Logger
	sink   Sink
	client *kgo.Client
}

func NewConsumer(cfg config.Config, log *slog.Logger, sink Sink) (*Consumer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ConsumerGroup(cfg.KafkaGroupID),
		kgo.ConsumeTopics(cfg.KafkaTopic),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.ClientID("titan-worker-"+cfg.PodName),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, err
	}
	return &Consumer{cfg: cfg, log: log, sink: sink, client: cl}, nil
}

// Ping reports broker connectivity for readiness.
func (c *Consumer) Ping(ctx context.Context) error { return c.client.Ping(ctx) }

// Run blocks until ctx is cancelled, then flushes and commits what it has.
func (c *Consumer) Run(ctx context.Context) error {
	defer c.client.Close()
	agg := NewAggregator()
	lastFlush := time.Now()

	flushAndCommit := func(fctx context.Context) {
		if err := c.sink.flush(fctx, agg); err != nil {
			flushErrors.Inc()
			c.log.Error("flush failed; will retry", "err", err, "pending", agg.Len())
			return
		}
		if err := c.client.CommitUncommittedOffsets(fctx); err != nil {
			c.log.Error("commit failed", "err", err)
		}
		agg.Reset()
		lastFlush = time.Now()
		c.client.AllowRebalance()
	}

	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, c.cfg.WorkerFlushInterval)
		fetches := c.client.PollRecords(pctx, c.cfg.WorkerFlushSize)
		cancel()
		if fetches.IsClientClosed() {
			break
		}
		fetches.EachError(func(topic string, part int32, err error) {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return
			}
			c.log.Warn("fetch error", "topic", topic, "partition", part, "err", err)
		})
		fetches.EachRecord(func(r *kgo.Record) {
			var ev events.Event
			if err := json.Unmarshal(r.Value, &ev); err != nil || ev.Type == "" {
				consumed.WithLabelValues("invalid").Inc()
				return
			}
			consumed.WithLabelValues("ok").Inc()
			agg.Add(ev)
		})

		if agg.Len() >= c.cfg.WorkerFlushSize || time.Since(lastFlush) >= c.cfg.WorkerFlushInterval {
			flushAndCommit(ctx)
		}
	}

	// Graceful shutdown: persist whatever is pending before leaving the group.
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	flushAndCommit(sctx)
	c.log.Info("consumer stopped")
	return nil
}
