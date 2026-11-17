# Thanos Receive + SeaweedFS sample

**One host, for trying the setup and developing against it.** Every part runs
as a container on one machine, so the machine is still a single point of
failure; trying out failures of servers and disks needs the parts on separate
VMs.

Collectors write to three Thanos Receives, which keep two copies of every
sample and upload blocks to SeaweedFS. A Thanos Querier answers from the
Receives (recent samples) and a Store Gateway (the bucket), merging the copies.

```
collector ──OTLP/HTTP──▶ Receive ×3 (copies: 2) ──blocks──▶ SeaweedFS S3 (filer ×3)
                              │                                  │
ai-o11y ──▶ Querier ◀─────────┘◀──── Store Gateway ◀─────────────┤
                                     Compactor (one) ◀───────────┘
SeaweedFS: master ×3 (Raft), volume ×3 (replication 001), filer metadata in MariaDB
```

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=thanos
AIO11Y_BACKEND_QUERY_URL=http://<host>:10902
AIO11Y_BACKEND_WRITE_URL=http://<host>:19291/api/v1/otlp,http://<host>:19292/api/v1/otlp,http://<host>:19293/api/v1/otlp
```

A Receive stores Prometheus names (`system_cpu_time_seconds_total`), the names
ai-o11y's metric catalog queries; it needs no setting for that.

## Replication

- **Receive**: `RECEIVE_REPLICATION_FACTOR` (2) Receives hold each sample; a
  write succeeds once one copy lands, so one Receive may be down. Every copy
  becomes its own block, so the bucket holds two of each. The copies differ only
  in `receive_replica`, which the Querier deduplicates on.
- **SeaweedFS**: `SEAWEEDFS_REPLICATION` (`001`) keeps two copies of every
  volume on different volume servers of the same rack (`010` would ask for
  different racks, `100` different data centers). Three masters elect a leader
  by Raft and keep working with one down.
- **Filer metadata** is in one MariaDB shared by every filer
  ([filer.toml](filer.toml)), so any filer serves any file; that MariaDB is not
  replicated.
- **Compactor**: one per bucket. A second would compact the same blocks twice.
  While it is down, blocks are not merged, downsampled or deleted; writes and
  queries go on.

## On one host (docker compose)

The Querier listens on port 10902 and the Receives on 19291 to 19293 by
default; see [.env.example](.env.example), which also holds the S3 account and
the MariaDB passwords to change.
