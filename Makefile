# typesafe-go
#
#   make            list every target
#   make verify     the full offline gate — run this before pushing
#   make live       everything above plus the live-API suites (needs a key)
#
# Nothing here needs network or credentials except the `live` and `spec`
# targets, which say so and skip cleanly when no key is present.

SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE   := github.com/nibir1/typesafe-go
GO       ?= go
COVEROUT := coverage.out
ENVFILE  := .env.local

# Load .env.local when present, so `make live` picks up TYPESAFE_API_KEY
# without it having to be exported in the shell.
ifneq (,$(wildcard $(ENVFILE)))
  include $(ENVFILE)
  export
endif

# ANSI helpers, disabled when stdout is not a terminal.
ifneq (,$(findstring xterm,$(TERM)))
  BOLD := $(shell tput bold)
  DIM  := $(shell tput dim)
  OK   := $(shell tput setaf 2)
  WARN := $(shell tput setaf 3)
  ERR  := $(shell tput setaf 1)
  OFF  := $(shell tput sgr0)
endif

define step
	@printf '\n$(BOLD)==> %s$(OFF)\n' "$(1)"
endef

define pass
	@printf '$(OK)  ✓ %s$(OFF)\n' "$(1)"
endef

.PHONY: help
help: ## Show this help
	@printf '$(BOLD)typesafe-go$(OFF)\n\n'
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(BOLD)%-16s$(OFF) %s\n", $$1, $$2}'
	@printf '\n$(DIM)Run `make verify` before pushing.$(OFF)\n\n'

# --- the gates ---------------------------------------------------------------

.PHONY: verify
verify: tidy-check fmt-check vet vet-integration deps test-race contract fixtures secrets docs-check lint-module analyzers ## Full offline gate (run before pushing)
	@printf '\n$(OK)$(BOLD)  All offline checks passed.$(OFF)\n'
	@printf '$(DIM)  `make live` additionally exercises the real API.$(OFF)\n\n'

.PHONY: live
live: verify integration ## verify, plus the live-API suites (needs TYPESAFE_API_KEY)
	@printf '\n$(OK)$(BOLD)  Offline and live checks passed.$(OFF)\n\n'

.PHONY: ci
ci: verify cover ## What CI runs

# --- formatting and static analysis ------------------------------------------

.PHONY: fmt
fmt: ## Format every Go file
	$(call step,gofmt)
	@$(GO) fmt ./... > /dev/null
	$(call pass,formatted)

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted
	$(call step,gofmt -l)
	@out=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$out" ]; then \
	  printf '$(ERR)  ✗ unformatted files:$(OFF)\n%s\n' "$$out"; \
	  printf '$(DIM)    run: make fmt$(OFF)\n'; exit 1; \
	fi
	$(call pass,all files formatted)

.PHONY: vet
vet: ## go vet, default build
	$(call step,go vet)
	@$(GO) vet ./...
	$(call pass,vet clean)

.PHONY: vet-integration
vet-integration: ## go vet with the integration build tag
	$(call step,go vet -tags=integration)
	@$(GO) vet -tags=integration ./...
	$(call pass,vet clean under the integration tag)

.PHONY: generate
generate: ## Re-run go:generate (typesafe-gen)
	$(call step,go generate)
	@$(GO) generate ./... > /dev/null
	$(call pass,generated files refreshed)

# There is no generate-check target. TestGeneratedOutputIsCurrent already
# regenerates into a temp file and compares, which catches drift without
# depending on git state — a git-based check reports a generated file as
# "out of date" merely because it has not been committed yet.

.PHONY: analyzers
analyzers: ## Build the TypeSafe analyzers and run them over this repository
	$(call step,typesafe analyzers)
	@cd lint && $(GO) build -o /tmp/typesafe-lint ./cmd/typesafe-lint
	@out=$$($(GO) vet -vettool=/tmp/typesafe-lint ./... 2>&1 | grep -vE '^#' || true); \
	if [ -n "$$out" ]; then \
	  printf '$(ERR)  ✗ the analyzers flagged this repository:$(OFF)\n%s\n' "$$out"; exit 1; \
	fi
	$(call pass,no analyzer findings)

.PHONY: lint-module
lint-module: ## Test the analyzer module
	$(call step,lint module)
	@cd lint && $(GO) vet ./... && $(GO) test -count=1 ./...
	$(call pass,analyzers pass their own tests)

.PHONY: lint
lint: ## golangci-lint, if installed
	$(call step,golangci-lint)
	@if command -v golangci-lint > /dev/null 2>&1; then \
	  golangci-lint run ./... && printf '$(OK)  ✓ lint clean$(OFF)\n'; \
	else \
	  printf '$(WARN)  ! golangci-lint not installed, skipping$(OFF)\n'; \
	  printf '$(DIM)    https://golangci-lint.run/docs/welcome/install/$(OFF)\n'; \
	fi

# --- dependency policy -------------------------------------------------------

