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

ai-o11y settings:

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
