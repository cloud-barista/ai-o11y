# Replicated storage on one host

[compose.yaml](../compose.yaml) at the top of the repository starts ai-o11y
with its database and a replicated store for metrics, logs and traces, all on
one host. Two nodes, A and B, are imitated by two sets of services on one
Docker bridge network: every store is kept twice, once per node, and each node
has an entry point (gateway, vmauth) that prefers its own node and moves to
the other one when that is down.

It is for trying the setup out on one host: everything speaks plain HTTP and
both "nodes" share the host. Two real nodes need the same routes with TLS on
the gateways (vmauth `-tls`) and the node-to-node ports opened to the other
node only.

```
                  collectors (OTLP/HTTP, no token)
                    │ 1st                    │ 2nd
   node A           ▼                        ▼           node B
             gateway-a :14318         gateway-b :14319
                    │                        │
                    ▼                        ▼
             metrics relay A          metrics relay B
             vlagent-a                vlagent-b
             vtagent-a                vtagent-b
                    │   each writes to both  │
                    │   nodes, queueing on   │
                    │   disk for one down    │
                    ▼                        ▼
             metrics store A  ◀─merged─▶  metrics store B
             VictoriaLogs A           VictoriaLogs B
             VictoriaTraces A         VictoriaTraces B
                    ▲                        ▲
                    └─ /metrics /logs /traces┘  (Bearer QUERY_TOKEN,
                                │                via either gateway)
                     ai-o11y :8094 (gateway-a, then gateway-b)
```

## Two metrics stores

Pick one with `COMPOSE_PROFILES` and set the matching `AIO11Y_BACKEND_PROVIDER`, both in
`.env`. Logs and traces are the same in either.

| | `COMPOSE_PROFILES=victoria` | `COMPOSE_PROFILES=prometheus` |
|---|---|---|
| `AIO11Y_BACKEND_PROVIDER` | `victoria` | `thanos` |
| Store per node | `vmstorage` (VictoriaMetrics cluster) | `prometheus` (OTLP receiver on, external label `replica` = `a` / `b`) |
| Metrics relay per node (`/v1/metrics`) | `vmagent`: OTLP in, writes to both nodes' `vminsert`, disk queue per node (`QUEUE_MAX`) | `relay` (OpenTelemetry Collector): OTLP in, writes to both Prometheus, disk queue per Prometheus (`file_storage`), retried with no time limit |
| Query per node (`/metrics/...`) | `vmselect` over both `vmstorage`, deduplicating at 1ms | `thanos-query` over both `thanos-sidecar`, deduplicating on `replica` |
| Names (`system_cpu_time_seconds_total`) | `-opentelemetry.usePrometheusNaming`, scope labels off | `otlp.translation_strategy: UnderscoreEscapingWithSuffixes` (the default) |

