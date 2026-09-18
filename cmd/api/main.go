// Command titan-api is the TitanEdge HTTP + gRPC API server.
//
//	titan-api            serve HTTP (:8080), gRPC (:9090) and metrics (:9102)
//	titan-api migrate    apply database migrations and exit
//	titan-api version    print build information
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/titanedge/titanedge/internal/api"
	"github.com/titanedge/titanedge/internal/buildinfo"
	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/config"
	"github.com/titanedge/titanedge/internal/events"
	"github.com/titanedge/titanedge/internal/grpcserver"
	"github.com/titanedge/titanedge/internal/observability"
	"github.com/titanedge/titanedge/internal/store"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("titan-api", buildinfo.String())
	case "migrate":
		os.Exit(migrate())
	case "", "serve":
		os.Exit(serve())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (use: serve | migrate | version)\n", cmd)
		os.Exit(2)
	}
}

func migrate() int {
	cfg, err := config.Load("titan-api")
	log := observability.NewLogger(cfg.LogLevel, cfg.ServiceName, cfg.PodName)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	if cfg.DatabaseURL == "" {
		log.Error("DATABASE_URL is required for migrate")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pg, err := waitForPostgres(ctx, log, cfg)
	if err != nil {
		log.Error("connect postgres", "err", err)
		return 1
	}
	defer pg.Close()
	if err := pg.Migrate(ctx, log); err != nil {
		log.Error("migrate", "err", err)
		return 1
	}
	log.Info("migrations complete")
	return 0
}

// waitForPostgres retries until the database accepts connections, which
// avoids crash loops while a StatefulSet is still starting.
func waitForPostgres(ctx context.Context, log *slog.Logger, cfg config.Config) (*store.Postgres, error) {
	for attempt := 1; ; attempt++ {
		pg, err := store.NewPostgres(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
		if err == nil {
			if err = pg.Ping(ctx); err == nil {
				return pg, nil
			}
			pg.Close()
		}
		log.Warn("postgres not ready", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(time.Duration(attempt)*time.Second, 10*time.Second)):
		}
	}
}

func serve() int {
	cfg, err := config.Load("titan-api")
	log := observability.NewLogger(cfg.LogLevel, cfg.ServiceName, cfg.PodName)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	slog.SetDefault(log)
	log.Info("starting titan-api", "version", buildinfo.String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, cfg.ServiceName, cfg.PodName, cfg.OTLPEndpoint, cfg.TraceSampleRatio)
	if err != nil {
		log.Error("tracing setup", "err", err)
		return 1
	}

	deps := api.Deps{Checks: map[string]func(context.Context) error{}}

	if cfg.DatabaseURL != "" {
		startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		pg, err := waitForPostgres(startCtx, log, cfg)
		cancel()
		if err != nil {
			log.Error("connect postgres", "err", err)
			return 1
		}
		defer pg.Close()
		if cfg.MigrateOnStart {
			if err := pg.Migrate(ctx, log); err != nil {
				log.Error("migrate", "err", err)
				return 1
			}
		}
		deps.Store = pg
		deps.Checks["postgres"] = pg.Ping
	}

	if cfg.RedisAddr != "" {
		rc := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisPoolSize, cfg.RedisTLS)
		defer rc.Close()
		deps.Cache = rc
		deps.Checks["redis"] = rc.Ping
	}

	if len(cfg.KafkaBrokers) > 0 {
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := events.EnsureTopic(tctx, cfg.KafkaBrokers, cfg.KafkaTopic, cfg.KafkaPartitions); err != nil {
			log.Warn("ensure topic failed; relying on broker auto-create", "err", err)
		}
		cancel()
		kp, err := events.NewKafka(cfg.KafkaBrokers, cfg.KafkaTopic, 250_000)
		if err != nil {
			log.Error("kafka producer", "err", err)
			return 1
		}
		defer kp.Close()
		deps.Publisher = kp
		deps.Checks["kafka"] = kp.Ping
	}

	srv := api.New(cfg, log, deps)

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	// Accept HTTP/1.1 and cleartext HTTP/2 (h2c) so Envoy and titanload can
	// multiplex thousands of streams over a handful of connections.
	httpSrv.Protocols = new(http.Protocols)
	httpSrv.Protocols.SetHTTP1(true)
	httpSrv.Protocols.SetUnencryptedHTTP2(true)

	metricsSrv := observability.NewMetricsServer(cfg.MetricsAddr, func() bool {
		ok, _ := srv.Ready(context.Background())
		return ok
	})

	grpcSrv, health := grpcserver.New()
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Error("grpc listen", "err", err)
		return 1
	}

	errCh := make(chan error, 3)
	go func() { errCh <- ignoreClosed(httpSrv.ListenAndServe()) }()
	go func() { errCh <- ignoreClosed(metricsSrv.ListenAndServe()) }()
	go func() { errCh <- grpcSrv.Serve(grpcLis) }()
	log.Info("listening", "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr, "metrics", cfg.MetricsAddr)

	exit := 0
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			log.Error("server failed", "err", err)
			exit = 1
		}
	}

	// Graceful drain. Kubernetes' preStop sleep has already removed us from
	// endpoints by the time SIGTERM arrives.
	health.Shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	stopped := make(chan struct{})
	go func() { grpcSrv.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-sctx.Done():
		grpcSrv.Stop()
	}
	_ = metricsSrv.Shutdown(sctx)
	_ = shutdownTracing(sctx)
	log.Info("shutdown complete")
	return exit
}

func ignoreClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
