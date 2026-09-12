# SPDX-License-Identifier: MIT
# go-zendure2mqtt — developer Makefile
#
# Tabs are required by GNU make. The whitespace rules below pin sane
# shell behaviour so a failing recipe step actually aborts the target
# instead of silently moving on.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO            ?= go
GOFUMPT       ?= gofumpt
GOIMPORTS     ?= goimports
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK   ?= govulncheck
GOLICENSES    ?= go-licenses
DOCKER        ?= docker

export CGO_ENABLED := 0

BIN_DIR  := bin
MODULE   := github.com/SukramJ/go-zendure2mqtt
PKG_VER  := $(MODULE)/internal/version

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG_VER).Version=$(VERSION) \
	-X $(PKG_VER).Commit=$(COMMIT) \
	-X $(PKG_VER).BuildDate=$(BUILD_DATE)

GO_BUILD_FLAGS := -trimpath -ldflags="$(LDFLAGS)"

DOCKER_IMAGE ?= go-zendure2mqtt
DOCKER_TAG   ?= $(VERSION)

DIST_DIR         := dist
RELEASE_TARGETS  ?= linux/amd64 linux/arm64 darwin/arm64
# Pulled from internal/version/version.go's default — the contract is
# that bumping the source default and adding a changelog.md entry
# happen in the same commit, so this is the canonical "what release am
# I packaging" answer. Override via `make release RELEASE_VERSION=...`
# for ad-hoc dry runs.
RELEASE_VERSION  ?= $(shell awk -F'"' '/^[[:space:]]*Version = /{print $$2; exit}' internal/version/version.go)
RELEASE_PAYLOAD  := zendure.yaml config-template.yaml README.md LICENSE changelog.md

.PHONY: help
help: ## show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Tool versions, pinned to match .github/workflows/ci.yml. A local gate is
# only usable if it reports what CI reports, so these two lists must be
# bumped together — raise both, run `make check`, and fix or justify
# whatever the new release finds in the same change.
#
# govulncheck and go-licenses are pinned too, now that CI's security job
# gates them. Pinning the binary does not freeze the answer: govulncheck
# resolves the vulnerability database at run time and go-licenses classifies
# whatever a dependency actually ships, so a newly published advisory or a
# relicensed dependency still turns the gate red without a commit here —
# which is the point. What the pin removes is the other source of red: a
# tool release changing its own reachability analysis or license classifier
# under an unrelated PR.
#
# goimports stays on @latest deliberately: it has no gate of its own,
# gofumpt is the formatting authority.
GOFUMPT_VERSION       ?= v0.12.0
GOLANGCI_LINT_VERSION ?= v2.13.2
GOVULNCHECK_VERSION   ?= v1.8.0
GOLICENSES_VERSION    ?= v1.6.0
GITLEAKS_VERSION      ?= v8.30.1