.PHONY: deps
deps: ## Assert the core module has zero third-party dependencies (principle P1)
	$(call step,dependency policy)
	@n=$$($(GO) list -m all | tail -n +2 | wc -l | tr -d ' '); \
	if [ "$$n" != "0" ]; then \
	  printf '$(ERR)  ✗ the core module gained %s dependency/dependencies:$(OFF)\n' "$$n"; \
	  $(GO) list -m all | tail -n +2; exit 1; \
	fi
	$(call pass,zero third-party dependencies)

.PHONY: tidy
tidy: ## go mod tidy
	@$(GO) mod tidy

.PHONY: tidy-check
tidy-check: ## Fail if go.mod or go.sum would change
	$(call step,go mod tidy check)
	@cp go.mod /tmp/go.mod.bak; [ -f go.sum ] && cp go.sum /tmp/go.sum.bak || true; \
	$(GO) mod tidy; \
	if ! diff -q go.mod /tmp/go.mod.bak > /dev/null; then \
	  printf '$(ERR)  ✗ go.mod is not tidy; run: make tidy$(OFF)\n'; \
	  cp /tmp/go.mod.bak go.mod; exit 1; \
	fi; \
	cp /tmp/go.mod.bak go.mod; [ -f /tmp/go.sum.bak ] && cp /tmp/go.sum.bak go.sum || true
	$(call pass,go.mod is tidy)

# --- tests -------------------------------------------------------------------

.PHONY: test
test: ## Offline unit tests
	$(call step,go test)
	@$(GO) test -count=1 ./...
	$(call pass,tests passed)

.PHONY: test-race
test-race: ## Offline tests under the race detector
	$(call step,go test -race)
	@$(GO) test -race -count=1 -timeout 300s ./...
	$(call pass,race-clean)

.PHONY: test-offline
test-offline: ## Prove the suite needs no key and no network
	$(call step,offline and keyless)
	@env -u TYPESAFE_API_KEY -u TYPESAFE_BASE_URL -u TYPESAFE_DEFAULT_MODEL \
	  $(GO) test -count=1 ./...
	$(call pass,suite passes with no credentials)

.PHONY: flake
flake: ## Repeat the suite to surface flakes
	$(call step,flake check: -count=5 -race)
	@$(GO) test -race -count=5 -timeout 600s ./...
	$(call pass,no flakes in 5 runs)

.PHONY: contract
contract: ## Offline contract suite (fixtures vs the locked wire contract)
	$(call step,contract suite)
	@$(GO) test -count=1 ./tests/contract/...
	$(call pass,fixtures satisfy the contract)

.PHONY: integration
integration: ## Live-API suites (needs TYPESAFE_API_KEY; skips without one)
	$(call step,live API)
	@if [ -z "$$TYPESAFE_API_KEY" ]; then \
	  printf '$(WARN)  ! TYPESAFE_API_KEY is not set, skipping$(OFF)\n'; \
	  printf '$(DIM)    put it in %s to enable this target$(OFF)\n' "$(ENVFILE)"; \
	else \
	  $(GO) test -tags=integration -count=1 -timeout 300s ./tests/integration/... && \
	  printf '$(OK)  ✓ live API matches the locked contract$(OFF)\n'; \
	fi

.PHONY: cover
cover: ## Cross-package coverage report
	$(call step,coverage)
	@$(GO) test -count=1 -coverpkg=./... -coverprofile=$(COVEROUT) ./... > /dev/null
	@$(GO) tool cover -func=$(COVEROUT) | tail -1
	$(call pass,coverage measured)

.PHONY: cover-html
cover-html: cover ## Open the coverage report in a browser
	@$(GO) tool cover -html=$(COVEROUT)

.PHONY: bench
bench: ## Run benchmarks
	$(call step,benchmarks)
	@$(GO) test -run '^$$' -bench=. -benchmem ./... 2>&1 | grep -v '^\(ok\|PASS\|no test files\|---\)' || true

# --- fixtures and the wire contract ------------------------------------------

# The fixture check needs jsonschema. Rather than skipping when it is absent —
# a check that quietly does nothing is worse than no check — bootstrap a local
# virtualenv once. It is gitignored and costs a few seconds on first run.
VENV    := .venv
VENVPY  := $(VENV)/bin/python

$(VENVPY):
	@printf '$(DIM)  bootstrapping $(VENV) for schema validation...$(OFF)\n'
	@python3 -m venv $(VENV)
	@$(VENV)/bin/pip install --quiet --disable-pip-version-check jsonschema

.PHONY: fixtures
fixtures: $(VENVPY) ## Validate every golden fixture against the vendored OpenAPI schema
	$(call step,fixtures vs OpenAPI schema)
	@$(VENVPY) scripts/validate_fixtures.py

