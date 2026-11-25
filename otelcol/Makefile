# Builds the ai-o11y collector and checks the configuration ai-o11y renders
# against it.
#
#   make build      ocb build into _build/aio11y-otelcol (static, linux/amd64), and
#                   the GPM helper into _build/aio11y-gpm (linux/amd64)
#   make components list the components the build holds
#   make test       test ai-o11y's own components (receiver/, processor/), its
#                   helper executables (cmd/) and the VM install scripts
#                   (install/test/run.sh)
#   make validate   render the collector configuration of a sample target and
#                   run `aio11y-otelcol validate` on it
#   make clean
#
# This directory is not part of the ai-o11y Go module: ocb generates its own
# module under _build, so ai-o11y never imports the collector.
#
# aio11y-gpm (cmd/aio11y-gpm) reads NVML GPM for the NVIDIA GPU receiver. It
# opens libnvidia-ml with purego, which links it to libc even without cgo, so
# it is built apart from the collector and only the collector is checked to be
# statically linked.
SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

OCB_VERSION ?= v0.164.0
TOOLS := $(CURDIR)/_tools
BIN := _build/aio11y-otelcol
GPM_BIN := _build/aio11y-gpm

.PHONY: build components validate test clean

$(TOOLS)/builder:
	mkdir -p $(TOOLS)
	GOBIN=$(TOOLS) go install go.opentelemetry.io/collector/cmd/builder@$(OCB_VERSION)

$(BIN): builder-config.yaml $(shell find receiver processor -name "*.go" -o -name go.mod) $(TOOLS)/builder
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(TOOLS)/builder --config builder-config.yaml
	file $(BIN) | grep -q 'statically linked' || { echo "$(BIN) is not statically linked" >&2; exit 1; }

$(GPM_BIN): $(shell find cmd/aio11y-gpm -name "*.go" -o -name go.mod -o -name go.sum)
	cd cmd/aio11y-gpm && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(CURDIR)/$(GPM_BIN) .

build: $(BIN) $(GPM_BIN)

components: $(BIN)
	$(BIN) components

validate: $(BIN)
	cd .. && AIO11Y_OTELCOL_BIN=$(CURDIR)/$(BIN) go test -count=1 -run TestRenderedConfigValidates ./internal/monitoring/collector/

# Tests every module of ai-o11y's own components and helpers, then the VM
# install scripts.
test:
	for mod in $$(find receiver processor cmd -name go.mod | sort); do (cd $$(dirname $$mod) && go test ./...); done
	install/test/run.sh

clean:
	rm -rf _build _tools
