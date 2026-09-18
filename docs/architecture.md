# Architecture

TitanEdge is a read-heavy and write-heavy HTTP service behind a two-tier edge, with an asynchronous event pipeline, full observability and GitOps delivery. Every component is horizontally scalable; state lives only in PostgreSQL, Redis and Kafka.

## 1. System context

```mermaid
flowchart TB
    users([Users / browsers])
    load([titanload workers<br/>laptop · pods · VMs])
    dev([Developers])

    subgraph platform[TitanEdge platform]
      edge[Edge tier<br/>NGINX + Envoy]
      app[Application tier<br/>Go API · Next.js · Go worker]
      data[Data tier<br/>PostgreSQL · Redis · Kafka]
      obs[Observability<br/>Prometheus · Grafana · Loki · Jaeger · OTel]
    end

    ci[Jenkins]
    git[(Git repository)]
    cd[Argo CD]

    users --> edge
    load --> edge
    edge --> app --> data
    app -. metrics/logs/traces .-> obs
    dev -->|push| git --> ci
    ci -->|image tag commit| git
    git --> cd -->|sync| platform
```

## 2. Request path (read, cache-aside)

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant N as NGINX
    participant E as Envoy
    participant A as Go API
    participant R as Redis
    participant P as PostgreSQL

    C->>N: GET /api/v1/items/42 (HTTP/1.1 or h2c)
    N->>E: keep-alive upstream pool, X-Request-ID
    Note over E: local rate limit · circuit breaker<br/>least-request over pod IPs (STRICT_DNS)
    E->>A: HTTP/2 (h2c), retry only on connect failure
    Note over A: admission control (MAX_INFLIGHT)<br/>otelhttp span · RED metrics
    A->>R: GET titan:item:42 (250ms timeout)
    alt cache hit
        R-->>A: JSON
        A-->>E: 200 X-Cache: HIT
    else miss or Redis error
        A->>P: SELECT ... WHERE id = 42 (prepared)
        P-->>A: row
        A->>R: SET titan:item:42 EX 60 (detached ctx)
        A-->>E: 200 X-Cache: MISS
    end
    E-->>N: response
    N-->>C: response (+X-Request-ID)
```

Design points:

- **Two proxy tiers, different jobs.** NGINX terminates client connections cheaply (hundreds of thousands of idle keep-alives, gzip, static caching of Next.js assets). Envoy does L7 traffic management: per-request load balancing across pod IPs (critical for HTTP/2, where kube-proxy would pin a whole connection to one pod), outlier ejection, retry budgets, circuit breakers and local rate limiting.
- **Admission control in the API.** A non-blocking semaphore returns `503 Retry-After: 1` above `MAX_INFLIGHT`. Under overload the service stays fast for admitted requests instead of timing everyone out, and `titan_http_shed_total` tells the autoscaler story.
- **Degrade, don't fail.** A slow or dead Redis falls through to PostgreSQL. `/api/v1/ping` has no dependencies and keeps working when all backends are down.

## 3. Write path (events)

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant A as Go API
    participant K as Kafka (titan.events, 12+ partitions)
    participant W as Worker (consumer group)
    participant P as PostgreSQL
    participant R as Redis

    C->>A: POST /api/v1/events {type, data}
    A->>A: validate, envelope {id, type, source, ts}
    A->>K: TryProduce (async, 5ms linger, snappy, acks=leader)
    A-->>C: 202 Accepted
    Note over A,K: buffer full → 503 (backpressure) instead of unbounded memory
    loop every 2s or 5,000 events
        W->>K: PollRecords
        W->>W: aggregate per (minute, type)
        W->>P: UPSERT event_rollups (one round trip, unnest)
        W->>R: pipelined INCRBY counters
        W->>K: commit offsets
    end
```

Delivery is **at-least-once**: offsets are committed only after the batch is persisted, and consumer-group rebalances are blocked until the pending batch is flushed, so a scale-in by KEDA does not double-count.

## 4. Kubernetes topology

```mermaid
flowchart LR
    subgraph host[Laptop / EKS]
      subgraph cp[control-plane node]
        np[NodePorts 30080 · 30300 · 30090 · 30686 · 30443]
      end
      subgraph ns1[ns titanedge  · PSA baseline · default-deny NetworkPolicy · Istio STRICT mTLS]
        nginx[Deployment nginx<br/>HPA 2-8 · PDB]
        envoy[Deployment envoy<br/>HPA 2-8 · PDB]
        web[Deployment web<br/>HPA 2-6 · PDB]
        api[Deployment api<br/>HPA or KEDA 2-10 · PDB]
        worker[Deployment worker<br/>KEDA 1-12 · PDB]
        pg[(StatefulSet postgres<br/>PVC)]
        redis[(Deployment redis)]
        kafka[(StatefulSet kafka KRaft<br/>PVC)]
        job[Job migrate<br/>Helm/Argo hook]
      end
      subgraph ns2[ns monitoring]
        kps[kube-prometheus-stack]
      end
      subgraph ns3[ns observability]
        loki[Loki]
        fb[Fluent Bit DaemonSet]
        otel[OTel Collector]
        jaeger[Jaeger]
      end
      subgraph ns4[ns keda]
        keda[KEDA operator + metrics server]
      end
      subgraph ns5[ns titanload]
        coord[Job coordinator] --> tw[Deployment workers]
      end
    end
    np --> nginx --> envoy --> api
    nginx --> web
    api --> pg & redis & kafka
    worker --> kafka
    tw --> nginx
```