In both, the query side reads the two copies and merges them, so a span one
copy lacks (a node that was down longer than its relay's queue could cover) is
answered from the other copy.

## Start

```sh
cp .env.example .env     # change the change-me values and AIO11Y_PUBLIC_HOST
docker compose up -d
docker compose ps        # every service running, the ones with a check healthy
curl -s http://localhost:8094/aio11y/readyz
```

`AIO11Y_PUBLIC_HOST` is the address collectors reach this host at; ai-o11y
puts `http://<it>:GATEWAY_A_PORT/v1/metrics` and
`http://<it>:GATEWAY_B_PORT/v1/metrics` in the collector configurations it
renders, and the same with `/v1/logs` and `/v1/traces`
(`AIO11Y_BACKEND_WRITE_URL`, `_LOGS_WRITE_URL`, `_TRACES_WRITE_URL`, each the
full URL a collector posts to).

Collectors write in `AIO11Y_BACKEND_WRITE_MODE=failover`: to gateway-a, and to
gateway-b while gateway-a fails, with their disk queue on gateway-b's address.
One gateway at a time is enough because the relays behind either one write to
both nodes. With `all` each node would take everything twice, once through
each gateway: the metrics stores merge the duplicate samples, but VictoriaLogs
and VictoriaTraces would keep every line and span twice.

## Switch the metrics store

Change both settings in `.env` together, then recreate:

```sh
docker compose down      # add -v to drop the stored data as well
sed -i 's/^COMPOSE_PROFILES=.*/COMPOSE_PROFILES=prometheus/; s/^AIO11Y_BACKEND_PROVIDER=.*/AIO11Y_BACKEND_PROVIDER=thanos/' .env
docker compose up -d
```

Give one profile only, through `COMPOSE_PROFILES` in `.env`: the gateways read
their routes from `deploy/${COMPOSE_PROFILES}/`, so `--profile` on the command
line does not switch them. Run `docker compose down` before switching, or the
other store's services keep running.

## Register a collector

On a VM that reaches this host, with the collector install scripts
([otelcol/install](../otelcol/install/README.md)):

```sh
export AIO11Y_AUTH_TOKEN=change-me-api   # the value in .env
sudo --preserve-env=AIO11Y_AUTH_TOKEN ./install.sh \
  --server http://192.0.2.10:8094 --ns ns01 --infra infra01 --node node01 \
  --binary ./aio11y-otelcol
```

The gateways take writes with no account, so no `AIO11Y_WRITE_PASSWORD` or
`AIO11Y_WRITE_TOKEN` is needed. To require a write token, give the gateways a
user with a `bearer_token` for the `/v1/...` routes in place of
`unauthorized_user`, and add `AIO11Y_BACKEND_WRITE_BEARER: "true"` to the
`ai-o11y-server` environment in compose.yaml (collectors then read the token
from `AIO11Y_WRITE_TOKEN`). With TLS on the gateways, also turn the write URLs
to `https://`, mount the private CA into `ai-o11y-server` and point
`AIO11Y_BACKEND_WRITE_TLS_CA_FILE` at it there; ai-o11y hands the CA to the
collectors. compose.yaml passes none of these as it is. See
[conf/template-setup.env](../conf/template-setup.env).

## Query through a gateway

The query routes need `Authorization: Bearer <QUERY_TOKEN>`; the first path
segment picks the store and is dropped.

```sh
T=change-me-query
curl -s -H "Authorization: Bearer $T" http://localhost:14319/metrics/api/v1/query --data-urlencode 'query=count(system_cpu_time_seconds_total)'
curl -s -H "Authorization: Bearer $T" http://localhost:14319/logs/select/logsql/query --data-urlencode 'query=* | stats count()'
curl -s -H "Authorization: Bearer $T" 'http://localhost:14319/traces/select/jaeger/api/services'
curl -s -H "Authorization: Bearer $T" 'http://localhost:14319/traces/select/jaeger/api/traces?service=<service>&operation=<operation>&lookback=1h'
```

A span shows up in trace searches some seconds after it is written; until
then the search answers no traces.

## Stop a node

Stop one node's part and keep writing and querying through either gateway:

```sh
docker compose stop vmstorage-b vminsert-b     # victoria: node B's metrics store
docker compose stop prometheus-b               # prometheus: node B's metrics store
docker compose stop victoria-logs-b victoria-traces-b
docker compose start vmstorage-b vminsert-b    # and so on
```

While node B's store is down, node A's relay queues node B's copy on disk and
sends it once B is back; queries are answered from A's copy (vmselect flags the
answer `isPartial`, thanos-query adds a warning). To see a span filled from the
other copy, drop the queue while B is down, as if it had run past its limit:

```sh
# victoria: vmagent-a's queue for its second destination (node B)
docker compose stop vmagent-a
docker run --rm -v ai-o11y_vmagent-a:/q alpine:3.20 sh -c 'rm -r /q/persistent-queue/2_*'
docker compose start vmagent-a vmstorage-b vminsert-b

# prometheus: relay-a's queue for prometheus-b
docker compose stop relay-a
docker run --rm -v ai-o11y_relay-a:/q alpine:3.20 sh -c 'rm /q/prometheus-b/*'
docker compose start relay-a prometheus-b
```

node B's own copy now has a gap, and a query through gateway-b still returns
every sample. Stopping only `vmstorage-b` does not make a gap: `vminsert-b`
holds what it cannot write in memory and writes it once `vmstorage-b` is back.
Stopping `vminsert-b` too while `vmstorage-b` is down drops what it holds,
which leaves a gap of a sample or so in node B's copy with no queue removed.

## Limits

- **Plain HTTP, one host.** The gateway ports and the query token travel in
  clear text, and both "nodes" share the host, its disk and its network: this
  shows how the copies behave, not that the setup survives losing a host.
- **Logs and traces are not merged.** Queries read one node's VictoriaLogs or
  VictoriaTraces (its own first, the other's when that is down), so a span one
  copy lacks is not filled from the other. Merging the two through vlselect
  returns each line twice (measured 2026-11-12: one line written to both,
  `count()` over both gave 2).
- **Queues are bounded.** vmagent, vlagent and vtagent keep up to `QUEUE_MAX`
  per destination and drop the oldest beyond it; the relay keeps up to 10000
  requests per Prometheus and drops new ones beyond it.
- **Partial answers.** ai-o11y is started with
  `AIO11Y_BACKEND_PARTIAL_RESPONSE=true`: with Thanos and the setting off,
  every query fails while one Prometheus is down. VictoriaMetrics ignores the
  setting (ai-o11y logs a warning); vmselect answers partially by default.
- ai-o11y queries metrics only; it has no logs or traces query yet.
