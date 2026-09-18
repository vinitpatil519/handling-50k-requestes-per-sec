// Command titanload is TitanEdge's distributed HTTP/1.1, HTTP/2 and gRPC load
// generator with a live terminal dashboard.
//
//	titanload run -u http://localhost:8080/api/v1/ping -c 256 -d 30s
//	titanload run -u http://localhost:8080/api/v1/ping -r 50000 --ramp 10s -m http2
//	titanload run -u grpc://localhost:9090 -c 512 -d 1m
//	titanload coordinator --expect-workers 4 -u http://titan-nginx/api/v1/ping -r 200000 -d 5m
//	titanload worker --coordinator http://coordinator:7070
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/titanedge/titanedge/internal/buildinfo"
	"github.com/titanedge/titanedge/internal/titanload/dashboard"
	"github.com/titanedge/titanedge/internal/titanload/distributed"
	"github.com/titanedge/titanedge/internal/titanload/engine"
	"github.com/titanedge/titanedge/internal/titanload/report"
)

const usage = `titanload - distributed load generator for TitanEdge

Usage:
  titanload run [flags] [URL]           run a load test from this machine
  titanload coordinator [flags] [URL]   orchestrate remote workers and aggregate results
  titanload worker --coordinator URL    execute load on behalf of a coordinator
  titanload version

Run "titanload <command> -h" for the flags of a command.

Exit codes: 0 ok, 1 error, 2 SLO violated.
`

func main() { os.Exit(realMain(os.Args[1:])) }

func realMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "coordinator", "coord":
		return cmdCoordinator(args[1:])
	case "worker":
		return cmdWorker(args[1:])
	case "version", "--version", "-v":
		fmt.Println("titanload", buildinfo.String())
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	if strings.HasPrefix(args[0], "-") || strings.Contains(args[0], "://") {
		return cmdRun(args)
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
	return 1
}

// headerFlag collects repeated -H "Key: Value" flags.
type headerFlag http.Header

func (h headerFlag) String() string { return "" }
func (h headerFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("header %q must be \"Key: Value\"", v)
	}
	http.Header(h).Add(strings.TrimSpace(k), strings.TrimSpace(val))
	return nil
}

type loadFlags struct {
	url, targets, mode, method, body, bodyFile, grpcService string
	concurrency, connections                                int
	rate                                                    float64
	duration, timeout, ramp                                 time.Duration
	requests                                                uint64
	insecure, noKeepAlive                                   bool
	headers                                                 headerFlag

	noDashboard bool
	jsonOut     string
	metricsAddr string
	sloP95      time.Duration
	sloP99      time.Duration
	sloErrPct   float64
	sloMinRPS   float64
}

