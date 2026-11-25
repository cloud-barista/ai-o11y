package nvidiagpureceiver

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/scraper/scrapererror"
)

// xidMetric counts the XID errors the NVIDIA driver logged to the kernel log.
// It is named after where it comes from, the kernel log, not nvidia-smi.
const xidMetric = "nvidia_kmsg.xid.errors"

// gpuScraper turns a source's samples into gauges named namespace.field, the
// XIDs counted, when it counts them, into xidMetric, and the GPM values, when
// it reads them, into nvml.<field>.
type gpuScraper struct {
	source Source
	now    func() time.Time
	// xid counts XID lines; nil when XIDs are not counted.
	xid *xidCounter
	// gpm reads NVML GPM through aio11y-gpm; nil when GPM is not read.
	gpm *gpmReader
	// uuids maps the PCI addresses of the GPUs last read to their uuid.
	uuids map[pciKey]string
}

// start starts XID counting and finds the GPM helper, when they are on.
func (s *gpuScraper) start(ctx context.Context, host component.Host) error {
	if s.xid != nil {
		if err := s.xid.Start(ctx, host); err != nil {
			return err
		}
	}
	if s.gpm != nil {
		return s.gpm.Start(ctx, host)
	}
	return nil
}

func (s *gpuScraper) shutdown(ctx context.Context) error {
	if s.xid != nil {
		return s.xid.Shutdown(ctx)
	}
	return nil
}

func (s *gpuScraper) scrape(ctx context.Context) (pmetric.Metrics, error) {
	md := pmetric.NewMetrics()
	// GPM waits a sample interval, so it runs alongside the source.
	var gpmResults []gpmResult
	var wg sync.WaitGroup
	if s.gpm != nil {
		wg.Go(func() { gpmResults = s.gpm.Read(ctx) })
	}
	gpus, err := s.source.Read(ctx)
	wg.Wait()
	if err != nil && s.xid == nil && len(gpmResults) == 0 {
		return md, err
	}
	ts := pcommon.NewTimestampFromTime(s.now())
	sm := md.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("github.com/cloud-barista/ai-o11y/otelcol/receiver/nvidiagpureceiver")
	if err == nil {
		s.learn(gpus)
	}
	byName := map[string]pmetric.Gauge{}
	for _, g := range gpus {
		for _, v := range g.Values {
			name := s.source.Namespace() + "." + v.Field
			gauge, ok := byName[name]
			if !ok {
				m := sm.Metrics().AppendEmpty()
				m.SetName(name)
				m.SetUnit(v.Unit)
				gauge = m.SetEmptyGauge()
				byName[name] = gauge
			}
			dp := gauge.DataPoints().AppendEmpty()
			dp.SetTimestamp(ts)
			dp.SetDoubleValue(v.Value)
			for k, a := range g.Attributes {
				dp.Attributes().PutStr(k, a)
			}
		}
	}
	appendGPM(sm, ts, gpmResults, gpus)
	// XIDs are reported even when the source fails: a GPU fallen off the bus
	// (XID 79) is when nvidia-smi fails.
	s.appendXID(sm, ts)
	if err != nil {
		if sm.Metrics().Len() == 0 {
			return pmetric.NewMetrics(), err
		}
		return md, scrapererror.NewPartialScrapeError(err, 0)
	}
	return md, nil
}

// learn keeps the PCI address -> uuid table of the GPUs just read.
func (s *gpuScraper) learn(gpus []GPU) {
	uuids := map[pciKey]string{}
	for _, g := range gpus {
		if k, ok := parsePCI(g.PCIBusID); ok && g.Attributes["uuid"] != "" {
			uuids[k] = g.Attributes["uuid"]
		}
	}
	s.uuids = uuids
}

// appendXID writes every XID counted so far, one point per GPU and XID
// number: uuid when the address is a GPU the source read, the address as the
// kernel wrote it (pci.bus_id) otherwise.
func (s *gpuScraper) appendXID(sm pmetric.ScopeMetrics, ts pcommon.Timestamp) {
	if s.xid == nil {
		return
	}
	counts := s.xid.snapshot()
	if len(counts) == 0 {
		return
	}
	m := sm.Metrics().AppendEmpty()
	m.SetName(xidMetric)
	m.SetUnit("{error}")
	sum := m.SetEmptySum()
	sum.SetIsMonotonic(true)
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	for _, c := range counts {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetTimestamp(ts)
		dp.SetIntValue(c.n)
		dp.Attributes().PutInt("xid", c.xid)
		if uuid, ok := s.uuids[c.pci]; ok {
			dp.Attributes().PutStr("uuid", uuid)
		} else {
			dp.Attributes().PutStr("pci.bus_id", c.raw)
		}
	}
}
