# Sample backend deployments (PoC)

Reference setups of the metrics backends ai-o11y queries, for local testing
and as a starting point for a real deployment. They are samples, not a
supported installation: versions, ports and storage paths are variables in
each `.env.example`, and nothing here holds real credentials.

Each backend has its own directory, with `single/` for everything on one host
(docker compose) and `distributed/` for the parts spread over several machines.

| Directory | Backend (`AIO11Y_BACKEND_PROVIDER`) | What | single | distributed |
|---|---|---|---|---|
| [thanos-sidecar](thanos-sidecar/) | thanos | Prometheus with its OTLP receiver, a Thanos Sidecar and a Thanos Querier. | ✓ | |
| [thanos-receive](thanos-receive/) | thanos | Three Thanos Receives keeping two copies of every sample, a Querier, a Store Gateway and a Compactor, blocks in [storage/seaweedfs](storage/seaweedfs/). | ✓ | |
| [victoria-single](victoria-single/) | victoria | Two single-node VictoriaMetrics, two vmagents writing to both, and two vmauths as the write and query entry points. | ✓ | ✓ (systemd) |
| [storage/seaweedfs](storage/seaweedfs/) | | SeaweedFS with an S3 gateway, the object storage of the Thanos samples. | ✓ | |

Each stores Prometheus names (`system_cpu_time_seconds_total`), which ai-o11y's
metric catalog queries. Every `single/` directory is used the same way:

```sh
cd <backend>/single
cp .env.example .env   # adjust if needed; .env is git-ignored
docker compose up -d
docker compose down -v # stop and remove the data volumes
```
