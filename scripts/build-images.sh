#!/usr/bin/env bash
# Builds every TitanEdge image and pushes it to the local Kind registry.
#   scripts/build-images.sh [--tag v1.2.3]
. "$(dirname "$0")/lib.sh"
parse_profile "$@" || { echo "usage: $0 [--tag TAG]"; exit 0; }
require docker

COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo none)"
export DOCKER_BUILDKIT=1

build_go() {
  local cmd=$1 image="${REGISTRY}/titanedge/$1:${IMAGE_TAG}"
  log "building ${image}"
  docker build -f "${ROOT}/build/go.Dockerfile" \
    --build-arg CMD="$cmd" --build-arg VERSION="$IMAGE_TAG" --build-arg COMMIT="$COMMIT" \
    -t "$image" "$ROOT"
  docker push "$image" >/dev/null
  ok "pushed ${image}"
}

build_go api
build_go worker
build_go titanload

web="${REGISTRY}/titanedge/web:${IMAGE_TAG}"
log "building ${web}"
docker build -t "$web" "${ROOT}/web"
docker push "$web" >/dev/null
ok "pushed ${web}"

# CI toolbox for the Jenkins agent pod (only needed for the full profile).
if [ "${BUILD_CI_TOOLS:-true}" = "true" ]; then
  tools="${REGISTRY}/titanedge/ci-tools:latest"
  log "building ${tools}"
  docker build -f "${ROOT}/build/ci-tools.Dockerfile" -t "$tools" "${ROOT}/build"
  docker push "$tools" >/dev/null
  ok "pushed ${tools}"
fi
