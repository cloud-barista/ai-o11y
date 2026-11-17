# Sample backend deployments (PoC)

Reference setups of the metrics backends ai-o11y queries, for local testing
and as a starting point for a real deployment. They are samples, not a
supported installation: versions, ports and storage paths are variables in
each directory's `.env.example`, and nothing here holds credentials.

| Directory | What |
|---|---|
| [prometheus](prometheus/) | Prometheus with its OTLP receiver, a Thanos Sidecar and a Thanos Querier. |
| [thanos-receive](thanos-receive/) | Three Thanos Receives keeping two copies of every sample, blocks in a SeaweedFS cluster (filer metadata in MariaDB), a Querier, a Store Gateway and a Compactor; on one host. |
| [victoria](victoria/) | Two single-node VictoriaMetrics, two vmagents writing to both, and two vmauths as the write and query entry points; on one host, or on two VMs with [victoria/vm](victoria/vm/). |

Each stores Prometheus names (`system_cpu_time_seconds_total`), which ai-o11y's
metric catalog queries. On one host, each directory is used the same way:

```sh
cp .env.example .env   # adjust if needed; .env is git-ignored
docker compose up -d
docker compose down -v # stop and remove the data volumes
```
