package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func metricStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return st
}

// The graphs read these keys by name. A reshuffle here is a dashboard that
// renders three empty boxes and says nothing about why, so the shape is pinned
// rather than left to whoever edits the query next.
func TestHistoryHasTheShapeTheGraphsRead(t *testing.T) {
	st := metricStore(t)

	st.RecordMetrics(MetricSample{CPU: 12.5, Memory: 40, Disk: 57.1, Load: 0.4,
		MemoryUsed: 400, MemoryTotal: 1000, DiskUsed: 570, DiskTotal: 1000})

	history := st.MetricHistory(context.Background(), DefaultMetricRange)

	for _, key := range []string{"range", "label", "bucket_seconds", "from", "to", "points"} {
		if _, ok := history[key]; !ok {
			t.Errorf("history is missing %q", key)
		}
	}

	points, ok := history["points"].([]map[string]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points is %#v", history["points"])
	}

	for _, key := range []string{"t", "cpu", "cpu_peak", "memory", "memory_peak", "disk",
		"memory_used", "memory_total", "disk_used", "disk_total"} {
		if _, ok := points[0][key]; !ok {
			t.Errorf("a point is missing %q", key)
		}
	}

	if points[0]["cpu"] != 12.5 || points[0]["disk"] != 57.1 {
		t.Fatalf("the averages did not survive: %#v", points[0])
	}

	// Unix seconds, which is what the graph plots against — not a formatted
	// timestamp it would have to parse.
	if _, ok := points[0]["t"].(int64); !ok {
		t.Fatalf("t is %#v, want unix seconds", points[0]["t"])
	}
}

// A CPU reading with nothing to compare against is null, not zero: a zero draws
// a graph saying the machine was idle when it was only just asked.
func TestAFirstCPUReadingIsRecordedAsNull(t *testing.T) {
	st := metricStore(t)

	st.RecordMetrics(MetricSample{CPU: -1, Memory: 40, Disk: 57, Load: 0.4,
		MemoryUsed: 400, MemoryTotal: 1000, DiskUsed: 570, DiskTotal: 1000})

	points := st.MetricHistory(context.Background(), DefaultMetricRange)["points"].([]map[string]any)

	if points[0]["cpu"] != nil {
		t.Fatalf("cpu is %#v, want null", points[0]["cpu"])
	}

	if points[0]["memory"] == nil {
		t.Fatal("memory went null with it")
	}
}

// Samples inside one bucket are averaged into a single point.
func TestSamplesInOneBucketAverage(t *testing.T) {
	st := metricStore(t)

	st.RecordMetrics(MetricSample{CPU: 10, Memory: 20, Disk: 30})
	st.RecordMetrics(MetricSample{CPU: 30, Memory: 40, Disk: 50})

	points := st.MetricHistory(context.Background(), DefaultMetricRange)["points"].([]map[string]any)

	if len(points) != 1 {
		t.Fatalf("got %d points, want one bucket", len(points))
	}

	if points[0]["cpu"] != 20.0 || points[0]["memory"] != 30.0 {
		t.Fatalf("averages are %#v", points[0])
	}
}

// A bucket's peak is the highest second of any sample in it, so a spike stays
// visible on the longer ranges where averaging would flatten it.
func TestABucketKeepsItsHighestSecond(t *testing.T) {
	st := metricStore(t)

	st.RecordMetrics(MetricSample{CPU: 4, CPUPeak: 100, Memory: 30, MemoryPeak: 85, Disk: 20})
	st.RecordMetrics(MetricSample{CPU: 2, CPUPeak: 6, Memory: 30, MemoryPeak: 31, Disk: 20})

	for _, window := range MetricRanges {
		points := st.MetricHistory(context.Background(), window.Key)["points"].([]map[string]any)
		if len(points) != 1 {
			t.Fatalf("%s: got %d points", window.Key, len(points))
		}

		if points[0]["cpu"] != 3.0 || points[0]["cpu_peak"] != 100.0 || points[0]["memory_peak"] != 85.0 {
			t.Fatalf("%s: point is %#v", window.Key, points[0])
		}
	}

	// A sample written before peaks were recorded counts its average as its peak.
	st.db.Exec(`UPDATE metric_samples SET cpu_max = NULL, memory_max = NULL`)

	points := st.MetricHistory(context.Background(), DefaultMetricRange)["points"].([]map[string]any)
	if points[0]["cpu_peak"] != 4.0 || points[0]["memory_peak"] != 30.0 {
		t.Fatalf("old samples: point is %#v", points[0])
	}
}

func TestEveryRangeIsOfferedWithARefreshInterval(t *testing.T) {
	if len(MetricRanges) < 4 {
		t.Fatalf("only %d ranges", len(MetricRanges))
	}

	for _, window := range MetricRanges {
		if !ValidMetricRange(window.Key) {
			t.Errorf("%q is offered but not accepted", window.Key)
		}

		if window.RefreshMS <= 0 || window.Bucket <= 0 || window.Window <= 0 {
			t.Errorf("%q has an unusable window: %+v", window.Key, window)
		}
	}

	// An unknown range falls back rather than returning an empty graph.
	if history := (&Store{}).MetricHistory; history == nil {
		t.Fatal("unreachable")
	}

	if findMetricRange("nonsense").Key != DefaultMetricRange {
		t.Fatal("an unknown range did not fall back to the default")
	}
}

// A database from when the breakdowns were kept loses them, and starting
// again after that is not an error.
func TestOldBreakdownColumnsAreDropped(t *testing.T) {
	st := metricStore(t)

	for _, column := range []string{"cpu_spike", "memory_usage", "disk_usage"} {
		if _, err := st.db.Exec(`ALTER TABLE metric_samples ADD COLUMN ` + column + ` TEXT`); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 {
		if err := st.migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	var left int
	st.db.QueryRow(`SELECT count(*) FROM pragma_table_info('metric_samples')
	                 WHERE name IN ('cpu_spike', 'memory_usage', 'disk_usage')`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d breakdown columns left", left)
	}
}

// Each point carries what used the CPU and memory in its busiest minute.
func TestHistoryCarriesTheBreakdownOfTheBusiestMinute(t *testing.T) {
	st := metricStore(t)

	st.RecordMetrics(MetricSample{CPU: 10, CPUPeak: 20, Memory: 40,
		TopCPU: `[{"name":"quiet","n":1,"pct":5}]`, TopMemory: `[{"name":"mysql","n":1,"pct":30,"mb":300}]`})
	st.RecordMetrics(MetricSample{CPU: 50, CPUPeak: 95, Memory: 30,
		TopCPU: `[{"name":"busy","n":3,"pct":45}]`})

	window := MetricRanges[1] // 24h: both minutes fall in one 15-minute point
	points := st.MetricHistory(context.Background(), window.Key)["points"].([]map[string]any)
	last := points[len(points)-1]

	if got := string(last["cpu_top"].(json.RawMessage)); got != `[{"name":"busy","n":3,"pct":45}]` {
		t.Errorf("cpu_top = %s, want the busier minute's", got)
	}
	if got := string(last["memory_top"].(json.RawMessage)); got != `[{"name":"mysql","n":1,"pct":30,"mb":300}]` {
		t.Errorf("memory_top = %s, want the minute memory went highest", got)
	}
}
