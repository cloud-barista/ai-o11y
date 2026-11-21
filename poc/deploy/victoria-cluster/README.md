# VictoriaMetrics cluster

Three vmstorages keep two copies of every sample. Collectors write through a
vmauth to a vminsert, which spreads the series over the vmstorages; ai-o11y
queries through a vmauth a vmselect, which reads every vmstorage and merges the
copies. Any one vmstorage, vminsert, vmselect or vmauth can stop without
collection or queries stopping.

```
collector ─1st─▶ vmauth ─┐               ┌─▶ vmstorage ×3 (copies: 2)
          └2nd─▶ vmauth ─┴─▶ vminsert ×N ┘          │
ai-o11y ──query─▶ vmauth ──▶ vmselect ×N ◀──────────┘
```

vmauth has two accounts: a writer that may only write, for collectors, and a
reader that may only query, for ai-o11y. Their names and passwords are in
`.env`; change the sample passwords before exposing vmauth. The samples speak
plain HTTP, so the passwords travel in clear text: put TLS in front of vmauth
before it is reachable from outside a private network. ai-o11y and collectors
use the same paths as with single-node VictoriaMetrics; vmauth adds the
cluster's tenant path (tenant 0).

ai-o11y settings (single/; distributed/ below):

```sh
AIO11Y_BACKEND_PROVIDER=victoria
AIO11Y_BACKEND_QUERY_URL=http://<host>:8427,http://<host>:8428
AIO11Y_BACKEND_WRITE_URL=http://<host>:8427/opentelemetry/v1/metrics,http://<host>:8428/opentelemetry/v1/metrics
AIO11Y_BACKEND_QUERY_USERNAME=<reader>
AIO11Y_BACKEND_QUERY_PASSWORD=<reader password>
AIO11Y_BACKEND_WRITE_USERNAME=<writer>
```

Each collector reads the writer's password from `AIO11Y_WRITE_PASSWORD` in its
own environment; ai-o11y never holds it.

- **vminsert** stores Prometheus names (`system_cpu_time_seconds_total`), the
  names ai-o11y's metric catalog queries, with
  `-opentelemetry.usePrometheusNaming`. With a vmstorage down it writes the
  copies to the others; with two down, one copy of each sample.
- **vmselect** answers in full while one vmstorage is down. With two down it
  fails the query (`-search.denyPartialResponse`) rather than answer from what
  is left.

## On one host: [single/](single/)

**For trying the setup and developing against it**: every part runs as a
container on one machine, which is still a single point of failure.
[single/compose.yaml](single/compose.yaml) runs three vmstorages, two
vminserts, two vmselects and two vmauths, the vmauths on ports 8427 and 8428
by default; see [single/.env.example](single/.env.example).

## Over three VMs: [distributed/](distributed/)

A vmstorage, a vminsert, a vmselect and a vmauth on every VM. Every VM runs
the same [distributed/compose.yaml](distributed/compose.yaml) with its own
`.env` ([distributed/.env.example](distributed/.env.example)): only
`SELF_ADDR` differs.

```sh
cd victoria-cluster/distributed
cp .env.example .env   # set SELF_ADDR, the VM addresses and the passwords
docker compose up -d
```

Every part listens on the VM's own address (`SELF_ADDR`, host networking):

| Port | Part | Reached by |
|---|---|---|
| 8427 | vmauth | collectors, ai-o11y |
| 8480 | vminsert | every VM's vmauth |
| 8481 | vmselect | every VM's vmauth |
| 8400, 8401 | vmstorage (from vminserts, from vmselects) | every VM's vminsert and vmselect |
| 8482 | vmstorage (HTTP) | |

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=victoria
AIO11Y_BACKEND_QUERY_URL=http://<VM1>:8427,http://<VM2>:8427,http://<VM3>:8427
AIO11Y_BACKEND_WRITE_URL=http://<VM1>:8427/opentelemetry/v1/metrics,http://<VM2>:8427/opentelemetry/v1/metrics,http://<VM3>:8427/opentelemetry/v1/metrics
```
