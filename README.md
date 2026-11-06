# ai-o11y

Monitoring control plane for the AI-MCMP platform. It keeps the monitoring
targets, renders the configuration of the collector that runs on each target,
and answers metric queries over a Prometheus-family backend (Thanos). Samples go
from the collectors to the backend, never through this service.

```
CB-Tumblebug ──(targets)──▶ ai-o11y ──(PromQL)──▶ Thanos Querier
                              │                        ▲
                     collector config                  │ OTLP/HTTP
                              ▼                        │
                        VM: aio11y-otelcol ────────────┘
                        (host metrics, NVIDIA GPUs)
```

## Layout

| Path | What |
|---|---|
| `cmd/ai-o11y-server` | The service. |
| `internal/rest` | REST API (Echo), under `/aio11y`. |
| `internal/monitoring/mapping` | Target table (MariaDB) and the sync with CB-Tumblebug. |
| `internal/monitoring/source/tumblebug` | Management platform adapter for CB-Tumblebug. |
| `internal/monitoring/catalog` | Metric catalog: the fields queries name, see [docs/metric-catalog.md](docs/metric-catalog.md). |
| `internal/monitoring/query` | Renders queries to PromQL and runs them in chunks of targets. |
| `internal/monitoring/backend` | Prometheus query API client and the Thanos backend. |
| `internal/monitoring/collector` | Renders the collector configuration of a target. |
| `otelcol/` | The collector, built with the OpenTelemetry Collector Builder, and its own components (`otelcol/receiver/nvidiagpureceiver`). Not part of the service's Go module. |

## Build and run

```sh
make build            # bin/ai-o11y-server
make test
make lint
make swag             # api/docs from the handlers' godoc

make otelcol          # otelcol/_build/aio11y-otelcol (static, linux/amd64)
make otelcol-validate # render a target's configuration and validate it with the collector
make -C otelcol test  # tests of the collector's own components
```

The service starts with no configuration; each part it is not given (database,
CB-Tumblebug, Thanos) leaves the routes that need it answering 503.
Configuration is environment variables, listed with their defaults in
[conf/template-setup.env](conf/template-setup.env).

## API

All routes are under `/aio11y` and, when `AIO11Y_AUTH_TOKEN` is set, need
`Authorization: Bearer <token>`. The Swagger UI is at `/aio11y/api/index.html`.

| Route | What |
|---|---|
| `POST /targets` | Register a target when its agent is installed. The body names the node as ai-app names a deployment target (`kind`, `nsId`, `infraId`, `nodeId`) and carries its description from CB-Tumblebug (`uid`, `name`, `os`, `gpuCount`, `gpuModel`, ...); without `uid` the description is read from CB-Tumblebug. |
| `GET /targets` | List targets; query parameters are attribute conditions (`?nsId=ns01`). |
| `GET /targets/{targetId}` · `DELETE /targets/{targetId}` | One target. |
| `GET /targets/{targetId}/collectorConfig` | The collector configuration (YAML) for the target. |
| `GET /targets/{targetId}/metrics` | The catalog as it stands for the target: what it has and what it does not, with the reason. |
| `POST /targets/sync` | Bring the targets in step with CB-Tumblebug: update listed targets, delete a missing one only when CB-Tumblebug answers it is gone. |
| `GET /metrics` | The metric catalog. |
| `POST /query` | Query one field over targets named by id or selected by attributes. |
| `GET /readyz` | Readiness. |

## Known limits

- **Collector**: the collector's own metrics are off by default
  (`AIO11Y_COLLECTOR_TELEMETRY_PORT=0`) and nothing reads them yet; set a port
  to serve them on localhost.
- **GPU**: the GPU receiver reads `nvidia-smi` only. MIG and XID fields wait for
  an NVML source, GPUs of other vendors are not read, and a target's metric
  listing cannot tell which fields its particular GPU leaves empty.
- **Sync**: a sync replaces a target's description with what CB-Tumblebug lists,
  attributes given at registration included.
- **Backend**: verified against a local Prometheus 3.16 with a Thanos 0.42
  Sidecar and Querier only. `AIO11Y_THANOS_PARTIAL_RESPONSE` is off for now.
- **Metric catalog**: see the known limits in
  [docs/metric-catalog.md](docs/metric-catalog.md).

## Third-party licenses

ai-o11y is Apache-2.0. Its dependencies are Apache-2.0, MIT, BSD or ISC, except
these under the Mozilla Public License 2.0, used unmodified. Their source is
available from the projects below (MPL-2.0 §3.2).

| Module | Used by | Source |
|---|---|---|
| `github.com/go-sql-driver/mysql` | service (MariaDB driver) | https://github.com/go-sql-driver/mysql |
| `github.com/hashicorp/go-version` | collector (through OpenTelemetry Collector components) | https://github.com/hashicorp/go-version |
| `github.com/hashicorp/golang-lru`, `.../v2` | collector (through OpenTelemetry Collector components) | https://github.com/hashicorp/golang-lru |
