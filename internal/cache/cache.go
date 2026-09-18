// Package cache wraps Redis for cache-aside reads and shared counters.
package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// Cache is implemented by Redis and by in-memory fakes in tests.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	GetInt(ctx context.Context, key string) (int64, error)
	IncrBy(ctx context.Context, counts map[string]int64) error
	Ping(ctx context.Context) error
	Close() error
}

// Well known keys shared by API and worker.
const (
	KeyEventsTotal  = "titan:events:total"
	KeyEventsPrefix = "titan:events:type:"
	KeyItemPrefix   = "titan:item:"
)

var cacheOps = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "titan_cache_requests_total",
	Help: "Redis cache lookups by result (hit, miss, error).",
}, []string{"result"})

// Redis implements Cache.
type Redis struct {
	client *redis.Client
}

// NewRedis creates a pooled client. Timeouts are aggressive on purpose: a slow
// cache must degrade to the database, not stall the request.
func NewRedis(addr, password string, poolSize int, useTLS bool) *Redis {
	opts := &redis.Options{
		Addr:            addr,
		Password:        password,
		PoolSize:        poolSize,
		MinIdleConns:    min(8, poolSize),
		DialTimeout:     2 * time.Second,
		ReadTimeout:     250 * time.Millisecond,
		WriteTimeout:    250 * time.Millisecond,
		PoolTimeout:     500 * time.Millisecond,
		ConnMaxIdleTime: 5 * time.Minute,
	}
	if useTLS {
		// ElastiCache / managed Redis with in-transit encryption.
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Redis{client: redis.NewClient(opts)}
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := r.client.Get(ctx, key).Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		cacheOps.WithLabelValues("miss").Inc()
		return nil, false, nil
	case err != nil:
		cacheOps.WithLabelValues("error").Inc()
		return nil, false, err
	}
	cacheOps.WithLabelValues("hit").Inc()
	return b, true, nil
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return r.client.Set(ctx, key, val, ttl).Err()
}

func (r *Redis) Del(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

func (r *Redis) GetInt(ctx context.Context, key string) (int64, error) {
	n, err := r.client.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

// IncrBy applies many counter increments in one pipelined round trip.
func (r *Redis) IncrBy(ctx context.Context, counts map[string]int64) error {
	if len(counts) == 0 {
		return nil
	}
	_, err := r.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for k, v := range counts {
			p.IncrBy(ctx, k, v)
		}
		return nil
	})
	return err
}

func (r *Redis) Ping(ctx context.Context) error { return r.client.Ping(ctx).Err() }

func (r *Redis) Close() error { return r.client.Close() }
