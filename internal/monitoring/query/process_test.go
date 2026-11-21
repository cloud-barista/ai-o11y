package query

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// TestProcessRows runs process.cpu.utilization and process.memory.utilization
// on series as the backend stores what the top_process processor sends: the
// process scraper's points with process.executable.name and process.pid,
// stored as process_executable_name and process_pid, CPU split by state and
// memory in percent. The fields add up the processes of one name, and the
// answer carries the name under process.executable.name.
func TestProcessRows(t *testing.T) {
	st := promqltest.LoadedStorage(t, `load 30s
  {__name__="process_cpu_utilization_ratio",entity_id="a",process_executable_name="burn",process_pid="1",state="user"} 0.2x20
  {__name__="process_cpu_utilization_ratio",entity_id="a",process_executable_name="burn",process_pid="1",state="system"} 0.1x20
  {__name__="process_cpu_utilization_ratio",entity_id="a",process_executable_name="burn",process_pid="2",state="user"} 0.05x20
  {__name__="process_cpu_utilization_ratio",entity_id="a",process_executable_name="python3",process_pid="3",state="user"} 0.3x20
  {__name__="process_cpu_utilization_ratio",entity_id="b",process_executable_name="burn",process_pid="1",state="user"} 0.9x20
  {__name__="process_memory_utilization_ratio",entity_id="a",process_executable_name="burn",process_pid="1"} 10x20
  {__name__="process_memory_utilization_ratio",entity_id="a",process_executable_name="burn",process_pid="2"} 5x20
  {__name__="process_memory_utilization_ratio",entity_id="a",process_executable_name="python3",process_pid="3"} 20x20
`)
	t.Cleanup(func() { _ = st.Close() })
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st,
		at: time.Unix(0, 0).UTC().Add(9 * time.Minute)}
	r := testRenderer(t)
	p := Planner{Renderer: r, Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner, Now: runner.now}
	for metric, want := range map[string]map[string]float64{
		"process.cpu.utilization":    {"burn": 0.35, "python3": 0.3},
		"process.memory.utilization": {"burn": 0.15, "python3": 0.2},
	} {
		row, err := r.Catalog.Lookup(metric)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Query(context.Background(), monitoring.QuerySpec{Metric: metric, Targets: targets("a")})
		if err != nil {
			t.Fatalf("%s: %v", metric, err)
		}
		byName := map[string]float64{}
		for _, s := range got {
			labels := ToAttributeNames(row, s.Labels)
			if labels["entity_id"] != "a" {
				t.Errorf("%s: series of another target: %v", metric, labels)
			}
			byName[labels["process.executable.name"]] = s.Points[len(s.Points)-1].V
		}
		if len(byName) != len(want) {
			t.Errorf("%s by process.executable.name = %v, want %v", metric, byName, want)
		}
		for name, v := range want {
			if g, ok := byName[name]; !ok || math.Abs(g-v) > 1e-9 {
				t.Errorf("%s[%s] = %v, want %v", metric, name, g, v)
			}
		}
	}
}
