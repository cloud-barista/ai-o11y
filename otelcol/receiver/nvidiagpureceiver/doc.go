// Package nvidiagpureceiver is an OpenTelemetry Collector receiver for NVIDIA
// GPUs. It reads GPU samples from a source and emits them under the source's
// own names: the nvidia-smi source runs nvidia-smi and names each metric after
// its query field, nvidia_smi.<field> (nvidia_smi.utilization.gpu), with the
// GPU's index, uuid and name as attributes. Mapping these to common fields is
// ai-o11y's metric catalog's job, not the receiver's.
//
// With xid enabled it also counts the XID errors the NVIDIA driver logs to the
// kernel log (/dev/kmsg), as nvidia_kmsg.xid.errors: a cumulative count since
// boot per GPU and XID number, the GPU found by the PCI address the line names.
//
// With gpm enabled it runs the helper aio11y-gpm (otelcol/cmd/aio11y-gpm) each
// scrape to read NVML GPM, which Hopper and later GPUs have, as
// nvml.gpm.<field> in percent: per GPU, and per MIG instance with mig "true",
// gpu_instance_id and mig.uuid. GPM is the only source of SM utilization per
// MIG instance. The helper opens libnvidia-ml, which links libc, so it is a
// separate executable and the collector stays statically linked.
//
// Sources are swappable: nvidia-smi is the first, an NVML source can follow
// without touching the receiver.
package nvidiagpureceiver
