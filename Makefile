SHELL := bash
GO ?= go
BIN := bin
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
SHELLCHECK_IMAGE := koalaman/shellcheck:v0.11.0
SHELL_SCRIPTS := scripts/install-agent.sh scripts/check-no-exec.sh scripts/test-install-agent.sh scripts/testdata/install-cases.sh
VERSION ?= dev

.PHONY: all build test lint fmt-check vet langcheck no-exec shellcheck install-test commitcheck vuln check clean

all: check

## build: build the agent binary into bin/
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '-X main.version=$(VERSION)' -o $(BIN)/ ./cmd/agent

## test: run all tests with the race detector
test:
	$(GO) test -race ./...

## lint: formatting, vet, English-only check, os/exec check, shell scripts
lint: fmt-check vet langcheck no-exec shellcheck

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

langcheck:
	set -o pipefail; git ls-files -z | $(GO) run ./tools/langcheck files

## no-exec: the agent must never start a process
no-exec:
	./scripts/check-no-exec.sh

## shellcheck: lint the shell scripts, through docker when it is not installed
shellcheck:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck $(SHELL_SCRIPTS); \
	else \
		docker run --rm -v "$$PWD:/mnt" -w /mnt $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS); \
	fi

## install-test: run install-agent.sh against throwaway containers (needs docker)
install-test:
	./scripts/test-install-agent.sh

## commitcheck: check that all commit messages are English
commitcheck:
	set -o pipefail; git log -z --format='%H%n%B' | $(GO) run ./tools/langcheck commits

## vuln: scan dependencies for known vulnerabilities
vuln:
	$(GO) run $(GOVULNCHECK) ./...

## check: everything CI runs
check: lint test vuln commitcheck build

clean:
	rm -rf $(BIN)
