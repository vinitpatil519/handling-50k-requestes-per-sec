// Package events publishes and consumes domain events on Kafka using
// franz-go, a pure Go client with full Kafka 4.x protocol support.
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is the envelope written to the titan.events topic.
type Event struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Source string          `json:"source"`
	Time   time.Time       `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// NewEvent builds an event with a random 128-bit id.
func NewEvent(typ, source string, data json.RawMessage) Event {
	return Event{ID: NewID(), Type: typ, Source: source, Time: time.Now().UTC(), Data: data}
}

// NewID returns a random hex identifier.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ErrBackpressure means the in-memory produce buffer is full. Callers should
// shed load (HTTP 503) instead of queueing unbounded work.
var ErrBackpressure = errors.New("event buffer full")

// Publisher is implemented by Kafka and by fakes in tests.
type Publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
	Close()
}

var published = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "titan_kafka_published_total",
	Help: "Kafka records produced by result (ok, error, backpressure).",
}, []string{"topic", "result"})

// Kafka is an asynchronous, batching producer.
type Kafka struct {
	client      *kgo.Client
	topic       string
	maxBuffered int64
}

// NewKafka creates a producer tuned for throughput: 5ms linger, snappy
// compression and leader-only acks. Up to maxBuffered records are held in
// memory; beyond that Publish returns ErrBackpressure.
func NewKafka(brokers []string, topic string, maxBuffered int) (*Kafka, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.ProducerBatchCompression(kgo.SnappyCompression(), kgo.NoCompression()),
		kgo.RequiredAcks(kgo.LeaderAck()),
		kgo.DisableIdempotentWrite(),
		kgo.MaxBufferedRecords(maxBuffered),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ClientID("titan-api"),
	)
	if err != nil {
		return nil, err
	}
	return &Kafka{client: cl, topic: topic, maxBuffered: int64(maxBuffered)}, nil
}

// Publish enqueues a record without blocking. The record is delivered
// asynchronously; delivery results are exported as metrics.
func (k *Kafka) Publish(ctx context.Context, key string, value []byte) error {
	if k.client.BufferedProduceRecords() >= k.maxBuffered {
		published.WithLabelValues(k.topic, "backpressure").Inc()
		return ErrBackpressure
	}
	// The record must outlive the request, so detach from its cancellation.
	k.client.TryProduce(context.WithoutCancel(ctx), &kgo.Record{Key: []byte(key), Value: value},
		func(_ *kgo.Record, err error) {
			switch {
			case errors.Is(err, kgo.ErrMaxBuffered):
				published.WithLabelValues(k.topic, "backpressure").Inc()
			case err != nil:
				published.WithLabelValues(k.topic, "error").Inc()
			default:
				published.WithLabelValues(k.topic, "ok").Inc()
			}
		})
	return nil
}

// Ping checks broker connectivity.
func (k *Kafka) Ping(ctx context.Context) error { return k.client.Ping(ctx) }

// Close flushes buffered records (bounded by ctx in Flush) and closes.
func (k *Kafka) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = k.client.Flush(ctx)
	k.client.Close()
}

// EnsureTopic creates the topic if it does not exist. Replication factor is
// capped by the number of brokers so the same code works on a 1-broker kind
// cluster and a 3-broker production cluster.
func EnsureTopic(ctx context.Context, brokers []string, topic string, partitions int) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)

	meta, err := adm.BrokerMetadata(ctx)
	if err != nil {
		return fmt.Errorf("broker metadata: %w", err)
	}
	rf := int16(min(3, len(meta.Brokers)))
	if rf < 1 {
		rf = 1
	}
	resp, err := adm.CreateTopics(ctx, int32(partitions), rf, map[string]*string{
		"retention.ms": ptr("86400000"),
	}, topic)
	if err != nil {
		return err
	}
	for _, r := range resp {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("create topic %s: %w", r.Topic, r.Err)
		}
	}
	return nil
}

func ptr(s string) *string { return &s }
