#!/usr/bin/env bash
# Load-test helpers.
#   scripts/load-test.sh local   [titanload flags...]   run titanload from this machine
#   scripts/load-test.sh smoke   [titanload flags...]   short SLO-gated check (CI)
#   scripts/load-test.sh cluster [helm --set flags...]  distributed run inside Kind
. "$(dirname "$0")/lib.sh"
mode="${1:-local}"
[ $# -gt 0 ] && shift
TARGET="${TARGET:-http://localhost:8080/api/v1/ping}"

titanload() {
  if command -v titanload >/dev/null 2>&1; then
    command titanload "$@"
  else
    (cd "$ROOT" && go run ./cmd/titanload "$@")
  fi
}

case "$mode" in
  local)
    titanload run -u "$TARGET" -c "${CONCURRENCY:-128}" -d "${DURATION:-60s}" "$@"
    ;;
  smoke)
    mkdir -p "${ROOT}/reports"
    titanload run -u "$TARGET" -c 32 -d 20s --no-dashboard \
      --slo-p99 250ms --slo-error-rate 1 --json "${ROOT}/reports/smoke.json" "$@"
    ;;
  cluster)
    require helm kubectl
    helm uninstall load -n titanload >/dev/null 2>&1 || true
    helm upgrade --install load "${ROOT}/charts/titanload" -n titanload --create-namespace \
      --set image.tag="$IMAGE_TAG" "$@"
    log "waiting for the coordinator (Ctrl+C stops following, not the test)"
    kubectl -n titanload wait --for=condition=Ready pod \
      -l app.kubernetes.io/component=coordinator --timeout=120s
    kubectl -n titanload logs -f job/load-titanload-coordinator
    ;;
  *) die "unknown mode ${mode} (local | smoke | cluster)" ;;
esac
