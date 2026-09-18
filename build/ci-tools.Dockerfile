# CI toolbox used by the Jenkins agent pod: helm, kubectl, yq, argocd, git.
#   docker build -f build/ci-tools.Dockerfile -t localhost:5001/titanedge/ci-tools:latest .
FROM alpine:3.22

ARG KUBECTL_VERSION=v1.36.1
ARG HELM_VERSION=v4.3.0
ARG YQ_VERSION=v4.53.6
ARG ARGOCD_VERSION=v3.5.3
ARG TARGETARCH=amd64

RUN apk add --no-cache bash git curl jq ca-certificates openssh-client make \
 && curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
 && curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" | tar -xz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && curl -fsSLo /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" \
 && curl -fsSLo /usr/local/bin/argocd "https://github.com/argoproj/argo-cd/releases/download/${ARGOCD_VERSION}/argocd-linux-${TARGETARCH}" \
 && chmod +x /usr/local/bin/* \
 && rm -rf /tmp/* \
 && adduser -D -u 1000 ci
USER 1000
WORKDIR /home/ci
