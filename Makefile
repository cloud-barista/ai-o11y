APP   := ai-o11y-server
GOBIN := $(shell go env GOPATH)/bin
IMAGE := ai-o11y:local

.PHONY: build run test swag fmt lint verify docker-build otelcol otelcol-validate

build:
	go build -o bin/$(APP) ./cmd/$(APP)

# Starts with no configuration at all; every setting has a default.
run:
	go run ./cmd/$(APP)

test:
	go test ./...

swag:
	$(GOBIN)/swag init -g cmd/$(APP)/main.go -o api/docs --parseInternal

fmt:
	gofmt -w .

lint:
	$(GOBIN)/golangci-lint run

verify: fmt build test lint

docker-build:
	docker build -t $(IMAGE) .

# The collector is built on its own from otelcol/; see otelcol/Makefile.
otelcol:
	$(MAKE) -C otelcol build

otelcol-validate:
	$(MAKE) -C otelcol validate
