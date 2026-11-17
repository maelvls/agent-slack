# The binary is named after the only directory under cmd/, and the module
# path comes from go.mod, so renaming the tool needs no change in this file.
NAME    := $(notdir $(firstword $(wildcard cmd/*)))
MODULE  := $(shell go list -m)

# Local builds are "dev" builds: they never claim to be outdated and never
# self-update. Releases get their version from the git tag (.goreleaser.yaml).
# Override with e.g. `make build VERSION=v1.2.3`.
VERSION ?= dev
# Packages to build, vet and test, e.g. `make test PKGS=./internal/update/...`.
PKGS    ?= ./...

GOFLAGS_BUILD := -trimpath -ldflags "-s -w -X $(MODULE)/internal/appinfo.Version=$(VERSION)"
GO_DIRS       := cmd internal

.PHONY: all build install test lint lint-extra fmt snapshot clean

all: lint test build

## build: compile a static binary into build/
build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o build/$(NAME) ./cmd/$(NAME)

## install: install the binary into GOBIN (default ~/go/bin)
install:
	CGO_ENABLED=0 go install $(GOFLAGS_BUILD) ./cmd/$(NAME)

## test: run the unit tests
test:
	go test $(PKGS)

## lint: go vet + gofmt check (what CI enforces)
lint:
	go vet $(PKGS)
	@unformatted="$$(gofmt -l $(GO_DIRS))"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on (run 'make fmt'):"; echo "$$unformatted"; exit 1; \
	fi

## lint-extra: optional linters, run only if installed (not enforced by CI)
lint-extra:
	@if command -v staticcheck >/dev/null 2>&1; then echo "staticcheck $(PKGS)"; staticcheck $(PKGS); else echo "staticcheck not installed, skipped"; fi
	@if command -v golangci-lint >/dev/null 2>&1; then echo "golangci-lint run $(PKGS)"; golangci-lint run $(PKGS); else echo "golangci-lint not installed, skipped"; fi

## fmt: format the Go sources
fmt:
	gofmt -w $(GO_DIRS)

## snapshot: build every release archive locally into dist/, without publishing
snapshot:
	goreleaser release --snapshot --clean

## clean: remove build outputs
clean:
	rm -rf build dist
