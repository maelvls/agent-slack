# Thin wrapper around the package.json scripts.
#
# `make install` is the one to use for a local global install: the npm bin
# (bin/agent-slack.js) runs dist/index.js under node, so it needs the
# node-targeted bundle (build:npm). `bun run build` targets Bun and only works
# with bin/agent-slack.bun.js; under node it crashes with
# "__require is not a function".

.DEFAULT_GOAL := help

.PHONY: help deps build build-bun install uninstall dev test typecheck lint fmt fmt-check check clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

node_modules: package.json bun.lock
	bun install
	@touch node_modules

deps: node_modules ## Install dependencies

build: node_modules ## Build dist/ for node (what the npm bin runs)
	bun run build:npm

build-bun: node_modules ## Build dist/ for Bun (bin/agent-slack.bun.js only)
	bun run build

install: build ## Build and link `agent-slack` globally via npm link
	npm link

uninstall: ## Remove the global npm link
	npm unlink -g agent-slack

dev: node_modules ## Run from source, e.g. make dev ARGS="auth whoami"
	bun run src/index.ts $(ARGS)

test: node_modules ## Run tests
	bun test

typecheck: node_modules ## Type-check with tsc
	bun run typecheck

lint: node_modules ## Lint with oxlint
	bun run lint

fmt: node_modules ## Format with oxfmt
	bun run format

fmt-check: node_modules ## Check formatting
	bun run format:check

check: typecheck lint fmt-check test ## Everything CI cares about

clean: ## Remove build output
	bun run clean
