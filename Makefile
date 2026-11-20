SHELL := /bin/sh
.DEFAULT_GOAL := help

BIN ?= gfiber-speed
GO ?= go
GOFMT ?= gofmt
TIMEOUT ?= 300s
ARGS ?=
COLOR ?= auto

ifeq ($(COLOR),auto)
USE_COLOR := $(if $(NO_COLOR),,$(MAKE_TERMOUT))
else
USE_COLOR := $(filter always,$(COLOR))
endif
ifneq ($(USE_COLOR),)
CYAN := \033[1;36m
GREEN := \033[1;32m
RESET := \033[0m
endif

PRINT_STEP = @printf '$(CYAN)==>$(RESET) %s\n' '$(1)' >&2
PRINT_OK = @printf '$(GREEN)OK:$(RESET) %s\n' '$(1)' >&2

.PHONY: help
help: ## List the development commands
	@printf '$(CYAN)%s$(RESET)\n' 'gfiber-speed development commands'
	@awk -v c='$(CYAN)' -v r='$(RESET)' 'BEGIN { FS = ":.*## " } /^[a-z-]+:.*## / { printf "%s%-16s%s %s\n", c, $$1, r, $$2 }' Makefile | LC_ALL=C sort

.PHONY: all check fix
all: fix ## Apply mechanical fixes, then run all checks and build
	@$(MAKE) --no-print-directory check

check: verify-modules format-check tidy-check lint test race build ## Check dependencies, formatting, code, tests, and build
	$(call PRINT_OK,all checks passed)

fix: format tidy-fix ## Format Go source and tidy module files

.PHONY: format format-check
format: ## Format Go source
	$(call PRINT_STEP,format)
	@$(GOFMT) -w .

format-check: ## Check Go formatting without editing files
	$(call PRINT_STEP,format check)
	@files="$$($(GOFMT) -l .)" || exit 1; \
		if [ -n "$$files" ]; then printf 'Run make format for:\n%s\n' "$$files" >&2; exit 1; fi

.PHONY: verify-modules tidy-check tidy-fix
verify-modules: ## Verify downloaded dependencies against their checksums
	$(call PRINT_STEP,go mod verify)
	@$(GO) mod verify

tidy-check: ## Check go.mod and go.sum without editing them
	$(call PRINT_STEP,go mod tidy check)
	@set -eu; dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
		cp go.mod go.sum "$$dir/"; \
		$(GO) mod tidy -modfile="$$dir/go.mod"; \
		diff -u go.mod "$$dir/go.mod"; diff -u go.sum "$$dir/go.sum"

tidy-fix: ## Update go.mod and go.sum
	$(call PRINT_STEP,go mod tidy)
	@$(GO) mod tidy

.PHONY: lint test race
lint: ## Run go vet on all packages
	$(call PRINT_STEP,go vet)
	@$(GO) vet ./...

test: ## Run the test suite
	$(call PRINT_STEP,go test)
	@$(GO) test -timeout "$(TIMEOUT)" ./...

race: ## Run the test suite with the race detector (skipped on Android)
	$(call PRINT_STEP,race tests)
	@target_os=$$($(GO) env GOOS) || exit $$?; \
		if [ "$$target_os" = android ]; then \
			printf 'Skipping race tests on Android\n' >&2; \
		else \
			$(GO) test -race -timeout "$(TIMEOUT)" ./...; \
		fi

.PHONY: build install run servers clean
build: ## Build the local executable
	$(call PRINT_STEP,go build)
	@$(GO) build -trimpath -o "$(BIN)" .
	$(call PRINT_OK,built $(BIN))

install: ## Install gfiber-speed into GOBIN or GOPATH/bin
	$(call PRINT_STEP,go install)
	@$(GO) install -trimpath .

run: ## Run a live speed test (e.g. ARGS='--server aus --json')
	@$(GO) run . $(ARGS)

servers: ## List advertised test servers (use ARGS=--json for JSON)
	@$(GO) run . --list-servers $(ARGS)

clean: ## Remove the local build output
	@rm -f -- "$(BIN)"
