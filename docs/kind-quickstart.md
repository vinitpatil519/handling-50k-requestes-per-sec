# Kind guide

Everything below is free and runs on one machine.

## Profiles

| Profile | Installs | Docker RAM |
|---|---|---|
| `lite` (default) | metrics-server, kube-prometheus-stack (Prometheus, Alertmanager, Grafana, node-exporter, kube-state-metrics), Loki, Fluent Bit, OpenTelemetry Collector, Jaeger, KEDA, TitanEdge | ~6-8 GB |
| `full` | `lite` + Istio (STRICT mTLS, sidecars on the app tier), Argo CD, Jenkins; the API autoscales with KEDA on RPS | ~12-16 GB |

## Step by step

```bash
scripts/kind-up.sh
```

Creates `kind-titan` from [kind/cluster.yaml](../kind/cluster.yaml): one control-plane and three workers (two `titan/pool=app`, one `titan/pool=data`), maps NodePorts to host ports, starts a `registry:2` container at `localhost:5001` and wires containerd on every node to it.

```bash
scripts/build-images.sh --tag dev
```

Builds `api`, `worker`, `titanload` (one multi-stage [go.Dockerfile](../build/go.Dockerfile), distroless, non-root) and `web` (Next.js standalone), and pushes them to `localhost:5001/titanedge/*:dev`.

```bash
scripts/install-addons.sh --profile lite
```

Installs the pinned chart versions from [addons/versions.env](../addons/versions.env) with the values in [addons/values](../addons/values).

```bash
helm install titan ./charts/platform -n titanedge --create-namespace -f environments/kind/values.yaml
# or: scripts/deploy.sh --profile lite   (adds --wait and `helm test`)
```

The chart detects what the cluster offers (`.Capabilities`): ServiceMonitors/PrometheusRules/dashboards only when the Prometheus Operator CRDs exist, KEDA ScaledObjects only when KEDA exists (else CPU HPAs), Istio policies only when Istio exists. A bare `helm install` into an empty Kind cluster therefore works too.

## Verify

```bash
kubectl get pods -A
kubectl -n titanedge get deploy,sts,svc,hpa,pdb,networkpolicy
kubectl -n titanedge get scaledobjects
helm test titan -n titanedge --logs
scripts/smoke.sh
```

Expected `kubectl -n titanedge get pods` (lite):

```
titan-api-...        1/1 Running     (×2)
titan-envoy-...      1/1 Running     (×2)
titan-kafka-0        1/1 Running
titan-nginx-...      2/2 Running     (nginx + exporter, ×2)
titan-postgres-0     1/1 Running
titan-redis-...      1/1 Running
titan-web-...        1/1 Running     (×2)
titan-worker-...     1/1 Running
titan-api-migrate-1  0/1 Completed
```

With `full`, app-tier pods show one extra container (`istio-proxy`).

## Watch it scale

Terminal 1:

```bash
kubectl -n titanedge get hpa,scaledobjects -w
```

Terminal 2:

```bash
go run ./cmd/titanload run -u http://localhost:8080/api/v1/ping -c 512 -d 5m --ramp 60s
```

Terminal 3 - write path, drives Kafka lag and the worker ScaledObject:

```bash
go run ./cmd/titanload run -X POST -b '{"type":"load.test","data":{"n":1}}' \
  -u http://localhost:8080/api/v1/events -c 256 -d 5m
```

Grafana → Dashboards → *TitanEdge*: **Overview**, **Edge**, **Event Pipeline**, **TitanLoad**, **Logs**.

## Distributed load inside the cluster

```bash
scripts/load-test.sh cluster --set workers=3 --set load.rate=30000 --set load.duration=3m
```

The coordinator Job waits for 3 worker pods, splits the rate, starts them together and prints per-second progress; it exits `2` when the SLOs in [charts/titanload/values.yaml](../charts/titanload/values.yaml) are violated.

## Docker Compose instead of Kind

For the quickest possible loop (no Kubernetes):

```bash
make compose-up        # NGINX :8080 → Envoy → 3× API, 2× worker, Postgres, Redis, Kafka, Next.js
make compose-obs       # + Prometheus :9090, Grafana :3001, Loki, Fluent Bit, OTel, Jaeger :16686
make compose-load
```

## Tear down

```bash
scripts/teardown.sh     # deletes the cluster and the local registry
```
