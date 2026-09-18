# Observability

| Signal | Producer | Transport | Store | UI |
|---|---|---|---|---|
| Metrics | Go services (`:9102`), Envoy (`:9901/stats/prometheus`), nginx-prometheus-exporter (`:9113`), titanload (`/metrics`), kube-state-metrics, node-exporter, KEDA | ServiceMonitors | Prometheus | Grafana |
| Logs | JSON on stdout from every component | Fluent Bit DaemonSet (tail + kubernetes filter) | Loki | Grafana Explore / *Logs* dashboard |
| Traces | Go services (OTel SDK, `otelhttp`), Envoy (OpenTelemetry tracer) | OTLP gRPC → OpenTelemetry Collector | Jaeger | Jaeger UI, Grafana |
| Span metrics | OTel Collector `spanmetrics` connector | Prometheus scrape | Prometheus | Grafana |

## Dashboards

Generated from [tools/dashboards/generate.mjs](../tools/dashboards/generate.mjs) into [charts/platform/dashboards](../charts/platform/dashboards) and shipped as ConfigMaps labelled `grafana_dashboard: "1"` (Kubernetes) or file-provisioned (compose).

| Dashboard | Highlights |
|---|---|
| TitanEdge / Overview | RPS, 5xx ratio, p50-p99.9, status codes, in-flight vs shed, Redis hit/miss, PostgreSQL p99 per query, Kafka produce results, replicas, CPU/memory per pod |
| TitanEdge / Edge | NGINX req/s & connections; Envoy downstream rate, upstream latency, 5xx by class, healthy endpoints, retries, ejections, circuit breakers, rate-limited requests |
| TitanEdge / Event Pipeline | produced vs consumed, KEDA lag vs worker replicas, producer backpressure, flush latency, batch size |
| TitanEdge / TitanLoad | client RPS vs server RPS, client latency quantiles, errors, worker count, API replicas during the test |
| TitanEdge / Logs | log volume by level, errors/warnings, full-text search |

## Key metrics

| Metric | Type | Labels |
|---|---|---|
| `titan_http_requests_total` | counter | route, method, code |
| `titan_http_request_duration_seconds` | histogram | route, method |
| `titan_http_inflight_requests` | gauge | |
| `titan_http_shed_total` | counter | |
| `titan_cache_requests_total` | counter | result (hit, miss, error) |
| `titan_db_query_duration_seconds` | histogram | op, result |
| `titan_kafka_published_total` | counter | topic, result (ok, error, backpressure) |
| `titan_worker_events_total` | counter | result (ok, invalid) |
| `titan_worker_flush_duration_seconds` | histogram | |
| `titan_worker_batch_events` | histogram | |
| `titanload_rps`, `titanload_latency_seconds{quantile}`, `titanload_error_ratio` | gauge | run_id |

`route` is the registered route name (`items_get`), never the raw path, so cardinality stays bounded no matter how many item IDs are requested.

## Recording rules and alerts

[charts/platform/files/prometheus-rules.yaml](../charts/platform/files/prometheus-rules.yaml) - used by the PrometheusRule in Kubernetes and loaded directly by Prometheus in compose.

| Alert | Condition | Severity |
|---|---|---|
| TitanAPIHighErrorRate | 5xx ratio > 1% for 5m | critical |
| TitanAPIHighLatencyP99 | p99 > 500ms for 10m | warning |
| TitanAPILoadShedding | shed > 1 req/s for 5m | warning |
| TitanAPIDown | no API target up for 3m | critical |
| TitanKafkaBackpressure | producer errors/backpressure for 5m | warning |
| TitanWorkerFlushFailing | > 3 failed flushes in 10m | warning |
| TitanConsumerLagHigh | lag > 100k for 10m | warning |
| TitanHPAMaxedOut | HPA at max replicas for 15m | warning |
| TitanEnvoyUpstreamErrors | > 2% upstream 5xx for 5m | warning |

Each alert links to its section in the [runbook](runbook.md).

## Useful queries

```promql
# RPS by route
sum by (route) (rate(titan_http_requests_total[1m]))

# p99 latency
histogram_quantile(0.99, sum by (le) (rate(titan_http_request_duration_seconds_bucket[5m])))

# Requests per API pod (is Envoy balancing evenly?)
sum by (pod) (rate(titan_http_requests_total[1m]))

# Cache hit ratio
titan:cache_hit:ratio_rate5m
```

```logql
# API errors with their request ids
{namespace="titanedge", app="api"} | json | level="ERROR"

# Slow requests seen by Envoy
{namespace="titanedge", app="envoy"} | json | duration_ms > 250
```

## Log format

```json
{"time":"2026-09-18T21:30:00.123Z","level":"INFO","msg":"http request","service":"titan-api",
 "pod":"titan-api-7c9f-abcde","version":"42-1a2b3c4d","route":"items_get","method":"GET",
 "path":"/api/v1/items/42","status":200,"bytes":112,"duration_ms":1.7,"request_id":"8f14e45f..."}
```

The same `X-Request-ID` is generated at NGINX, preserved by Envoy and logged by the API, so one ID follows a request through all three tiers.
