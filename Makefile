# Canonical task runner. Mirrors .github/workflows/ci.yml so `make verify`
# reproduces the PR gate locally. Run `make help` for the list.

SHELL := bash
GO_PKGS = $(shell go list ./... | grep -v '/frontend/node_modules/')
FRONTEND = frontend

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

# go:embed of internal/web/dist needs the dir to exist even when the
# frontend bundle has not been built. CI stubs it the same way.
.PHONY: dist-stub
dist-stub:
	@mkdir -p internal/web/dist && touch internal/web/dist/.gitkeep

.PHONY: gen
gen: ## Regenerate Zod schemas + OpenAPI from Go sources
	cd $(FRONTEND) && npm run gen

.PHONY: gen-check
gen-check: gen ## Fail if generated files are stale
	git diff --exit-code -- frontend/src/generated frontend/public/openapi.json

.PHONY: lint-go
lint-go: dist-stub ## golangci-lint on Go sources
	golangci-lint run

.PHONY: lint-fe
lint-fe: ## oxlint on frontend sources
	cd $(FRONTEND) && npm run lint

.PHONY: lint
lint: lint-go lint-fe ## All linters

.PHONY: format-check
format-check: ## oxfmt in check mode on frontend sources
	cd $(FRONTEND) && npm run format:check

.PHONY: typecheck
typecheck: ## tsc --noEmit
	cd $(FRONTEND) && npm run typecheck

.PHONY: msw-worker-check
msw-worker-check: ## Verify the tracked worker matches the installed MSW runtime
	cmp $(FRONTEND)/public/mockServiceWorker.js $(FRONTEND)/node_modules/msw/lib/mockServiceWorker.js

.PHONY: test-go
test-go: dist-stub ## Go tests (shuffle, no cache)
	go test -shuffle=on -count=1 $(GO_PKGS)

.PHONY: race
# internal/web/service runs ~10x slower under -race and overruns go test's 10m default.
race: dist-stub ## Go tests with the race detector (needs a C compiler)
	go test -race -shuffle=on -count=1 -timeout 25m $(GO_PKGS)

.PHONY: test-fe
test-fe: ## Frontend tests (vitest)
	cd $(FRONTEND) && npm test

.PHONY: test
test: test-go test-fe ## All tests

.PHONY: vulncheck
vulncheck: dist-stub ## govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: build-fe
build-fe: ## Build the Vite bundles into internal/web/dist
	cd $(FRONTEND) && npm run build

.PHONY: build
build: build-fe ## Build the frontend then the Go binary
	go build ./...

# Native fleet binaries. Static (CGO off) so they run on any Linux host without
# a toolchain; the version comes from git so a report tells which build runs.
NATIVE_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
NATIVE_LDFLAGS = -s -w -X main.version=$(NATIVE_VERSION)
NATIVE_OUT ?= dist/native

.PHONY: fbnode fbmaster native-binaries
fbnode: ## Build the static node agent (lite profile) into $(NATIVE_OUT)
	@mkdir -p $(NATIVE_OUT)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(NATIVE_LDFLAGS)' -o $(NATIVE_OUT)/fbnode ./cmd/fbnode
	@sha256sum $(NATIVE_OUT)/fbnode

fbmaster: ## Build the static master into $(NATIVE_OUT)
	@mkdir -p $(NATIVE_OUT)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(NATIVE_LDFLAGS)' -o $(NATIVE_OUT)/fbmaster ./cmd/fbmaster
	@sha256sum $(NATIVE_OUT)/fbmaster

native-binaries: fbnode fbmaster ## Both native binaries, versioned and checksummed

.PHONY: native-release
native-release: native-binaries build-fe ## Binaries, UI, units, checksums and build info in $(NATIVE_OUT)
	tar -czf $(NATIVE_OUT)/fbui-dist.tar.gz -C internal/web dist
	cp packaging/native/fbmaster.service packaging/native/fbnode.service $(NATIVE_OUT)/
	cd $(NATIVE_OUT) && sha256sum fbnode fbmaster fbui-dist.tar.gz > SHA256SUMS
	@printf '# fbui native release\n\n' > $(NATIVE_OUT)/build-info.md
	@printf -- '- built: %s\n- commit: %s\n- branch: %s\n- dirty files: %s\n- version: %s\n- go: %s\n- node: %s\n\n' \
		"$$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$$(git rev-parse HEAD)" "$$(git rev-parse --abbrev-ref HEAD)" \
		"$$(git status --porcelain | wc -l)" "$(NATIVE_VERSION)" "$$(go version)" "$$(node --version)" >> $(NATIVE_OUT)/build-info.md
	@printf '| file | bytes | sha256 |\n|---|---|---|\n' >> $(NATIVE_OUT)/build-info.md
	@for f in fbnode fbmaster fbui-dist.tar.gz; do \
		printf '| %s | %s | %s |\n' "$$f" "$$(stat -c %s $(NATIVE_OUT)/$$f)" "$$(sha256sum $(NATIVE_OUT)/$$f | cut -d' ' -f1)"; \
	done >> $(NATIVE_OUT)/build-info.md
	@cat $(NATIVE_OUT)/SHA256SUMS $(NATIVE_OUT)/build-info.md

.PHONY: native-smoke
native-smoke: native-binaries build-fe ## End-to-end smoke: master, UI login, subscription, relay enroll
	bash tools/native-smoke.sh

.PHONY: build-storybook
build-storybook: ## Build the static Storybook (compile-checks all stories)
	cd $(FRONTEND) && npm run build-storybook

# The PR gate. Matches ci.yml: codegen freshness, both linters, the formatter,
# typecheck, both test suites, a full build, and the Storybook compile-check.
.PHONY: verify
verify: gen-check lint format-check typecheck msw-worker-check test build build-storybook ## Full local gate (mirrors CI)
	@echo "verify: OK"
