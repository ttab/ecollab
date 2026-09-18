SHELL := /usr/bin/env bash

NODE_DEPS := testdata/node/node_modules

.PHONY: build
build:
	go build -o /dev/null ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run

# The cursor tests hold the default awareness envelope to the editor
# library that writes it, so the helper's dependencies come from the
# committed lock file rather than from a fresh resolution. Nothing else
# needs node: build, vet and lint do not depend on this target, and the
# tests that need it skip with a hint when it has not run.
.PHONY: node-deps
node-deps: $(NODE_DEPS)

$(NODE_DEPS): testdata/node/package.json testdata/node/package-lock.json
	cd testdata/node && npm ci
	@touch $@

.PHONY: test
test: node-deps
	go test ./...

.PHONY: clean
clean:
	rm -rf $(NODE_DEPS)
