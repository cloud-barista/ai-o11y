# Builds the ai-o11y collector and checks the configuration ai-o11y renders
# against it.
#
#   make build      ocb build into _build/aio11y-otelcol (static, linux/amd64)
#   make components list the components the build holds
#   make validate   render the collector configuration of a sample target and
#                   run `aio11y-otelcol validate` on it
#   make clean
#
# This directory is not part of the ai-o11y Go module: ocb generates its own
# module under _build, so ai-o11y never imports the collector.
SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

OCB_VERSION ?= v0.164.0
TOOLS := $(CURDIR)/_tools
BIN := _build/aio11y-otelcol

.PHONY: build components validate clean

$(TOOLS)/builder:
	mkdir -p $(TOOLS)
	GOBIN=$(TOOLS) go install go.opentelemetry.io/collector/cmd/builder@$(OCB_VERSION)

$(BIN): builder-config.yaml $(TOOLS)/builder
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(TOOLS)/builder --config builder-config.yaml
	file $(BIN) | grep -q 'statically linked' || { echo "$(BIN) is not statically linked" >&2; exit 1; }

build: $(BIN)

components: $(BIN)
	$(BIN) components

validate: $(BIN)
	cd .. && AIO11Y_OTELCOL_BIN=$(CURDIR)/$(BIN) go test -count=1 -run TestRenderedConfigValidates ./internal/monitoring/collector/

clean:
	rm -rf _build _tools
