# Scaling toward 1,000,000 requests/second

A laptop running Kind tops out somewhere between 10k and 50k requests/second end-to-end. That ceiling is Docker Desktop NAT, a few shared CPU cores and the fact that the load generator competes with the system under test - not the architecture. This page shows what 1M RPS actually takes and how TitanEdge's pieces scale to it.

## 1. Measure the per-core cost of each hop

Before multiplying anything, measure. Every number below is a starting assumption to replace with your own measurements:

```bash
# API alone (no edge): what one pod does per core
kubectl -n titanedge port-forward deploy/titan-api 8081:8080
titanload run -u http://localhost:8081/api/v1/ping -c 256 -d 60s

# Through Envoy only, then through NGINX -> Envoy
kubectl -n titanedge port-forward svc/titan-envoy 8082:8080
titanload run -u http://localhost:8082/api/v1/ping -c 256 -d 60s
titanload run -u http://localhost:8080/api/v1/ping -c 256 -d 60s
```

Divide achieved RPS by CPU used (`sum(rate(container_cpu_usage_seconds_total{pod=~"titan-api.*"}[1m]))` in Prometheus). Typical orders of magnitude on modern x86 cores with keep-alive:

| Hop | Work | RPS per vCPU (typical) | vCPU for 1M RPS |
|---|---|---|---|
| NGINX | L7 proxy, keep-alive upstream | 40-60k | ~20 |
| Envoy edge | L7 proxy, rate limit, stats, per-request LB | 20-30k | ~40 |
| Go API `/ping` | tiny JSON write | 30-50k | ~25 |
| Go API `/items/{id}` (Redis hit) | + Redis round trip + JSON | 10-20k | ~70 |
| Istio sidecar | per proxy, per direction | ~5k (Istio reports ≈0.2 vCPU per 1k RPS) | **~200 per hop** |
| titanload | generator | 20-40k | ~35 |

The mesh is the dominant cost at this scale. Two sidecar hops (Envoy → API) on a 1M RPS path cost several hundred vCPUs. Options, in order of preference:

1. **Keep mTLS, drop the sidecars**: Istio ambient mode (ztunnel does L4 mTLS per node for a fraction of the CPU).
2. Exclude the hot path from injection and rely on NetworkPolicies + Envoy-to-API TLS.
3. Accept the cost for the security properties and budget for it explicitly.

## 2. Size the fleet (read path, 1M RPS, 99% cache hits)

| Tier | Replicas | Per pod | Node pool |
|---|---|---|---|
| NLB | 1 (cross-zone) | - | managed |
| NGINX | 12 | 2 vCPU | `app` (c7i.2xlarge) |
| Envoy | 24 | 2 vCPU, `concurrency: 2` | `app` |
| API | 80 | 1 vCPU, `MAX_INFLIGHT` 50k, `DB_MAX_CONNS` 8 | `app` |
| Redis | 6-8 shards (cluster mode) or ElastiCache r7g.xlarge ×2 + client-side L1 cache | - | managed |
| PostgreSQL | 1 writer + read replicas; cache absorbs >99% of reads | - | RDS r7g.2xlarge |
| titanload | 12 workers | 16 vCPU each | `loadgen` (c7i.4xlarge, Spot, tainted) |

≈ 230 vCPU of application tier → ~30 `c7i.2xlarge` nodes plus 12 load generator nodes. `environments/eks/values.yaml` sets `maxReplicas` high enough; the node groups in `infra/terraform/envs/eks` allow `app_max_nodes = 30` by default - raise it.

Write path at 1M events/s (~500 B each ≈ 500 MB/s): Kafka with 3+ brokers, 48-96 partitions, `linger.ms` batching (already 5ms in the producer), snappy/lz4 compression, and a worker fleet of up to one consumer per partition. The worker's aggregation turns 1M events/s into a few thousand database upserts/s.

## 3. Remove the non-CPU bottlenecks

**Connections and ports**

- Keep-alive everywhere (it is in every hop's config). A new TCP connection per request caps a client IP at ~28k-64k connections/minute because of `TIME_WAIT`.
- `net.ipv4.ip_local_port_range = 1024 65535`, `tcp_tw_reuse = 1`, `somaxconn = 65535` (the `kernel_tuning` role).
- File descriptors ≥ 1M on load generators and edge nodes.
- `nf_conntrack_max` large enough for all flows through kube-proxy (or run Cilium kube-proxy-free).

**Load balancing**

- HTTP/2 connections are long-lived. Without per-request balancing, a scale-out adds pods that receive nothing. Envoy + the headless Service + `STRICT_DNS` + `LEAST_REQUEST` fixes this; the gRPC server sets `MaxConnectionAge` for the same reason.
- NLB `cross-zone-load-balancing-enabled` so a single-AZ client fleet does not overload one zone.

**Pod density (EKS)**

- Enable VPC CNI prefix delegation (`ENABLE_PREFIX_DELEGATION=true`) so a c7i.2xlarge can run 100+ pods instead of 58.

**Observability overhead**

- Access logs sampled at 1% plus all 5xx (already configured).
- Trace sampling 1% or lower; tail sampling in the OTel Collector if you need all errors.
- Prometheus: keep label cardinality bounded (`route`, never raw path).

**Autoscaling reaction time**

- HPA scale-up policy doubles replicas every 15s; KEDA polls every 10s. Cluster Autoscaler / Karpenter needs ~60-90s for new nodes: pre-warm (`minReplicas`, over-provisioning pause pods) before step tests.

## 4. Run the test like an SRE

1. **Baseline** - 1 API pod, find its knee: increase `-r` until p99 bends.
2. **Linear check** - 2, 4, 8 pods: RPS should scale ~linearly. If not, find the shared bottleneck (Envoy, Redis, node network).
3. **Step test** - titanload distributed with `--ramp` to 25%, 50%, 75%, 100% of target, holding each step 5 minutes.
4. **Soak** - 1-2 hours at 70% to catch leaks and GC drift.
5. **Chaos** - kill pods / a node mid-test; SLOs should hold thanks to PDBs, outlier detection and retries.

```bash
titanload coordinator --expect-workers 12 --token "$T" \
  -u http://<nlb>/api/v1/items/1 -r 1000000 --ramp 10m -d 30m -c 2048 \
  --slo-p99 50ms --slo-error-rate 0.1 --json 1m-rps.json
```

## 5. Cost of a 1M RPS hour on AWS (rough, us-east-1 on-demand)

| Item | Count | $/h |
|---|---|---|
| EKS control plane | 1 | 0.10 |
| c7i.2xlarge (app) | 30 | ~10.7 |
| c7i.4xlarge (loadgen, Spot ≈ -65%) | 12 | ~2.5 |
| m7i.xlarge (system) | 3 | ~0.6 |
| RDS r7g.2xlarge Multi-AZ | 1 | ~1.7 |
| ElastiCache r7g.xlarge ×2 | 2 | ~0.9 |
| NLB + NAT + data transfer (in-VPC) | - | ~1-3 |
| **Total** | | **≈ $17-20 / hour** |

Run the test, export the Grafana dashboards and `titanload --json` report, then `terraform destroy`.
