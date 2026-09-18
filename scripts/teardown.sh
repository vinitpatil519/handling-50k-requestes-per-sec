#!/usr/bin/env bash
# Deletes the Kind cluster and the local registry (all local cluster data).
. "$(dirname "$0")/lib.sh"
require kind docker
kind delete cluster --name "$CLUSTER_NAME"
docker rm -f "$REGISTRY_NAME" >/dev/null 2>&1 || true
ok "cluster ${CLUSTER_NAME} and registry removed"
