# SeaweedFS

The object storage the Thanos samples write blocks to: a SeaweedFS cluster
serving S3. The thanos-* samples start it with them.

```
Thanos ──S3──▶ filer + S3 gateway ×3 ──▶ volume server ×3 (each volume on two)
                     │                         ▲
                     └─ metadata ─▶ MariaDB    └── master ×3 (Raft)
```

Thanos reaches it at `<filer>:8333`, path style without TLS, with
`S3_ACCESS_KEY` / `S3_SECRET_KEY` and bucket `S3_BUCKET`.

- **Volumes**: `SEAWEEDFS_REPLICATION` (`001`) keeps two copies of every volume
  on different volume servers of the same rack (`010` would ask for different
  racks, `100` different data centers).
- **Masters**: three elect a leader by Raft and keep working with one down.
  The leader restores missing volume copies every 17 minutes
  ([master.toml](master.toml)); the image does no such maintenance by itself.
- **Filer metadata** is in one MariaDB shared by every filer
  ([filer.toml](filer.toml)), so any filer serves any file; that
  MariaDB is not replicated.

The S3 gateways need the S3 account; the masters', volume servers' and filers'
own HTTP ports take any request, so keep them on a private network.

## On one host: [single/](single/)

**For trying the setup and developing against it**: every part runs as a
container on one machine. Started with a thanos-* sample it takes that
sample's `.env`; [single/.env.example](single/.env.example) lists its own
settings. Its ports are not published on the host: only the containers of the
same compose project reach it.

## Over three VMs: [distributed/](distributed/)

A master, a volume server and a filer with its S3 gateway on every VM, and the
MariaDB on one of them (`COMPOSE_PROFILES=db`). Every VM runs the same
[distributed/compose.yaml](distributed/compose.yaml) with its own `.env`
([distributed/.env.example](distributed/.env.example)): only `SELF_ADDR` and
`COMPOSE_PROFILES` differ. Started with thanos-receive/distributed it takes that
sample's `.env`. Each part listens on `SELF_ADDR` (host networking):

| Port | Part |
|---|---|
| 9333, 19333 | master (HTTP, gRPC) |
| 8080, 18080 | volume server (HTTP, gRPC) |
| 8888, 18888 | filer (HTTP, gRPC) |
| 8333 | S3 gateway |
| 3306 | MariaDB (the `db` VM) |

The MariaDB is the one part not duplicated: while it is down, the filers
cannot read or write files.
