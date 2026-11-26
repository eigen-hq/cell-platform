# Single entry point. Pinned versions live in deploy/versions.env.
include deploy/versions.env

SHELL := /usr/bin/env bash
OS := $(shell uname -s | tr A-Z a-z)
ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
BIN := $(CURDIR)/bin
export PATH := $(BIN):$(PATH)
export CELL_NAME ?= cell
export KUBECONFIG := $(CURDIR)/.cell/kubeconfig

K3D := $(BIN)/k3d-$(K3D_VERSION)
KUBECTL := $(BIN)/kubectl-$(KUBECTL_VERSION)
FLUX := $(BIN)/flux-$(FLUX_VERSION)

.PHONY: help tools cell-up cell-down status test e2e demo lima vm-test generate dr-test console console-browser release

help:
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

$(K3D):
	@mkdir -p $(BIN)
	curl -fsSL -o $@ https://github.com/k3d-io/k3d/releases/download/$(K3D_VERSION)/k3d-$(OS)-$(ARCH)
	chmod +x $@

$(KUBECTL):
	@mkdir -p $(BIN)
	curl -fsSL -o $@ https://dl.k8s.io/release/$(KUBECTL_VERSION)/bin/$(OS)/$(ARCH)/kubectl
	chmod +x $@

$(FLUX):
	@mkdir -p $(BIN)
	curl -fsSL https://github.com/fluxcd/flux2/releases/download/v$(FLUX_VERSION)/flux_$(FLUX_VERSION)_$(OS)_$(ARCH).tar.gz | tar xz -C $(BIN) flux
	mv $(BIN)/flux $@

tools: $(K3D) $(KUBECTL) $(FLUX) ## Download pinned CLI tools into bin/
	@ln -sf $(notdir $(K3D)) $(BIN)/k3d
	@ln -sf $(notdir $(KUBECTL)) $(BIN)/kubectl
	@ln -sf $(notdir $(FLUX)) $(BIN)/flux

cell-up: tools ## Start the local cell (k3d) with all base services
	deploy/local-up.sh

cell-down: tools ## Delete the local cell (the backup S3 next to it stays)
	@if docker inspect -f '{{json .NetworkSettings.Networks}}' cell-backup-s3 2>/dev/null | grep -q '"k3d-$(CELL_NAME)"'; then \
		docker network disconnect k3d-$(CELL_NAME) cell-backup-s3; fi
	k3d cluster delete $(CELL_NAME)
	rm -f $(KUBECONFIG)

status: ## Show the state of the cell base services
	go run ./cmd/cellctl status

console: ## Install the console's pinned packages and type-check it
	cd console && npm ci --no-audit --no-fund && npm run check

console-browser: console
	cd console && npx playwright install chromium

test: console ## Unit and controller tests (envtest), console type check
	deploy/lib_test.sh
	go vet ./...
# ponytail: the envtest suite runs about 10m serially; run tests in parallel if it grows
	KUBEBUILDER_ASSETS="$$(go tool setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(BIN)/envtest -p path)" go test -timeout 20m ./...

e2e: console-browser ## End-to-end tests against the local cell (with a browser for the console)
	go test -tags e2e -count=1 -timeout 60m ./test/e2e/...

demo: cell-up ## Target demo on the local cell, step by step (docs/status.md)
	test/demo.sh

release: ## Sign and publish the tagged release VERSION=vX.Y.Z (key: CELL_RELEASE_KEY)
	@[ -n "$(VERSION)" ] || { echo "usage: make release VERSION=vX.Y.Z" >&2; exit 1; }
	deploy/release.sh publish $(VERSION)

dr-test: ## Restore an app into a freshly recreated local cell (destroys the cell)
	go test -tags 'e2e dr' -count=1 -timeout 60m -run TestRestoreIntoFreshCell -v ./test/e2e/...

LIMACTL := $(BIN)/lima/bin/limactl

$(LIMACTL):
	@mkdir -p $(BIN)/lima
	curl -fsSL https://github.com/lima-vm/lima/releases/download/v$(LIMA_VERSION)/lima-$(LIMA_VERSION)-$(shell uname -s)-$(shell uname -m).tar.gz | tar xz -C $(BIN)/lima

lima: $(LIMACTL)

vm-test: $(LIMACTL) ## Cell check on a fresh Ubuntu VM (Lima, local)
	test/vm/run.sh

generate: ## Regenerate deepcopy code and CRDs from api/
	go tool controller-gen object paths=./api/... crd paths=./api/... output:crd:artifacts:config=deploy/crd
