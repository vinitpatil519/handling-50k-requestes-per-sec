# Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ErrImagePull` for `localhost:5001/titanedge/*` | Images not pushed or registry not wired | `scripts/build-images.sh`; `docker ps` shows `kind-registry`; re-run `scripts/kind-registry.sh` |
| `helm install` renders no ServiceMonitors / ScaledObjects | CRDs missing when the chart rendered | Install add-ons first, then `helm upgrade` (the chart checks `.Capabilities`) |
| Pods `Pending`, `Insufficient cpu/memory` | Docker Desktop limits | Raise Docker memory/CPU (see setup docs) or use the `lite` profile |
| `too many open files` in Kind nodes / Fluent Bit | inotify limits | `sudo sysctl fs.inotify.max_user_watches=1048576 fs.inotify.max_user_instances=8192` |
| Kafka pod restarting with `node.id` or quorum errors | Stale PVC from a different `kafka.replicas` | `kubectl -n titanedge delete pvc -l app.kubernetes.io/name=kafka` (deletes Kafka data) |
| API `/readyz` 503 `kafka: ...` during startup | Broker still electing | Wait ~30-60s; readiness recovers on its own |
| Worker scaled to 0 / not scaling | No lag or KEDA not installed | `kubectl get scaledobject titan-worker -o yaml`, `kubectl -n keda logs deploy/keda-operator` |
| HPA shows `<unknown>` targets | metrics-server not ready | `kubectl -n kube-system logs deploy/metrics-server`; Kind needs `--kubelet-insecure-tls` (set in values) |
| Probes failing right after enabling NetworkPolicies | CNI blocks node-to-pod probe traffic | Kind's kindnet allows it; for other CNIs add a rule for the node CIDR or set `networkPolicy.enabled=false` to confirm |
| Istio: API 403 `RBAC: access denied` | AuthorizationPolicy: caller is not Envoy | Call through Envoy/NGINX, or from the `titanload` namespace |
| Istio: Prometheus targets down with mTLS on | Scrape port not PERMISSIVE | Ports 9102/9901/9113 are listed in `istio.yaml`; check `kubectl get peerauthentication -n titanedge` |
| titanload: `no_ephemeral_ports` / `conn_refused` spikes | Client port exhaustion or edge saturation | Keep-alive on (default), widen `ip_local_port_range`, use more workers |
| titanload dashboard garbled on Windows | Legacy console | Use Windows Terminal or `--no-dashboard` |
| Argo CD apps `Unknown` / repo errors | Placeholder repo URL | Run the substitution printed by `scripts/argocd-bootstrap.sh` and push |
| Jenkins build cannot push `kind-registry:5000` | Pod DNS cannot resolve the registry container | Use the registry's IP on the kind network: `docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' kind-registry` as `PUSH_REGISTRY` |
| `helm test` fails on `x-cache` | Migration job not finished yet (no items) | `kubectl -n titanedge logs job/titan-api-migrate-1`; re-run `helm test` |

Collect a debug bundle:

```bash
mkdir -p debug && for ns in titanedge monitoring observability keda; do
  kubectl -n $ns get all -o wide > debug/$ns.txt
  kubectl -n $ns get events --sort-by=.lastTimestamp >> debug/$ns.txt
done
kubectl -n titanedge logs -l app.kubernetes.io/part-of=titanedge --all-containers --tail=500 > debug/logs.txt
```