## 5. Autoscaling loop

```mermaid
flowchart LR
    load[titanload] -->|RPS ↑| api
    api -->|titan_http_requests_total| prom[Prometheus]
    api -->|CPU| ms[metrics-server]
    ms --> hpa[HPA api]
    prom --> kedaAPI[KEDA ScaledObject api<br/>rps/pod threshold]
    api -->|events| kafka[(Kafka)]
    kafka -->|consumer lag| kedaW[KEDA ScaledObject worker<br/>lagThreshold 1000]
    hpa -->|replicas| api
    kedaAPI -->|replicas| api
    kedaW -->|replicas ≤ partitions| worker[worker]
    worker -->|drains lag| kafka
```

`api.autoscaling.type` selects `hpa` (CPU, default, works with plain metrics-server) or `keda` (requests/second per pod from Prometheus, plus a CPU trigger). Workers always scale on Kafka lag when KEDA is installed and fall back to a CPU HPA when it is not; `maxReplicaCount` is capped at the partition count because extra consumers would sit idle.

## 6. Observability data flow

```mermaid
flowchart LR
    subgraph pods[Workloads]
      api[api :9102/metrics]
      worker[worker :9102/metrics]
      envoy[envoy :9901/stats/prometheus]
      nginx[nginx-exporter :9113]
      tl[titanload coordinator /metrics]
    end
    pods -->|ServiceMonitors| prom[Prometheus]
    prom -->|rules| am[Alertmanager]
    prom --> graf[Grafana]
    stdout[(container stdout<br/>JSON logs)] --> fb[Fluent Bit] --> loki[Loki] --> graf
    api -->|OTLP gRPC| otel[OTel Collector]
    envoy -->|OTLP gRPC| otel
    otel -->|traces| jaeger[Jaeger] --> graf
    otel -->|span metrics| prom
```

- Metrics are served on a **separate port (9102)**, so the edge can never expose them and Istio can mark only that port PERMISSIVE for Prometheus while the application port stays STRICT mTLS.
- Logs are JSON (`log/slog`, NGINX `escape=json`, Envoy `json_format`) and **sampled**: all 5xx plus 1% of the rest. Full access logs at 100k RPS would cost more CPU than serving the traffic.
- Traces are head-sampled at 1% (`TRACE_SAMPLE_RATIO`), parent-based so an upstream decision is honoured. Loki's derived field links a `trace_id` in a log line to Jaeger.

## 7. Delivery pipeline

```mermaid
flowchart LR
    dev([git push]) --> j1
    subgraph jenkins[Jenkins - ephemeral Kubernetes agent pod]
      j1[Verify<br/>go vet/test · tsc/next build · helm lint/template · terraform validate] --> j2[Build<br/>rootless BuildKit ×4 images]
      j2 --> j3[Scan<br/>Trivy HIGH/CRITICAL]
      j3 --> j4[Promote<br/>yq set global.imageTag · git push]
      j4 --> j6[Performance gate<br/>titanload p99 + error-rate SLO]
    end
    j4 --> git[(Git)]
    git --> argo[Argo CD<br/>app-of-apps, sync waves]
    argo --> cluster[(Cluster)]
    argo -->|healthy| j6
```

Argo CD sync waves order the cluster build-out: namespaces (-30) → metrics-server (-20) → Prometheus CRDs / KEDA (-15) → Loki (-10) → Fluent Bit, OTel, Jaeger (-5) → platform (0) → migration hook (1).

## 8. Failure modes

| Failure | Behaviour |
|---|---|
| API pod dies | Readiness removes it; Envoy health checks + outlier detection eject it within seconds; PDB keeps ≥1 during voluntary disruption |
| Redis down | Reads fall back to PostgreSQL (`titan_cache_requests_total{result="error"}`), `/readyz` fails so the pod is flagged |
| PostgreSQL down | Item routes return 500/503; ping and events keep working; worker retains its batch and retries without committing offsets |
| Kafka down / slow | Producer buffers up to 250k records then returns 503 (`TitanKafkaBackpressure` alert) |
| Traffic spike | Envoy local rate limit → API admission control → HPA/KEDA scale-out; shed requests are fast 503s, not timeouts |
| Node drain | PDBs + `maxUnavailable: 0` rolling updates + `preStop` sleep + graceful HTTP/gRPC shutdown |
