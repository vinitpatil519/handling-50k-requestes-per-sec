// Package config loads service configuration from environment variables.
// Every setting has a safe default so a bare binary starts in "edge only"
// mode (no PostgreSQL / Redis / Kafka) which is handy for raw RPS benchmarks.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServiceName string
	PodName     string

	HTTPAddr    string
	GRPCAddr    string
	MetricsAddr string

	DatabaseURL    string
	DBMaxConns     int32
	MigrateOnStart bool

	RedisAddr     string
	RedisPassword string
	RedisTLS      bool
	RedisPoolSize int
	CacheTTL      time.Duration

	KafkaBrokers    []string
	KafkaTopic      string
	KafkaPartitions int
	KafkaGroupID    string

	MaxInflight     int
	MaxBodyBytes    int64
	ShutdownTimeout time.Duration

	LogLevel      string
	LogSampleRate float64

	OTLPEndpoint     string
	TraceSampleRatio float64

	// Worker specific.
	WorkerFlushInterval time.Duration
	WorkerFlushSize     int
}

// Load reads the configuration from the process environment.
func Load(serviceName string) (Config, error) {
	c := Config{
		ServiceName:         env("OTEL_SERVICE_NAME", serviceName),
		PodName:             env("POD_NAME", hostname()),
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		GRPCAddr:            env("GRPC_ADDR", ":9090"),
		MetricsAddr:         env("METRICS_ADDR", ":9102"),
		DatabaseURL:         env("DATABASE_URL", ""),
		RedisAddr:           env("REDIS_ADDR", ""),
		RedisPassword:       env("REDIS_PASSWORD", ""),
		KafkaTopic:          env("KAFKA_TOPIC", "titan.events"),
		KafkaGroupID:        env("KAFKA_GROUP_ID", "titan-worker"),
		LogLevel:            env("LOG_LEVEL", "info"),
		OTLPEndpoint:        env("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		KafkaBrokers:        splitList(env("KAFKA_BROKERS", "")),
		MaxBodyBytes:        1 << 20,
		WorkerFlushInterval: 2 * time.Second,
	}

	var errs []string
	must := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	dbMax, err := envInt("DB_MAX_CONNS", 10)
	must(err)
	c.DBMaxConns = int32(dbMax)
	c.RedisPoolSize, err = envInt("REDIS_POOL_SIZE", 64)
	must(err)
	c.KafkaPartitions, err = envInt("KAFKA_PARTITIONS", 12)
	must(err)
	c.MaxInflight, err = envInt("MAX_INFLIGHT", 20000)
	must(err)
	c.WorkerFlushSize, err = envInt("WORKER_FLUSH_SIZE", 5000)
	must(err)
	c.MigrateOnStart, err = envBool("MIGRATE_ON_START", false)
	must(err)
	c.RedisTLS, err = envBool("REDIS_TLS", false)
	must(err)
	c.CacheTTL, err = envDuration("CACHE_TTL", 60*time.Second)
	must(err)
	c.ShutdownTimeout, err = envDuration("SHUTDOWN_TIMEOUT", 20*time.Second)
	must(err)
	c.WorkerFlushInterval, err = envDuration("WORKER_FLUSH_INTERVAL", 2*time.Second)
	must(err)
	c.LogSampleRate, err = envFloat("LOG_SAMPLE_RATE", 0.001)
	must(err)
	c.TraceSampleRatio, err = envFloat("TRACE_SAMPLE_RATIO", 0.01)
	must(err)

	if c.MaxInflight < 1 {
		errs = append(errs, "MAX_INFLIGHT must be >= 1")
	}
	if c.KafkaPartitions < 1 {
		errs = append(errs, "KAFKA_PARTITIONS must be >= 1")
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

func envBool(key string, def bool) (bool, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