func bindLoadFlags(fs *flag.FlagSet) *loadFlags {
	f := &loadFlags{headers: headerFlag{}}
	str := func(p *string, names []string, def, help string) {
		for _, n := range names {
			fs.StringVar(p, n, def, help)
		}
	}
	str(&f.url, []string{"u", "url"}, "", "target URL (http://, https://, grpc://, grpcs://)")
	str(&f.targets, []string{"targets"}, "", `file of "METHOD URL [BODY|@file]" lines, used round-robin`)
	str(&f.mode, []string{"m", "mode"}, "", "protocol: http1 | http2 (h2c for http://) | grpc (default: from URL)")
	str(&f.method, []string{"X", "method"}, "GET", "HTTP method")
	str(&f.body, []string{"b", "body"}, "", "request body")
	str(&f.bodyFile, []string{"body-file"}, "", "read request body from file")
	str(&f.grpcService, []string{"grpc-service"}, "", "service name for grpc health checks")
	str(&f.jsonOut, []string{"json"}, "", "write JSON summary to file (- for stdout)")
	str(&f.metricsAddr, []string{"metrics-addr"}, "", "expose live Prometheus metrics, e.g. :9464")
	for _, n := range []string{"c", "concurrency"} {
		fs.IntVar(&f.concurrency, n, 64, "concurrent request goroutines (per worker in distributed mode)")
	}
	fs.IntVar(&f.connections, "connections", 0, "max connections per host (0 = one per goroutine; gRPC: channel count)")
	for _, n := range []string{"r", "rate"} {
		fs.Float64Var(&f.rate, n, 0, "target requests/second, open model (0 = as fast as possible)")
	}
	for _, n := range []string{"d", "duration"} {
		fs.DurationVar(&f.duration, n, 30*time.Second, "test duration (0 = until -n requests)")
	}
	for _, n := range []string{"n", "requests"} {
		fs.Uint64Var(&f.requests, n, 0, "stop after this many requests (0 = unlimited)")
	}
	for _, n := range []string{"t", "timeout"} {
		fs.DurationVar(&f.timeout, n, 5*time.Second, "per-request timeout")
	}
	fs.DurationVar(&f.ramp, "ramp", 0, "linearly ramp rate (or goroutines) up over this period")
	for _, n := range []string{"k", "insecure"} {
		fs.BoolVar(&f.insecure, n, false, "skip TLS certificate verification")
	}
	fs.BoolVar(&f.noKeepAlive, "no-keepalive", false, "open a new connection per request")
	fs.BoolVar(&f.noDashboard, "no-dashboard", false, "plain progress lines instead of the live dashboard")
	fs.Var(f.headers, "H", `request header "Key: Value" (repeatable)`)
	fs.DurationVar(&f.sloP95, "slo-p95", 0, "fail (exit 2) if overall p95 exceeds this")
	fs.DurationVar(&f.sloP99, "slo-p99", 0, "fail (exit 2) if overall p99 exceeds this")
	fs.Float64Var(&f.sloErrPct, "slo-error-rate", 0, "fail (exit 2) if error rate exceeds this percentage, e.g. 1 = 1%")
	fs.Float64Var(&f.sloMinRPS, "slo-min-rps", 0, "fail (exit 2) if achieved rps is below this")
	return f
}

func (f *loadFlags) config(fs *flag.FlagSet) (engine.Config, error) {
	if f.url == "" && fs.NArg() > 0 {
		f.url = fs.Arg(0)
	}
	mode, err := engine.ParseMode(f.mode)
	if err != nil {
		return engine.Config{}, err
	}
	if f.mode == "" && (strings.HasPrefix(f.url, "grpc://") || strings.HasPrefix(f.url, "grpcs://")) {
		mode = engine.ModeGRPC
	}
	body := []byte(f.body)
	if f.bodyFile != "" {
		if body, err = os.ReadFile(f.bodyFile); err != nil {
			return engine.Config{}, err
		}
	}
	var targets []engine.Target
	switch {
	case f.targets != "":
		if targets, err = engine.ParseTargetsFile(f.targets, http.Header(f.headers)); err != nil {
			return engine.Config{}, err
		}
	case f.url != "":
		targets = []engine.Target{{Method: f.method, URL: f.url, Body: body, Headers: http.Header(f.headers)}}
	default:
		return engine.Config{}, errors.New("a target is required: -u URL or --targets FILE")
	}
	cfg := engine.Config{
		Targets: targets, Mode: mode, Concurrency: f.concurrency, Connections: f.connections,
		Rate: f.rate, Duration: f.duration, MaxRequests: f.requests, Timeout: f.timeout, Ramp: f.ramp,
		Insecure: f.insecure, DisableKeepAlive: f.noKeepAlive, GRPCService: f.grpcService,
	}
	return cfg, cfg.Validate()
}

func (f *loadFlags) slo() report.SLO {
	return report.SLO{P95: f.sloP95, P99: f.sloP99, ErrorRate: f.sloErrPct / 100, MinRPS: f.sloMinRPS}
}

func (f *loadFlags) info(cfg engine.Config, workers int) report.RunInfo {
	t := cfg.Targets[0].Method + " " + cfg.Targets[0].URL
	if len(cfg.Targets) > 1 {
		t = fmt.Sprintf("%s (+%d more)", t, len(cfg.Targets)-1)
	}
	return report.RunInfo{Target: t, Mode: string(cfg.Mode), Workers: workers, Concurrency: cfg.Concurrency, Rate: cfg.Rate}
}