.PHONY: setup
setup: hooks ## install developer tooling (gofumpt, goimports, golangci-lint, govulncheck, go-licenses) + git hooks
	$(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	$(GO) install golang.org/x/tools/cmd/goimports@latest
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/google/go-licenses@$(GOLICENSES_VERSION)

.PHONY: hooks
hooks: ## point git at the tracked hooks in .githooks/ (blocks direct commits on main)
	@git config core.hooksPath .githooks
	@echo "git core.hooksPath -> .githooks (direct commits on main/master are now blocked)"

.PHONY: build
build: build-daemon build-util ## build both binaries into bin/

.PHONY: build-daemon
build-daemon: ## build the zendure2mqtt daemon
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/zendure2mqtt ./cmd/zendure2mqtt

.PHONY: build-util
build-util: ## build the zendure2mqtt-util interactive CLI
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/zendure2mqtt-util ./cmd/zendure2mqtt-util

.PHONY: install
install: ## go install both binaries to $(go env GOPATH)/bin
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/zendure2mqtt
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/zendure2mqtt-util

.PHONY: test
test: ## run the full test suite with race detector
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=60s ./...

.PHONY: test-cover
test-cover: ## run tests + coverage report
	CGO_ENABLED=1 $(GO) test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -20

# Fuzz targets live in ./internal/process: sanitizeSegment and validPackSN
# parse device-supplied strings into MQTT topic levels and HA unique_ids, and
# a unique_id is what Home Assistant keys its entity registry on — with no
# migration path once published. Seed corpora are committed under
# internal/process/testdata/fuzz/, so the seeds also replay as a plain
# regression table under `make test`.
FUZZ_PKG ?= ./internal/process
FUZZTIME ?= 5m

.PHONY: fuzz-smoke
fuzz-smoke: ## run every Fuzz target in $(FUZZ_PKG) for 10s each (CI smoke gate)
	@for fn in $$($(GO) test $(FUZZ_PKG) -list '^Fuzz' | grep '^Fuzz'); do \
	  echo "== fuzz-smoke: $$fn (10s) =="; \
	  $(GO) test $(FUZZ_PKG) -run '^$$' -fuzz "^$$fn$$" -fuzztime=10s; \
	done

.PHONY: fuzz
fuzz: ## run every Fuzz target in $(FUZZ_PKG) for FUZZTIME (default 5m; local/periodic)
	@for fn in $$($(GO) test $(FUZZ_PKG) -list '^Fuzz' | grep '^Fuzz'); do \
	  echo "== fuzz: $$fn (fuzztime=$(FUZZTIME)) =="; \
	  $(GO) test $(FUZZ_PKG) -run '^$$' -fuzz "^$$fn$$" -fuzztime=$(FUZZTIME); \
	done

# Per-package coverage gate, deliberately per package rather than on a
# merged total: a single total lets one well-tested package hide a package
# nothing executes, which is the failure mode this tree actually has.
#
# COVER_MIN is the floor for any package not listed in COVER_MIN_OVERRIDES.
# 25 is chosen to be green on main today with margin, not aspirational — a
# gate that arrives red is worse than no gate, because the next person
# cannot tell their finding from the pre-existing ones. The floor clears
# every tested package with room to spare; the thinnest is
# internal/coordinator at 32.3%, and that margin is deliberate — the ADR
# 0070 work is actively adding rendering paths in and around it.
#
# COVER_MIN_OVERRIDES pins every package that is below the floor today at
# (the floor of) its current number. That makes this a ratchet rather than a
# threshold: those packages cannot get *worse*, and raising one is a matter
# of deleting its line. Six of them have no test file at all
# (cmd/zendure2mqtt-util, internal/source, internal/state, internal/version,
# internal/zendure/local, internal/zendure/model) — the pin records that as
# a known state instead of letting a merged total paper over it.
COVER_MIN ?= 25
COVER_MIN_OVERRIDES ?= \
	cmd/zendure2mqtt=0 \
	cmd/zendure2mqtt-util=0 \
	internal/source=0 \
	internal/state=0 \
	internal/version=0 \
	internal/zendure/cloud=8 \
	internal/zendure/local=0 \
	internal/zendure/model=0

.PHONY: cover-check
cover-check: ## per-package coverage gate (COVER_MIN percent, default 25); NOT part of `check`
	@status=0; \
	for pkg in $$($(GO) list ./...); do \
	  suffix=$${pkg#$(MODULE)/}; \
	  min="$(COVER_MIN)"; \
	  for ov in $(COVER_MIN_OVERRIDES); do \
	    if [ "$${ov%%=*}" = "$$suffix" ]; then min="$${ov#*=}"; fi; \
	  done; \
	  profile=$$(mktemp); log=$$(mktemp); \
	  if ! CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=120s -covermode=atomic \
	      -coverprofile="$$profile" "$$pkg" >"$$log" 2>&1; then \
	    echo "FAIL $$suffix (test failure)"; cat "$$log"; status=1; \
	    rm -f "$$profile" "$$log"; continue; \
	  fi; \
	  total=$$($(GO) tool cover -func="$$profile" | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	  rm -f "$$profile" "$$log"; \
	  if [ -z "$$total" ]; then total=0; fi; \
	  if awk -v t="$$total" -v m="$$min" 'BEGIN{exit !(t+0>=m+0)}'; then \
	    printf "ok   %-34s %5s%% >= %s%%\n" "$$suffix" "$$total" "$$min"; \
	  else \
	    printf "FAIL %-34s %5s%% <  %s%%\n" "$$suffix" "$$total" "$$min"; status=1; \
	  fi; \
	done; \
	exit $$status

.PHONY: vet
vet: ## run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## format with gofumpt + goimports (writes in place)
	$(GOFUMPT) -w .
	$(GOIMPORTS) -w -local $(MODULE) .

.PHONY: fmt-check
fmt-check: ## fail when sources are not gofumpt-clean
	@diff=$$($(GOFUMPT) -l .); \
	if [ -n "$$diff" ]; then \
	  echo "gofumpt would rewrite:"; echo "$$diff"; exit 1; \
	fi

.PHONY: lint
lint: ## run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln: ## scan dependencies + reachable code for known vulnerabilities (govulncheck)
	$(GOVULNCHECK) ./...

.PHONY: licenses
licenses: ## fail on copyleft dependency licenses (GPL/AGPL/LGPL forbidden; MPL = reciprocal)
	$(GOLICENSES) check ./... --disallowed_types=forbidden,restricted,reciprocal

# Git mode: scans the tracked history, not just the working tree. A secret
# committed once and reverted in the next commit is still in the history and
# still a secret — and this daemon handles a Zendure cloud app token plus
# MQTT credentials. Deliberately configless: the default ruleset is clean
# over the whole history, so there is no allowlist to hide behind. The first
# fixture that trips a rule gets a .gitleaks.toml entry with a reason,
# rather than being pre-exempted by a blanket testdata/docs carve-out.
.PHONY: secrets
secrets: ## scan the tracked git history for committed secrets (gitleaks)
	$(GO) run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --no-banner --redact .

.PHONY: tidy
tidy: ## sync go.mod / go.sum
	$(GO) mod tidy

# Non-destructive by construction: go.mod/go.sum are stashed first and
# restored on failure, so this is safe inside `make check` — a gate that
# rewrote the tree as a side effect of reporting would be its own problem.
# Drift matters here immediately: the ADR 0070 migration is a series of
# go-hamqtt version bumps, and a bump is exactly what leaves a stale go.sum
# line, an orphaned require, or an indirect that has become direct.
.PHONY: tidy-check
tidy-check: ## verify go.mod/go.sum are tidy + module checksums verify (CI gate)
	$(GO) mod verify
	@tmp=$$(mktemp -d); 	cp go.mod go.sum "$$tmp/"; 	$(GO) mod tidy; 	if diff -q "$$tmp/go.mod" go.mod >/dev/null && diff -q "$$tmp/go.sum" go.sum >/dev/null; then 	  rm -rf "$$tmp"; echo "go.mod/go.sum are tidy"; 	else 	  echo "go.mod/go.sum are not tidy — run 'make tidy' and commit the result:"; 	  diff -u "$$tmp/go.mod" go.mod || true; 	  diff -u "$$tmp/go.sum" go.sum || true; 	  cp "$$tmp/go.mod" "$$tmp/go.sum" .; rm -rf "$$tmp"; 	  exit 1; 	fi

.PHONY: check
check: vet fmt-check lint tidy-check test ## the pre-commit / pre-push gate

.PHONY: run
run: build-daemon ## run the daemon against ./config.yaml
	$(BIN_DIR)/zendure2mqtt --config ./config.yaml

.PHONY: clean
clean: ## remove build artefacts
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out

.PHONY: release
release: ## stage cross-compiled release archives + notes into dist/ (no upload)
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@echo "release version: $(RELEASE_VERSION)"
	@version="$(RELEASE_VERSION)"; \
	commit="$$(git rev-parse --short HEAD 2>/dev/null || echo none)"; \
	build_date="$$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	ldflags="-s -w \
	  -X $(PKG_VER).Version=$$version \
	  -X $(PKG_VER).Commit=$$commit \
	  -X $(PKG_VER).BuildDate=$$build_date"; \
	for tgt in $(RELEASE_TARGETS); do \
	  goos=$${tgt%/*}; goarch=$${tgt#*/}; \
	  stage="$(DIST_DIR)/go-zendure2mqtt-$$version-$$goos-$$goarch"; \
	  mkdir -p "$$stage"; \
	  echo "==> $$goos/$$goarch -> $$stage"; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/zendure2mqtt" ./cmd/zendure2mqtt; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/zendure2mqtt-util" ./cmd/zendure2mqtt-util; \
	  cp $(RELEASE_PAYLOAD) "$$stage/"; \
	  ( cd $(DIST_DIR) && tar -czf "$$(basename $$stage).tar.gz" "$$(basename $$stage)" ); \
	  rm -rf "$$stage"; \
	done
	@cd $(DIST_DIR) && shasum -a 256 *.tar.gz > SHA256SUMS
	@$(MAKE) --no-print-directory release-notes
	@echo ""
	@ls -lh $(DIST_DIR)

.PHONY: release-notes
release-notes: ## extract the changelog.md section for $(RELEASE_VERSION) into dist/RELEASE_NOTES.md
	@mkdir -p $(DIST_DIR)
	@script/extract-release-notes.sh $(RELEASE_VERSION) > $(DIST_DIR)/RELEASE_NOTES.md
	@echo "--- $(DIST_DIR)/RELEASE_NOTES.md (first 20 lines) ---"
	@head -20 $(DIST_DIR)/RELEASE_NOTES.md

.PHONY: docker
docker: ## build a tagged container image
	$(DOCKER) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(DOCKER_IMAGE):$(DOCKER_TAG) \
	  -t $(DOCKER_IMAGE):latest .

.PHONY: version
version: ## print the resolved build metadata
	@echo "VERSION    = $(VERSION)"
	@echo "COMMIT     = $(COMMIT)"
	@echo "BUILD_DATE = $(BUILD_DATE)"
