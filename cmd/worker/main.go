// Command titan-worker consumes titan.events from Kafka and persists
// aggregates. KEDA scales it on consumer-group lag.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/titanedge/titanedge/internal/buildinfo"
	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/config"
	"github.com/titanedge/titanedge/internal/observability"
	"github.com/titanedge/titanedge/internal/store"
	"github.com/titanedge/titanedge/internal/worker"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("titan-worker", buildinfo.String())
		return
	}
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load("titan-worker")
	log := observability.NewLogger(cfg.LogLevel, cfg.ServiceName, cfg.PodName)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	slog.SetDefault(log)
	if len(cfg.KafkaBrokers) == 0 {
		log.Error("KAFKA_BROKERS is required")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, cfg.ServiceName, cfg.PodName, cfg.OTLPEndpoint, cfg.TraceSampleRatio)
	if err != nil {
		log.Error("tracing setup", "err", err)
		return 1
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	var sink worker.Sink
	if cfg.DatabaseURL != "" {
		pg, err := store.NewPostgres(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
		if err != nil {
			log.Error("postgres", "err", err)
			return 1
		}
		defer pg.Close()
		sink.Store = pg
	}
	if cfg.RedisAddr != "" {
		rc := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, 16, cfg.RedisTLS)
		defer rc.Close()
		sink.Cache = rc
	}

	consumer, err := worker.NewConsumer(cfg, log, sink)
	if err != nil {
		log.Error("kafka consumer", "err", err)
		return 1
	}

	metricsSrv := observability.NewMetricsServer(cfg.MetricsAddr, func() bool {
		pctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return consumer.Ping(pctx) == nil
	})
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()

	log.Info("worker started", "topic", cfg.KafkaTopic, "group", cfg.KafkaGroupID, "version", buildinfo.Version)
	if err := consumer.Run(ctx); err != nil {
		log.Error("consumer", "err", err)
		return 1
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(sctx)
	return 0
}
