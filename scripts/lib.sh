#!/usr/bin/env bash
# Shared helpers for TitanEdge scripts. Source, don't execute.
# shellcheck disable=SC2034  # variables are consumed by the scripts that source this file
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-titan}"
REGISTRY_NAME="${REGISTRY_NAME:-kind-registry}"
REGISTRY_PORT="${REGISTRY_PORT:-5001}"
REGISTRY="localhost:${REGISTRY_PORT}"
NAMESPACE="${NAMESPACE:-titanedge}"
IMAGE_TAG="${IMAGE_TAG:-dev}"
PROFILE="${PROFILE:-lite}"

# shellcheck source=../addons/versions.env
set -a; . "${ROOT}/addons/versions.env"; set +a

if [ -t 1 ]; then
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_RESET=$'\033[0m'
else
  C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_RESET=""
fi

log()  { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok()   { printf '%s ✓ %s %s\n' "$C_GREEN" "$*" "$C_RESET"; }
warn() { printf '%s ! %s %s\n' "$C_YELLOW" "$*" "$C_RESET" >&2; }
die()  { printf '%s ✗ %s %s\n' "$C_RED" "$*" "$C_RESET" >&2; exit 1; }

require() {
  local missing=()
  for c in "$@"; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
  [ ${#missing[@]} -eq 0 ] || die "missing tools: ${missing[*]} (see docs/setup-windows.md or docs/setup-linux.md)"
}

# helm_install <release> <namespace> <repo-alias/chart> <version> [extra args...]
helm_install() {
  local release=$1 ns=$2 chart=$3 version=$4; shift 4
  log "helm ${release} (${chart} ${version}) -> ${ns}"
  helm upgrade --install "$release" "$chart" --namespace "$ns" --create-namespace \
    --version "$version" --wait --timeout 10m "$@"
}

parse_profile() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --profile) PROFILE="$2"; shift 2 ;;
      --profile=*) PROFILE="${1#*=}"; shift ;;
      --tag) IMAGE_TAG="$2"; shift 2 ;;
      -h|--help) return 1 ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  case "$PROFILE" in lite|full) ;; *) die "profile must be lite or full" ;; esac
}
