# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT

# kg - build, generate and test targets.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

IMAGE      ?= ghcr.io/ashon/kg
IMAGE_TAG  ?= dev
MANAGER_IMAGE := $(IMAGE):$(IMAGE_TAG)

LDFLAGS := -s -w \
  -X github.com/Ashon/kg/internal/version.Version=$(VERSION) \
  -X github.com/Ashon/kg/internal/version.GitCommit=$(GIT_COMMIT) \
  -X github.com/Ashon/kg/internal/version.BuildDate=$(BUILD_DATE) \
  -X github.com/Ashon/kg/internal/version.Image=$(MANAGER_IMAGE)

BIN := bin

##@ General

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
	  /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } \
	  /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: generate
generate: ## Regenerate deepcopy code, CRDs and RBAC
	go tool controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
	go tool controller-gen crd paths=./api/... output:crd:artifacts:config=config/crd
	go tool controller-gen rbac:roleName=kgenesis-manager-role paths=./internal/controller/... \
	  output:rbac:artifacts:config=config/rbac
	$(MAKE) components

.PHONY: components
components: ## Rebuild the provider manifest embedded in the CLI
	./hack/build-components.sh

.PHONY: license-headers
license-headers: ## Add the SPDX header to any file kg owns that lacks one
	./hack/license-headers.sh --fix

.PHONY: fmt
fmt: ## Format the source
	gofmt -w ./api ./cmd ./internal

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: ## Run the unit tests
	go test ./... -race

# Only the generated paths are compared. Checking the whole tree would report
# any work in progress as stale generated output, which is a confusing way to
# find out you simply have uncommitted changes.
GENERATED := api/v1alpha1/zz_generated.deepcopy.go config/crd config/rbac internal/assets

.PHONY: verify
verify: generate fmt ## Fail when generated files or license headers are out of date
	./hack/license-headers.sh
	@if ! git diff --quiet -- $(GENERATED); then \
	  echo "Generated files are out of date. Run 'make generate' and commit the result:"; \
	  git --no-pager diff --stat -- $(GENERATED); \
	  exit 1; \
	fi
	@echo "Generated files are current."

##@ Build

.PHONY: build
build: ## Build the kg CLI into bin/
	mkdir -p $(BIN)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/kg ./cmd/kg

.PHONY: manager
manager: ## Build the provider manager binary into bin/
	mkdir -p $(BIN)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/manager ./cmd/manager

.PHONY: install
install: ## Install the kg CLI into GOBIN
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/kg
	@target="$$(go env GOBIN)"; \
	if [ -z "$$target" ]; then target="$$(go env GOPATH)/bin"; fi; \
	echo "Installed $$target/kg"

.PHONY: docker-build
docker-build: ## Build the provider container image
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg GIT_COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(MANAGER_IMAGE) .

.PHONY: release
release: ## Build the release artifacts into dist/ (VERSION=v0.1.0)
	./hack/release.sh $(VERSION)

.PHONY: docker-push
docker-push: ## Push the provider container image
	docker push $(MANAGER_IMAGE)

##@ End to end

.PHONY: e2e
e2e: ## Run the scenarios against container hosts (needs Docker)
	DRIVER=docker ./test/scenarios/run.sh

.PHONY: e2e-keep
e2e-keep: ## Same as e2e, but leave the clusters and hosts running for inspection
	KEEP=1 DRIVER=docker ./test/scenarios/run.sh

.PHONY: vm-test
vm-test: ## Run every scenario on real VMs; see test/scenarios/SCENARIOS.md (macOS, needs lima)
	./test/scenarios/run.sh

.PHONY: vm-test-keep
vm-test-keep: ## Same as vm-test, but leave the VMs running for inspection
	KEEP=1 ./test/scenarios/run.sh

.PHONY: vm-test-reuse
vm-test-reuse: ## Same as vm-test against the VMs already running, skipping provisioning
	REUSE=1 ./test/scenarios/run.sh

.PHONY: e2e-cni-manifest
e2e-cni-manifest: ## Re-extract the vendored kindnet manifest the e2e test installs
	./hack/extract-kindnet.sh

##@ Cleanup

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN)
