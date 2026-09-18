#!/usr/bin/env bash
# Creates the Kind cluster (kind/cluster.yaml) and its local image registry.
. "$(dirname "$0")/lib.sh"
require docker kind kubectl

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  ok "cluster ${CLUSTER_NAME} already exists"
else
  log "creating kind cluster ${CLUSTER_NAME}"
  args=(--name "$CLUSTER_NAME" --config "${ROOT}/kind/cluster.yaml" --wait 180s)
  if [ -n "${KIND_NODE_IMAGE:-}" ]; then args+=(--image "$KIND_NODE_IMAGE"); fi
  kind create cluster "${args[@]}"
fi

"${ROOT}/scripts/kind-registry.sh"
kubectl apply -f "${ROOT}/k8s/namespaces.yaml" >/dev/null
ok "cluster ready: $(kubectl config current-context)"
kubectl get nodes -o wide
