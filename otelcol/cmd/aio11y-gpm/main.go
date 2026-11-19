// Command aio11y-gpm takes one NVML GPM (GPU Performance Monitoring)
// measurement of every NVIDIA device and MIG instance and writes it to
// standard output as JSON (gpm.Output). GPM needs Hopper or later; it is the
// only NVML source of per MIG instance SM utilization.
//
// It opens libnvidia-ml through purego, which links the binary to libc even
// with CGO_ENABLED=0. The collector itself must stay statically linked, so
// NVML lives in this separate executable and the collector only reads its
// output.
//
// Usage: aio11y-gpm [-interval 1s]
//
// Exit status 0: gpm.Output JSON on standard output. When GPM cannot be used
// (no library, missing symbols, no supporting device) results is empty and
// skipped says why.
// Exit status 1: the measurement or writing the output failed.
// Exit status 2: bad arguments.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cloud-barista/ai-o11y/otelcol/cmd/aio11y-gpm/gpm"
)

// noTargets is the skipped reason when NVML opened but nothing sampled.
const noTargets = "no device or MIG instance supports GPM"

func main() {
	fs := flag.NewFlagSet("aio11y-gpm", flag.ContinueOnError)
	interval := fs.Duration("interval", time.Second, "time between the two GPM samples; must be more than 100ms")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", fs.Args())
		os.Exit(2)
	}
	if *interval <= 100*time.Millisecond {
		fmt.Fprintln(os.Stderr, "-interval must be more than 100ms")
		os.Exit(2)
	}
	if err := run(os.Stdout, openGPM, func() { time.Sleep(*interval) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run opens NVML, takes one GPM measurement and writes it to w as JSON.
// Failing to open NVML is reported in Skipped, not as an error.
func run(w io.Writer, open func() (gpm.Lib, error), sleep func()) error {
	out := gpm.Output{Results: []gpm.Result{}}
	lib, err := open()
	if err != nil {
		out.Skipped = err.Error()
	} else {
		if res := gpm.Sample(lib, sleep); len(res) > 0 {
			out.Results = res
		} else {
			out.Skipped = noTargets
		}
		lib.Shutdown()
	}
	return json.NewEncoder(w).Encode(out)
}
