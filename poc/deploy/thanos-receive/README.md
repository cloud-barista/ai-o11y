# Thanos Receive

Collectors write to three Thanos Receives, which keep two copies of every
sample and upload blocks to object storage
([storage/seaweedfs](../storage/seaweedfs/)). A Thanos Querier answers from the
Receives (recent samples) and a Store Gateway (the bucket), merging the copies.

```
collector ──OTLP/HTTP──▶ Receive ×3 (copies: 2) ──blocks──▶ object storage (S3)
                              │                                  │
ai-o11y ──▶ Querier ◀─────────┘◀──── Store Gateway ◀─────────────┤
                                     Compactor (one) ◀───────────┘
```

ai-o11y settings (single/; distributed/ below):

```sh
AIO11Y_BACKEND_PROVIDER=thanos
AIO11Y_BACKEND_QUERY_URL=http://<host>:10902
AIO11Y_BACKEND_WRITE_URL=http://<host>:19291/api/v1/otlp,http://<host>:19292/api/v1/otlp,http://<host>:19293/api/v1/otlp
```

A Receive stores Prometheus names (`system_cpu_time_seconds_total`), the names
ai-o11y's metric catalog queries; it needs no setting for that.

- **Receive**: `RECEIVE_REPLICATION_FACTOR` (2) Receives hold each sample; a
  write succeeds once one copy lands, so one Receive may be down. Every copy
  becomes its own block, so the bucket holds two of each. The copies differ only
  in `receive_replica`, which the Querier deduplicates on.
- **Compactor**: one per bucket. A second would compact the same blocks twice.
  While it is down, blocks are not merged, downsampled or deleted; writes and
  queries go on.

Nothing here has authentication but the S3 account: the Receives and the
Querier take any request, so keep them on a private network.

## On one host: [single/](single/)

**For trying the setup and developing against it**: every part runs as a
container on one machine, which is still a single point of failure.
[single/compose.yaml](single/compose.yaml) starts the SeaweedFS cluster of
[storage/seaweedfs/single](../storage/seaweedfs/single/) with it; one
[single/.env.example](single/.env.example) holds the settings of both. The
Querier listens on port 10902 and the Receives on 19291 to 19293 by default.

## Over three VMs: [distributed/](distributed/)

A Receive and the SeaweedFS parts on every VM; a Querier and a Store Gateway
on two, the Compactor and the MariaDB on one each, picked by
`COMPOSE_PROFILES`. Any one VM may be down: writes, queries and uploads go on.

| VM | `COMPOSE_PROFILES` | Runs |
|---|---|---|
| VM1 | `query,compact` | Receive, Querier, Store Gateway, Compactor |
| VM2 | `query` | Receive, Querier, Store Gateway |
| VM3 | `db` | Receive, MariaDB |

[distributed/compose.yaml](distributed/compose.yaml) starts the SeaweedFS
cluster of [storage/seaweedfs/distributed](../storage/seaweedfs/distributed/)
with it. On every VM:

```sh
cd thanos-receive/distributed
cp .env.example .env   # set SELF_ADDR, the VM addresses and COMPOSE_PROFILES
docker compose up -d
```

Every part listens on the VM's own address (`SELF_ADDR`, host networking), so
the VMs must reach one another on these ports, besides the SeaweedFS ones:

| Port | Part |
|---|---|
| 19291 | Receive: OTLP and remote write (collectors) |
| 10901, 10902 | Receive (gRPC: Queriers and the other Receives; HTTP) |
| 19391 | Receive: copies between Receives |
| 10903, 10904 | Querier (gRPC; HTTP: ai-o11y) |
| 10905, 10906 | Store Gateway (gRPC, HTTP) |
| 10912 | Compactor (HTTP) |

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=thanos
AIO11Y_BACKEND_QUERY_URL=http://<VM1>:10904,http://<VM2>:10904
AIO11Y_BACKEND_WRITE_URL=http://<VM1>:19291/api/v1/otlp,http://<VM2>:19291/api/v1/otlp,http://<VM3>:19291/api/v1/otlp
```
