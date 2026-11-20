# Prometheus + Thanos Sidecar

Collectors write every sample to two Prometheus replicas. A Thanos Sidecar next
to each uploads its blocks to object storage
([storage/seaweedfs](../storage/seaweedfs/)); a Thanos Querier answers from the
Sidecars (recent samples) and a Store Gateway (the bucket), merging the copies.

```
collector ──OTLP/HTTP, to both──▶ Prometheus ×2 ◀── Sidecar ×2 ──blocks──▶ object storage (S3)
                                                       │                        │
ai-o11y ──▶ Querier ◀──────────────────────────────────┘◀──── Store Gateway ◀───┤
                                                              Compactor (one) ◀─┘
```

ai-o11y settings (single/; distributed/ below):

```sh
AIO11Y_BACKEND_PROVIDER=thanos
AIO11Y_BACKEND_QUERY_URL=http://<host>:10902
AIO11Y_BACKEND_WRITE_URL=http://<host>:9090/api/v1/otlp/v1/metrics,http://<host>:9091/api/v1/otlp/v1/metrics
AIO11Y_BACKEND_WRITE_MODE=all
```

Prometheus stores Prometheus names (`system_cpu_time_seconds_total`), the names
ai-o11y's metric catalog queries: `otlp.translation_strategy:
UnderscoreEscapingWithSuffixes` in [prometheus.yml](prometheus.yml), its
default, written out because the catalog depends on it.

- **Prometheus**: the replicas do not copy samples between them, so the
  collectors send each sample to both (`AIO11Y_BACKEND_WRITE_MODE=all`). While
  one is down the collectors queue its samples and resend them when it is back
  (up to the exporter's retry time, 5 minutes by default). The copies differ
  only in `replica`, which the Querier deduplicates on.
- **Late samples**: Prometheus takes samples up to 10 minutes late and keeps
  them in blocks of their own; the Sidecar uploads those too and the Compactor
  merges them (vertical compaction).
- **Compactor**: one per bucket. A second would compact the same blocks twice.
  While it is down, blocks are not merged, downsampled or deleted; writes and
  queries go on.

Nothing here has authentication but the S3 account: Prometheus and the Querier
take any request, so keep them on a private network.

## On one host: [single/](single/)

**For trying the setup and developing against it**: every part runs as a
container on one machine, which is still a single point of failure.
[single/compose.yaml](single/compose.yaml) starts the SeaweedFS cluster of
[storage/seaweedfs/single](../storage/seaweedfs/single/) with it; one
[single/.env.example](single/.env.example) holds the settings of both. The
Querier listens on port 10902 and the Prometheus replicas on 9090 and 9091 by
default.

## Over three VMs: [distributed/](distributed/)

A Prometheus with its Sidecar, a Querier and a Store Gateway on two VMs, the
Compactor on one of them and the MariaDB on the third, picked by
`COMPOSE_PROFILES`; the SeaweedFS parts run on all three. Any one VM may be
down: writes, queries and uploads go on.

| VM | `COMPOSE_PROFILES` | Runs |
|---|---|---|
| VM1 | `prometheus,query,compact` | Prometheus, Sidecar, Querier, Store Gateway, Compactor |
| VM2 | `prometheus,query` | Prometheus, Sidecar, Querier, Store Gateway |
| VM3 | `db` | MariaDB |

[distributed/compose.yaml](distributed/compose.yaml) starts the SeaweedFS
cluster of [storage/seaweedfs/distributed](../storage/seaweedfs/distributed/)
with it. On every VM:

```sh
cd thanos-sidecar/distributed
cp .env.example .env   # set SELF_ADDR, the VM addresses and COMPOSE_PROFILES
docker compose up -d
```

Every part listens on the VM's own address (`SELF_ADDR`, host networking), so
the VMs must reach one another on these ports, besides the SeaweedFS ones:

| Port | Part |
|---|---|
| 9090 | Prometheus: OTLP (collectors) |
| 10901, 10902 | Sidecar (gRPC: Queriers; HTTP) |
| 10903, 10904 | Querier (gRPC; HTTP: ai-o11y) |
| 10905, 10906 | Store Gateway (gRPC, HTTP) |
| 10912 | Compactor (HTTP) |

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=thanos
AIO11Y_BACKEND_QUERY_URL=http://<VM1>:10904,http://<VM2>:10904
AIO11Y_BACKEND_WRITE_URL=http://<VM1>:9090/api/v1/otlp/v1/metrics,http://<VM2>:9090/api/v1/otlp/v1/metrics
AIO11Y_BACKEND_WRITE_MODE=all
```
