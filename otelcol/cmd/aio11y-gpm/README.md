# aio11y-gpm

Helper binary that takes one NVML GPM (GPU Performance Monitoring)
measurement of every NVIDIA device and MIG instance and prints it as JSON.
GPM is the only NVML source of SM utilization per MIG instance;
`nvidia-smi` does not report it.

**Not verified on real GPM hardware.** The tests run against a fake NVML.
On a real host it has only run on GPUs without GPM (it opens NVML and
reports `skipped`). Checking the values needs a Hopper or later GPU,
with and without MIG. T4 and A10 do not support GPM.

## Why a separate binary

It opens `libnvidia-ml` with [purego](https://github.com/ebitengine/purego)
(dlopen without cgo). purego links the binary to libc even with
`CGO_ENABLED=0`, and the collector must stay a statically linked binary
(`make -C otelcol build` checks it). So the NVML code lives in this
executable and the collector only runs it and reads its standard output.
The `gpm` package holds the output types and the sampling logic and does not
import purego, so a reader can use `gpm.Output` and stay static.

## Usage

```sh
CGO_ENABLED=0 go build .        # glibc dynamic executable
./aio11y-gpm [-interval 1s]
```

`make -C otelcol build` builds it into `otelcol/_build/aio11y-gpm` next to the
collector, and `otelcol/install/install.sh --gpm-binary` installs it next to
the collector, where the receiver looks for it first.

`-interval` is the time between the two GPM samples the values are computed
from, and must be more than 100ms. All targets are sampled together, so a run
takes one interval whatever the number of GPUs and MIG instances. When no
target supports GPM it returns without waiting.

## Output

One JSON object on standard output:

```json
{"results":[
  {"uuid":"GPU-...","values":{"gpm.graphics.utilization":55,"gpm.sm.utilization":50}},
  {"uuid":"GPU-...","mig":true,"gpu_instance_id":3,"mig_uuid":"MIG-...","values":{"gpm.sm.utilization":12}}]}
{"results":[],"skipped":"no device or MIG instance supports GPM"}
```

| Key | Meaning |
|---|---|
| `uuid` | UUID of the GPU as NVML gives it (`GPU-...`), the same as `nvidia-smi`'s `uuid`. A MIG instance carries its parent GPU's UUID. |
| `mig` | `true` for a MIG instance, absent for the whole device. |
| `gpu_instance_id` | MIG GPU instance id. Absent when 0 or not MIG; read it together with `mig`. |
| `mig_uuid` | UUID of the MIG device (`MIG-...`), absent if NVML did not report it. |
| `values` | GPM fields of this device or instance. A field NVML could not compute (not supported, NaN) is left out. |
| `skipped` | Why `results` is empty. Absent when there are results. |

Fields in `values`, all percent (0 to 100), from `NVML_GPM_METRIC_*`:

| Field | GPM metric id |
|---|---|
| `gpm.graphics.utilization` | 1 |
| `gpm.sm.utilization` | 2 |
| `gpm.sm.occupancy` | 3 |
| `gpm.dram.bandwidth.utilization` | 10 |
| `gpm.fp64.utilization` | 11 |
| `gpm.fp32.utilization` | 12 |
| `gpm.fp16.utilization` | 13 |

Naming: the NVIDIA GPU receiver (`otelcol/receiver/nvidiagpureceiver`, with
`gpm: {enabled: true}`) runs this helper each scrape and writes each field
under its source, `nvml.<field>` in percent, like the other GPU sources
(`nvidia_smi.<field>`, `amd_smi.<field>`). The fields are named after GPM
rather than after MIG because the same field covers the whole device and its
MIG instances; on a MIG instance's points the receiver adds `mig` `"true"`,
`gpu_instance_id` (also when it is 0) and `mig.uuid`. The catalog row
`mig.sm.utilization` (schema `nvml`) is `nvml.gpm.sm.utilization` on MIG
points divided by 100, as its unit is `1`.

## Exit status

| Status | When | Standard output |
|---|---|---|
| 0 | Measured, or GPM cannot be used: no `libnvidia-ml`, missing GPM symbols (driver too old), `nvmlInit_v2` failed, no device or MIG instance supports GPM | JSON; `results` empty and `skipped` set when nothing was measured |
| 1 | Writing the output failed | nothing usable; the error is on standard error |
| 2 | Bad arguments (unknown flag, extra argument, `-interval` 100ms or less) | nothing; the error is on standard error |

## Requirements

- Linux. On other systems it always reports `skipped`.
- `libnvidia-ml.so.1` (or `libnvidia-ml.so`) from the NVIDIA driver, with the
  `nvmlGpm*` functions; a driver without them gets `skipped` with the missing
  symbol.
- A Hopper or later GPU. Older GPUs, and T4 or A10 vGPU guests, get `skipped`.
- Root, or permission to open the NVIDIA device files (`/dev/nvidia*`).
  Whether GPM needs more than that has not been checked.
- In a vGPU guest only the guest's own MIG instance is visible; the device
  level is not supported there. A hypervisor sees the device and all instances.

## Tests

```sh
go vet ./... && go test ./... && CGO_ENABLED=0 go build ./...
```

The tests do not load `libnvidia-ml`. They feed a fake NVML to the sampler and
compare the JSON for a single GPU, two MIG instances, no library, a device
without GPM and a missing symbol.
