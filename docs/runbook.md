# Runbook

Every alert in [prometheus-rules.yaml](../charts/platform/files/prometheus-rules.yaml) links here. Start with the **TitanEdge / Overview** dashboard, then follow the section.

Handy commands:

```bash
kubectl -n titanedge get pods,hpa,scaledobjects -o wide
kubectl -n titanedge top pods
kubectl -n titanedge logs deploy/titan-api --tail=100 | jq -c 'select(.level!="INFO")'
kubectl -n titanedge port-forward deploy/titan-envoy 9901   # Envoy admin: /clusters, /stats
```

## TitanAPIHighErrorRate

**Meaning:** more than 1% of API responses are 5xx for 5 minutes.

1. Overview → *Responses by status code*: is it 503 (shedding / backpressure), 500 (dependency), or 502/504 (edge)?
2. 500s: check *PostgreSQL p99 by operation* and `kubectl logs` for `database error`. Is Postgres up? `kubectl -n titanedge get pod titan-postgres-0`.
3. 503s with `overloaded`: see TitanAPILoadShedding. With `event bus saturated`: see TitanKafkaBackpressure.
4. Recent deploy? `helm history titan -n titanedge` / Argo CD history → roll back.

## TitanAPIHighLatencyP99

1. Compare *Latency* with *In-flight*: rising in-flight = saturation → scale out (`kubectl -n titanedge scale` is overridden by HPA; raise `minReplicas`).
2. Check Redis hit ratio - a cold or evicted cache pushes load onto PostgreSQL.
3. CPU throttling: `container_cpu_cfs_throttled_periods_total` for API pods → raise CPU limits.
4. Jaeger: open a slow trace, see which span dominates.

## TitanAPILoadShedding

Pods are at `MAX_INFLIGHT`. This protects latency, but clients see 503.

1. Is the HPA/KEDA at max? (TitanHPAMaxedOut) → raise `maxReplicas` or add nodes.
2. Is a dependency slow, so requests pile up? Fix that first - more pods will not help.
3. Only raise `MAX_INFLIGHT` if CPU and memory have headroom.

## TitanAPIDown

1. `kubectl -n titanedge get pods -l app.kubernetes.io/name=api` - CrashLoopBackOff? `kubectl logs --previous`.
2. Config error (`invalid configuration`)? Check values / secrets.
3. Image pull error on Kind? Re-run `scripts/build-images.sh` and ensure the registry container is running (`docker ps | grep kind-registry`).
4. Prometheus side: is the ServiceMonitor present and the target `UP` (Prometheus → Status → Targets)?

## TitanKafkaBackpressure

The producer's buffer (250k records) is full or produce requests fail.

1. `kubectl -n titanedge get pods -l app.kubernetes.io/name=kafka` and logs.
2. Disk full? `kubectl -n titanedge exec titan-kafka-0 -- df -h /var/lib/kafka/data`.
3. Throughput limit → more partitions/brokers (`kafka.replicas`, `kafka.partitions`).

## TitanWorkerFlushFailing

Workers cannot write rollups to PostgreSQL; they keep the batch and retry, offsets are not committed (no data loss, lag grows).

1. Worker logs: `flush failed`.
2. PostgreSQL connections exhausted? `max_connections` vs `api replicas × DB_MAX_CONNS + workers × 4`.

## TitanConsumerLagHigh

1. Event Pipeline dashboard: is *consumed* flat while *produced* is high?
2. Workers at `maxReplicaCount`? It is capped by partition count - increase partitions (new topic or `kafka-topics.sh --alter`) and `worker.autoscaling.maxReplicas`.
3. Workers healthy but slow? Check flush latency (PostgreSQL).

## TitanHPAMaxedOut

Capacity ceiling reached for 15 minutes.

1. Raise `maxReplicas` in `environments/<env>/values.yaml` (GitOps commit).
2. Pending pods? `kubectl get pods --field-selector=status.phase=Pending -A` → add nodes (Kind: more workers in `kind/cluster.yaml`; EKS: node group `max_size`).

## TitanEnvoyUpstreamErrors

1. Envoy admin `/clusters`: are API hosts marked unhealthy / ejected?
2. Circuit breakers open (Edge dashboard)? Raise `envoy.circuitBreaker.*` only if the API has headroom.
3. Correlate with API pod restarts or rollouts (`maxUnavailable: 0` should prevent drops; check `preStopSleepSeconds`).
