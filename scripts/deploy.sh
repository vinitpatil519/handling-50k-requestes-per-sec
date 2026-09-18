#!/usr/bin/env bash
# Deploys the TitanEdge platform chart and runs its Helm tests.
#   scripts/deploy.sh [--profile lite|full] [--tag TAG]
. "$(dirname "$0")/lib.sh"
parse_profile "$@" || { sed -n '2,3p' "$0"; exit 0; }
require helm kubectl

values="${ROOT}/environments/kind/values.yaml"
[ "$PROFILE" = "full" ] && values="${ROOT}/environments/kind-full/values.yaml"

kubectl apply -f "${ROOT}/k8s/namespaces.yaml" >/dev/null
log "helm upgrade --install titan (profile ${PROFILE}, tag ${IMAGE_TAG})"
helm upgrade --install titan "${ROOT}/charts/platform" \
  --namespace "$NAMESPACE" --create-namespace \
  -f "$values" --set global.imageTag="$IMAGE_TAG" \
  --wait --timeout 15m

log "running helm tests (NGINX -> Envoy -> API -> Redis/Postgres)"
helm test titan --namespace "$NAMESPACE" --logs --timeout 5m

kubectl -n "$NAMESPACE" get pods,hpa -o wide
if kubectl get crd scaledobjects.keda.sh >/dev/null 2>&1; then
  kubectl -n "$NAMESPACE" get scaledobjects
fi
ok "TitanEdge is live at http://localhost:8080"
