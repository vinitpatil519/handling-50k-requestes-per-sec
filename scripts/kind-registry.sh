#!/usr/bin/env bash
# Starts a local image registry (localhost:5001) and wires every Kind node to
# it, following https://kind.sigs.k8s.io/docs/user/local-registry/.
# Idempotent; called by kind-up.sh and by infra/terraform/envs/kind.
. "$(dirname "$0")/lib.sh"
require docker kind kubectl

if [ "$(docker inspect -f '{{.State.Running}}' "$REGISTRY_NAME" 2>/dev/null || true)" != "true" ]; then
  docker rm -f "$REGISTRY_NAME" >/dev/null 2>&1 || true
  log "starting local registry ${REGISTRY}"
  docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --network bridge \
    --name "$REGISTRY_NAME" registry:2 >/dev/null
fi

log "pointing containerd on each node at the registry"
for node in $(kind get nodes --name "$CLUSTER_NAME"); do
  dir="/etc/containerd/certs.d/localhost:${REGISTRY_PORT}"
  docker exec "$node" mkdir -p "$dir"
  printf '[host."http://%s:5000"]\n' "$REGISTRY_NAME" | docker exec -i "$node" cp /dev/stdin "${dir}/hosts.toml"
done

if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "$REGISTRY_NAME")" = "null" ]; then
  docker network connect kind "$REGISTRY_NAME"
fi

kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
YAML
ok "registry ${REGISTRY} ready for cluster ${CLUSTER_NAME}"
