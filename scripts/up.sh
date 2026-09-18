#!/usr/bin/env bash
# One command from zero to a running platform on Kind.
#   scripts/up.sh                 lite profile (~6 GB RAM for Docker)
#   scripts/up.sh --profile full  + Istio, Argo CD, Jenkins (~12 GB RAM)
. "$(dirname "$0")/lib.sh"
parse_profile "$@" || { sed -n '2,4p' "$0"; exit 0; }
"${ROOT}/scripts/kind-up.sh"
"${ROOT}/scripts/build-images.sh" --tag "$IMAGE_TAG"
"${ROOT}/scripts/install-addons.sh" --profile "$PROFILE"
"${ROOT}/scripts/deploy.sh" --profile "$PROFILE" --tag "$IMAGE_TAG"
"${ROOT}/scripts/smoke.sh"