.PHONY: spec
spec: ## Re-fetch the served OpenAPI document and show any drift
	$(call step,OpenAPI drift)
	@curl -fsSL --max-time 60 https://api.typesafe.ai/openapi.json -o /tmp/served.json
	@norm() { python3 -c 'import json,sys;json.dump(json.load(open(sys.argv[1])),sys.stdout,indent=2,sort_keys=True)' "$$1"; }; \
	norm testdata/spec/openapi.json > /tmp/a.json; norm /tmp/served.json > /tmp/b.json; \
	if diff -u /tmp/a.json /tmp/b.json > /tmp/spec.diff; then \
	  printf '$(OK)  ✓ vendored spec matches the served one$(OFF)\n'; \
	else \
	  printf '$(WARN)  ! the served spec has drifted:$(OFF)\n'; head -60 /tmp/spec.diff; \
	  printf '$(DIM)    update with: make spec-update$(OFF)\n'; exit 1; \
	fi

.PHONY: spec-update
spec-update: ## Vendor the currently served OpenAPI document
	@curl -fsSL --max-time 60 https://api.typesafe.ai/openapi.json -o testdata/spec/openapi.json
	@printf '$(OK)  ✓ vendored spec updated — review the diff before committing$(OFF)\n'

.PHONY: cassettes
cassettes: ## Re-record every cassette from the live API (needs a key)
	@if [ -z "$$TYPESAFE_API_KEY" ]; then \
	  printf '$(ERR)  ✗ TYPESAFE_API_KEY is required to record cassettes$(OFF)\n'; exit 1; \
	fi
	@TYPESAFE_UPDATE_CASSETTES=1 $(GO) test -tags=integration -count=1 ./...
	@printf '$(OK)  ✓ cassettes re-recorded — an empty diff means no drift$(OFF)\n'

# --- safety ------------------------------------------------------------------

.PHONY: secrets
secrets: ## Assert no credential reached any committed fixture or cassette
# Every testdata tree, not just the two at the root: a cassette recorded
# beside the package that uses it is exactly where a key would hide.
# testdata/spec is excluded because it is the vendored OpenAPI document,
# whose prose describes the Authorization header and is not ours to edit.
	$(call step,secret scan)
	@failed=0; scanned=0; \
	while IFS= read -r -d '' f; do \
	  scanned=$$((scanned + 1)); \
	  if grep -qiE 'bearer |authorization|sk-[a-zA-Z0-9_-]{16,}' "$$f"; then \
	    printf '$(ERR)  ✗ possible credential in %s$(OFF)\n' "$$f"; \
	    grep -niE 'bearer |authorization|sk-[a-zA-Z0-9_-]{16,}' "$$f" | head -3; \
	    failed=1; \
	  fi; \
	done < <(find . -path ./.git -prune -o -type f \( -name '*.json' -o -name '*.jsonl' \) \
	    -path '*/testdata/*' -not -path '*/testdata/spec/*' -print0 2>/dev/null); \
	if [ "$$failed" = 1 ]; then exit 1; fi; \
	printf '$(OK)  ✓ scanned %s fixture(s), no credentials$(OFF)\n' "$$scanned"
	@if git ls-files --error-unmatch $(ENVFILE) > /dev/null 2>&1; then \
	  printf '$(ERR)  ✗ %s is tracked by git$(OFF)\n' "$(ENVFILE)"; exit 1; \
	fi
	$(call pass,$(ENVFILE) is not tracked)

.PHONY: vulncheck
vulncheck: ## govulncheck, if installed
	$(call step,govulncheck)
	@if command -v govulncheck > /dev/null 2>&1; then \
	  govulncheck ./... && printf '$(OK)  ✓ no known vulnerabilities$(OFF)\n'; \
	else \
	  printf '$(WARN)  ! govulncheck not installed, skipping$(OFF)\n'; \
	  printf '$(DIM)    go install golang.org/x/vuln/cmd/govulncheck@latest$(OFF)\n'; \
	fi

# --- documentation -----------------------------------------------------------

.PHONY: docs-check
docs-check: ## Assert every exported symbol is documented
	$(call step,doc coverage)
	@python3 scripts/check_docs.py

.PHONY: docs
docs: ## Serve godoc locally
	@if command -v pkgsite > /dev/null 2>&1; then \
	  printf 'Serving on http://localhost:8080/$(MODULE)\n'; pkgsite -http=:8080 .; \
	else \
	  printf '$(WARN)pkgsite not installed$(OFF)\n'; \
	  printf '$(DIM)  go install golang.org/x/pkgsite/cmd/pkgsite@latest$(OFF)\n'; \
	  $(GO) doc -all .; \
	fi

# --- housekeeping ------------------------------------------------------------

.PHONY: clean
clean: ## Remove build and coverage artifacts
	@rm -f $(COVEROUT) /tmp/served.json /tmp/spec.diff
	@$(GO) clean -testcache
	$(call pass,cleaned)

.PHONY: tree
tree: ## Show the repository layout
	@find . -type f \( -name '*.go' -o -name '*.md' -o -name '*.json' -o -name '*.jsonl' -o -name 'Makefile' -o -name 'go.mod' \) \
	  -not -path './.git/*' | sort | sed 's|^\./||'