func serveMetrics(addr string, h http.Handler) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", h)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "metrics server:", err)
		}
	}()
}

func runID() string { return "run-" + time.Now().UTC().Format("20060102-150405") }

// finish prints the summary, writes JSON and maps SLO results to an exit code.
func finish(f *loadFlags, sum report.Summary) int {
	sum.WriteText(os.Stdout)
	if f.jsonOut != "" {
		if err := sum.WriteJSON(f.jsonOut); err != nil {
			fmt.Fprintln(os.Stderr, "write json:", err)
			return 1
		}
	}
	if !sum.SLOPassed {
		return 2
	}
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	f := bindLoadFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg, err := f.config(fs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	e, err := engine.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); stop() }() // a second Ctrl+C kills immediately

	exp := dashboard.NewExporter(runID())
	serveMetrics(f.metricsAddr, exp.Handler())

	cfg = e.Config()
	info := f.info(cfg, 1)
	go e.Run(ctx)
	dashboard.Run(context.Background(), engineSource{e}, dashboard.Options{
		Info: info, Duration: cfg.Duration, MaxRequests: cfg.MaxRequests, SLO: f.slo(),
		Interactive: !f.noDashboard && dashboard.IsTerminal(), Exporter: exp,
	})
	return finish(f, report.Build(e.Snapshot(), info, f.slo()))
}

type engineSource struct{ *engine.Engine }

func (engineSource) Workers() []dashboard.WorkerView { return nil }

func cmdCoordinator(args []string) int {
	fs := flag.NewFlagSet("coordinator", flag.ContinueOnError)
	f := bindLoadFlags(fs)
	listen := fs.String("listen", ":7070", "address for the worker API and /metrics")
	expect := fs.Int("expect-workers", 1, "number of workers to wait for before starting")
	token := fs.String("token", os.Getenv("TITANLOAD_TOKEN"), "shared secret workers must present (env TITANLOAD_TOKEN)")
	startDelay := fs.Duration("start-delay", 3*time.Second, "delay between job release and synchronized start")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg, err := f.config(fs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	interactive := !f.noDashboard && dashboard.IsTerminal()
	var logOut io.Writer = os.Stderr
	if interactive {
		logOut = io.Discard // the dashboard owns the screen
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))

	id := runID()
	exp := dashboard.NewExporter(id)
	coord, err := distributed.NewCoordinator(cfg, distributed.CoordinatorOptions{
		Expect: *expect, Token: *token, RunID: id, StartDelay: *startDelay, Metrics: exp.Handler(),
	}, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	srv, err := coord.Listen(*listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "coordinator listening on %s, waiting for %d worker(s)...\n", *listen, *expect)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); stop() }()

	runErr := make(chan error, 1)
	go func() { runErr <- coord.Run(ctx) }()

	info := f.info(cfg, *expect)
	dashboard.Run(ctx, coord, dashboard.Options{
		Info: info, Duration: cfg.Duration, MaxRequests: cfg.MaxRequests, SLO: f.slo(),
		Interactive: interactive, Exporter: exp,
	})
	if err := <-runErr; err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	info.Workers = coord.WorkerCount()
	return finish(f, report.Build(coord.Snapshot(), info, f.slo()))
}

func cmdWorker(args []string) int {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("TITANLOAD_COORDINATOR"), "coordinator URL, e.g. http://10.0.0.5:7070 (env TITANLOAD_COORDINATOR)")
	name := fs.String("name", os.Getenv("TITANLOAD_WORKER_NAME"), "worker name shown in the dashboard (default hostname)")
	token := fs.String("token", os.Getenv("TITANLOAD_TOKEN"), "shared secret (env TITANLOAD_TOKEN)")
	loop := fs.Bool("loop", false, "stay registered for subsequent runs (daemon mode)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *coordinator == "" {
		fmt.Fprintln(os.Stderr, "error: --coordinator is required")
		return 1
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := distributed.RunWorker(ctx, distributed.WorkerOptions{
		Coordinator: *coordinator, Name: *name, Token: *token, Loop: *loop,
	}, log); err != nil {
		log.Error("worker failed", "err", err)
		return 1
	}
	return 0
}
