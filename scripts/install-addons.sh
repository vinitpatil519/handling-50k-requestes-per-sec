#!/usr/bin/env bash
# Installs cluster add-ons with Helm (imperative path; see
# scripts/argocd-bootstrap.sh for the GitOps path).
#
#   scripts/install-addons.sh --profile lite   metrics-server, Prometheus/Grafana,
#                                              Loki, Fluent Bit, OTel, Jaeger, KEDA
#   scripts/install-addons.sh --profile full   lite + Istio (mTLS mesh), Argo CD, Jenkins
. "$(dirname "$0")/lib.sh"
parse_profile "$@" || { sed -n '2,7p' "$0"; exit 0; }
require helm kubectl
V="${ROOT}/addons/values"

log "adding Helm repositories"
while read -r name url; do
  helm repo add "$name" "$url" >/dev/null 2>&1 || true
done <<REPOS
metrics-server https://kubernetes-sigs.github.io/metrics-server
prometheus-community https://prometheus-community.github.io/helm-charts
grafana https://grafana.github.io/helm-charts
fluent https://fluent.github.io/helm-charts
open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
kedacore https://kedacore.github.io/charts
istio https://istio-release.storage.googleapis.com/charts
argo https://argoproj.github.io/argo-helm
jenkins https://charts.jenkins.io
REPOS
helm repo update >/dev/null

kubectl apply -f "${ROOT}/k8s/namespaces.yaml" >/dev/null

helm_install metrics-server kube-system metrics-server/metrics-server "$METRICS_SERVER_VERSION" -f "$V/metrics-server.yaml"

if [ "$PROFILE" = "full" ]; then
  helm_install istio-base istio-system istio/base "$ISTIO_VERSION" --set defaultRevision=default
  helm_install istiod istio-system istio/istiod "$ISTIO_VERSION" -f "$V/istiod.yaml"
  kubectl label namespace "$NAMESPACE" istio-injection=enabled --overwrite
  ok "Istio ${ISTIO_VERSION} installed; namespace ${NAMESPACE} gets sidecars"
fi

# ServiceMonitor / PrometheusRule CRDs must exist before the platform chart.
helm_install kube-prometheus-stack monitoring prometheus-community/kube-prometheus-stack \
  "$KUBE_PROMETHEUS_STACK_VERSION" -f "$V/kube-prometheus-stack.yaml"
helm_install keda keda kedacore/keda "$KEDA_VERSION" -f "$V/keda.yaml"
helm_install loki observability grafana/loki "$LOKI_VERSION" -f "$V/loki.yaml"
helm_install fluent-bit observability fluent/fluent-bit "$FLUENT_BIT_VERSION" -f "$V/fluent-bit.yaml"
helm_install otel-collector observability open-telemetry/opentelemetry-collector \
  "$OTEL_COLLECTOR_VERSION" -f "$V/otel-collector.yaml"
helm upgrade --install jaeger "${ROOT}/addons/charts/jaeger" -n observability --create-namespace --wait --timeout 5m

if [ "$PROFILE" = "full" ]; then
  helm_install argocd argocd argo/argo-cd "$ARGOCD_VERSION" -f "$V/argocd.yaml"
  helm_install jenkins jenkins jenkins/jenkins "$JENKINS_VERSION" -f "$V/jenkins.yaml"
  kubectl apply -f "${ROOT}/jenkins/k8s/rbac.yaml" >/dev/null
fi

ok "add-ons ready (profile: ${PROFILE})"
echo
echo "  Grafana     http://localhost:3000   (admin / admin)"
echo "  Prometheus  http://localhost:9090"
echo "  Jaeger      http://localhost:16686"
if [ "$PROFILE" = "full" ]; then
  pw="$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)"
  echo "  Argo CD     https://localhost:8443  (admin / ${pw})"
  echo "  Jenkins     http://localhost:8081   (admin / admin)"
fi
