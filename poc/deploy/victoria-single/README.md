# VictoriaMetrics single-node pair

Two single-node VictoriaMetrics (A, B) holding the same data. Collectors write
through a vmauth to a vmagent, which writes every sample to both storages and
buffers on disk for a storage that is down; queries go through a vmauth to the
first storage that is up. Either vmauth, vmagent or storage can stop without
collection or queries stopping.

```
collector ─1st─▶ vmauth-1 ─┐                ┌─▶ storage A
          └2nd─▶ vmauth-2 ─┴─▶ vmagent 1/2 ─┴─▶ storage B
ai-o11y ──query─▶ vmauth-1 / vmauth-2 ──▶ storage A or B
```

vmauth has two accounts: a writer that may only write, for collectors, and a
reader that may only query, for ai-o11y. Their names and passwords are in
`.env`; change the sample passwords before exposing vmauth.
The samples speak plain HTTP, so the passwords travel in clear text: put TLS in
front of vmauth before it is reachable from outside a private network.

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=victoria
AIO11Y_BACKEND_QUERY_URL=http://<vmauth-1>:8427,http://<vmauth-2>:8427
AIO11Y_BACKEND_WRITE_URL=http://<vmauth-1>:8427/opentelemetry/v1/metrics,http://<vmauth-2>:8427/opentelemetry/v1/metrics
AIO11Y_BACKEND_QUERY_USERNAME=<reader>
AIO11Y_BACKEND_QUERY_PASSWORD=<reader password>
AIO11Y_BACKEND_WRITE_USERNAME=<writer>
```

Each collector reads the writer's password from `AIO11Y_WRITE_PASSWORD` in its
own environment; ai-o11y never holds it. A collector started without it runs,
but every write is refused (401).

vmagent stores Prometheus names (`system_cpu_time_seconds_total`), the names
ai-o11y's metric catalog queries, with `-opentelemetry.usePrometheusNaming`.
Each storage deduplicates the two copies a vmagent failover may write
(`-dedup.minScrapeInterval`, the collection interval).

## On one host: [single/](single/)

[single/compose.yaml](single/compose.yaml) runs all of it on one host,
vmauth-1 on port 8427 and vmauth-2 on 8428 by default; see
[single/.env.example](single/.env.example).

## Over two VMs: [distributed/](distributed/)

A storage, a vmagent and a vmauth on each VM. Both VMs run the same
[distributed/compose.yaml](distributed/compose.yaml) with their own `.env`
([distributed/.env.example](distributed/.env.example)), `SELF_ADDR` and
`PEER_ADDR` swapped:

```sh
cd victoria-single/distributed
cp .env.example .env   # set SELF_ADDR, PEER_ADDR and the passwords
docker compose up -d
```

Every part listens on the VM's own address (`SELF_ADDR`, host networking):

| Port | Part | Reached by |
|---|---|---|
| 8427 | vmauth | collectors, ai-o11y |
| 8428 | storage | both VMs' vmagents and vmauths |
| 8429 | vmagent | both VMs' vmauths |

ai-o11y settings:

```sh
AIO11Y_BACKEND_PROVIDER=victoria
AIO11Y_BACKEND_QUERY_URL=http://<VM1>:8427,http://<VM2>:8427
AIO11Y_BACKEND_WRITE_URL=http://<VM1>:8427/opentelemetry/v1/metrics,http://<VM2>:8427/opentelemetry/v1/metrics
```
