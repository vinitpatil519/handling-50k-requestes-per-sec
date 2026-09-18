#!/usr/bin/env bash
# GitOps bootstrap: installs Argo CD and hands the cluster to the
# app-of-apps in gitops/argocd. Push this repository to your own Git server
# first, then:
#   scripts/argocd-bootstrap.sh https://github.com/<you>/titanedge.git
. "$(dirname "$0")/lib.sh"
require helm kubectl
REPO_URL="${1:-}"
[ -n "$REPO_URL" ] || die "usage: $0 <git-repo-url>"
PLACEHOLDER='https://github.com/YOUR_ORG/titanedge.git'

helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
helm repo update argo >/dev/null
kubectl apply -f "${ROOT}/k8s/namespaces.yaml" >/dev/null
helm_install argocd argocd argo/argo-cd "$ARGOCD_VERSION" -f "${ROOT}/addons/values/argocd.yaml"

log "applying AppProject and root application for ${REPO_URL}"
sed "s#${PLACEHOLDER}#${REPO_URL}#g" "${ROOT}/gitops/argocd/project.yaml" | kubectl apply -f -
sed "s#${PLACEHOLDER}#${REPO_URL}#g" "${ROOT}/gitops/argocd/root-app.yaml" | kubectl apply -f -

echo
echo "Argo CD now reconciles gitops/argocd/apps from ${REPO_URL}."
echo "The child Applications in Git still reference ${PLACEHOLDER}."
echo "Commit the same substitution so they track your fork:"
echo
echo "  grep -rl '${PLACEHOLDER}' gitops addons/values | xargs sed -i 's#${PLACEHOLDER}#${REPO_URL}#g'"
echo "  git commit -am 'gitops: point Argo CD at my fork' && git push"
echo
echo "UI: https://localhost:8443  user: admin"
echo "password: kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d"
