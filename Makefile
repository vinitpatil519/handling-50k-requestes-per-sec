# TitanEdge developer entry points. Run `make help`.
# Works on Linux, macOS and Windows (WSL2 or Git Bash with GNU make).

SHELL        := bash
.SHELLFLAGS  := -eu -o pipefail -c
PROFILE      ?= lite
IMAGE_TAG    ?= dev
GO           ?= go
COMPOSE      := docker compose -f deploy/compose/docker-compose.yml
BIN          := bin
LDFLAGS      := -s -w -X github.com/titanedge/titanedge/internal/buildinfo.Version=$(IMAGE_TAG) \
                -X github.com/titanedge/titanedge/internal/buildinfo.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)

export PROFILE IMAGE_TAG

.DEFAULT_GOAL := help

##@ Develop
.PHONY: build test lint fmt titanload web dashboards
build: ## Build api, worker and titanload binaries into ./bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/ ./cmd/...

titanload: ## Build only the titanload CLI
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/ ./cmd/titanload

test: ## Run Go unit tests (with the race detector when cgo is available)
	$(GO) test -race -count=1 ./... 2>/dev/null || $(GO) test -count=1 ./...

lint: ## go vet, gofmt check, web typecheck, helm lint
	$(GO) vet ./...
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; echo "run: make fmt"; exit 1; }
	cd web && npm run typecheck
	helm lint charts/platform charts/titanload

fmt: ## Format Go code
	gofmt -w cmd internal

web: ## Install and build the Next.js dashboard
	cd web && npm ci && npm run build

dashboards: ## Regenerate Grafana dashboards from tools/dashboards/generate.mjs
	node tools/dashboards/generate.mjs

##@ Docker Compose (no Kubernetes)
.PHONY: compose-up compose-obs compose-down compose-load
compose-up: ## Start the core stack at http://localhost:8080
	$(COMPOSE) up -d --build

compose-obs: ## Start core stack + Prometheus/Grafana/Loki/Jaeger
	$(COMPOSE) --profile obs up -d --build

compose-load: ## 30s titanload run against the compose stack
	$(COMPOSE) --profile load run --rm titanload run -u http://nginx:8080/api/v1/ping -c 128 -d 30s --no-dashboard

compose-down: ## Stop and remove the compose stack (keeps volumes)
	$(COMPOSE) --profile obs --profile load down

##@ Kind (local Kubernetes)
.PHONY: up kind images addons deploy smoke load load-cluster argocd down
up: ## Everything: cluster, images, add-ons, platform, smoke test (PROFILE=lite|full)
	scripts/up.sh --profile $(PROFILE) --tag $(IMAGE_TAG)

kind: ## Create the Kind cluster and local registry
	scripts/kind-up.sh

images: ## Build and push all images to the local registry
	scripts/build-images.sh --tag $(IMAGE_TAG)

addons: ## Install observability, KEDA (+ Istio, Argo CD, Jenkins for PROFILE=full)
	scripts/install-addons.sh --profile $(PROFILE)

deploy: ## Helm install/upgrade the platform chart and run helm tests
	scripts/deploy.sh --profile $(PROFILE) --tag $(IMAGE_TAG)

smoke: ## Curl the public edge end to end
	scripts/smoke.sh

load: ## Run titanload from this machine against localhost:8080
	scripts/load-test.sh local

load-cluster: ## Distributed titanload inside the cluster (charts/titanload)
	scripts/load-test.sh cluster

argocd: ## GitOps bootstrap: make argocd REPO=https://github.com/you/titanedge.git
	scripts/argocd-bootstrap.sh $(REPO)

down: ## Delete the Kind cluster and registry
	scripts/teardown.sh

##@ Cloud
.PHONY: tf-validate tf-eks-plan
tf-validate: ## terraform fmt + validate every environment
	terraform -chdir=infra/terraform fmt -check -recursive
	for env in kind eks; do terraform -chdir=infra/terraform/envs/$$env init -backend=false -input=false >/dev/null && terraform -chdir=infra/terraform/envs/$$env validate; done

tf-eks-plan: ## Plan the optional EKS environment (costs money when applied!)
	terraform -chdir=infra/terraform/envs/eks init && terraform -chdir=infra/terraform/envs/eks plan

##@ Help
.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make \033[36m<target>\033[0m [PROFILE=lite|full] [IMAGE_TAG=dev]\n"} \
	/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 } \
	/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
