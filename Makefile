SHELL := /usr/bin/env bash

.PHONY: build
build:
	go build -o /dev/null ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: test
test:
	go test ./...
