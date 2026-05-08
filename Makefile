.DEFAULT_GOAL := help

GO         ?= go
PYTHON     ?= python3
BIN_DIR    := bin
BINARY     := $(BIN_DIR)/litemlflow
PKG        := ./...
LDFLAGS    := -X github.com/gorevds/litemlflow/pkg/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
              -X github.com/gorevds/litemlflow/pkg/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
              -X github.com/gorevds/litemlflow/pkg/version.Date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: help build run dev test test-go test-py test-integration lint fmt vet clean docker compat-test py-install py-build dist-helm-lint dist-helm-template fuzz-short test-chaos mutation operator-build operator-test

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build the litemlflow binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/litemlflow

run: build ## Build and run the server (data dir: ./data)
	$(BINARY) up --data ./data --addr :5000

dev: ## Run server with rebuild on save (requires entr or air)
	@command -v entr >/dev/null || (echo "install entr: apt install entr" && exit 1)
	@find . -name '*.go' -not -path './bin/*' | entr -r $(MAKE) run

test: test-go test-py ## Run all tests

test-go: ## Run Go unit tests
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic $(PKG)

test-py: py-install ## Run Python SDK tests
	cd python && $(PYTHON) -m pytest -v tests/

test-integration: build ## Run end-to-end integration tests
	$(PYTHON) tests/integration/run.py

compat-test: build py-install ## Run real MLflow client against LiteMLflow server
	$(PYTHON) tests/integration/mlflow_compat.py

lint: ## Static analysis
	$(GO) vet $(PKG)
	@command -v staticcheck >/dev/null && staticcheck $(PKG) || echo "(install staticcheck for stricter linting)"

fmt: ## Format Go source
	gofmt -s -w .

vet: ## Run go vet
	$(GO) vet $(PKG)

py-install: ## Install Python SDK in editable mode
	cd python && $(PYTHON) -m pip install -e ".[dev]" -q

py-build: ## Build Python wheel
	cd python && $(PYTHON) -m build

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.txt coverage.html data/ python/dist python/build python/*.egg-info

docker: ## Build the Docker image
	docker build -t litemlflow:dev -f Dockerfile .

# ── Distribution packaging ──────────────────────────────────────────────────

DIST_BINARY ?= $(BINARY)
DIST_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

dist-helm-lint: ## Lint the Helm chart
	helm lint dist/helm/litemlflow/

dist-helm-template: ## Dry-run render Helm chart to /tmp/render.yaml
	helm template lmf dist/helm/litemlflow/ > /tmp/render.yaml
	@echo "Rendered to /tmp/render.yaml"

fuzz-short: ## Run each fuzz target for 20s (CI smoke run — seed corpus + brief fuzzing)
	$(GO) test -fuzz='^FuzzParseRunPredicate$$'     -fuzztime=20s ./internal/store/
	$(GO) test -fuzz='^FuzzParseRunFilter$$'        -fuzztime=20s ./internal/store/
	$(GO) test -fuzz='^FuzzParseExperimentFilter$$' -fuzztime=20s ./internal/store/
	$(GO) test -fuzz='^FuzzSplitOnAnd$$'            -fuzztime=20s ./internal/store/
	$(GO) test -fuzz='^FuzzVerifyIDToken$$'         -fuzztime=20s ./internal/auth/
	$(GO) test -fuzz='^FuzzVerifyIDToken_SignatureCorruption$$' -fuzztime=20s ./internal/auth/
	$(GO) test -fuzz='^FuzzIngestOTLP$$'            -fuzztime=20s ./internal/api/native/
	$(GO) test -fuzz='^FuzzIngestTraces$$'          -fuzztime=20s ./internal/api/native/
	$(GO) test -fuzz='^FuzzUploadMeta$$'            -fuzztime=20s ./internal/api/native/

test-chaos: ## Run chaos tests (requires Linux; some scenarios need CAP_SYS_ADMIN)
	$(GO) test -v -count=1 -tags=chaos -timeout=5m ./internal/store/ -run TestChaos

mutation: ## Run gremlins mutation testing (gates: auth 80%, store 70%, webhooks 65%, datasets 60%)
	@command -v gremlins >/dev/null 2>&1 || (echo "gremlins not found; run: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest"; exit 1)
	gremlins unleash --threshold-efficacy 80 ./internal/auth/
	gremlins unleash --threshold-efficacy 70 ./internal/store/
	gremlins unleash --threshold-efficacy 65 ./internal/webhooks/
	gremlins unleash --threshold-efficacy 60 ./internal/datasets/

operator-build: ## Build the LiteMLflow operator binary (→ bin/litemlflow-operator)
	@mkdir -p $(BIN_DIR)
	cd operator && $(GO) build -o ../$(BIN_DIR)/litemlflow-operator ./

operator-test: ## Run operator unit tests (pure, no cluster required)
	cd operator && $(GO) test ./...

# Sunset distribution targets (dist-deb, dist-rpm, terraform-build, terraform-test)
# moved to dist/_sunset/ in v1.2 — see dist/_sunset/README.md.
