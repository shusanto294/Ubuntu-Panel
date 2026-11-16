package store

import (
	"context"
	"math"
	"strconv"
	"time"
)

// DefaultMetricRange is what the graphs open on.
const DefaultMetricRange = "1h"

// MetricRange is one option under the graphs.
//
// The bucket is chosen so each range lands near a hundred points: reading a
// month back a sample at a time would be 43,000 rows for a graph 300 pixels
// wide, and anything denser than a hundred points is finer than the graph can
// draw.
type MetricRange struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Window int    `json:"-"` // seconds
	Bucket int    `json:"-"` // seconds a point covers

	// RefreshMS is how often a graph on this range is worth refetching. There
	// is no point asking again before the next bucket can have moved.
	RefreshMS int `json:"refresh_ms"`
}

// MetricRanges are the windows the dashboard offers, in the order it renders
// them.
var MetricRanges = []MetricRange{
	{Key: "1h", Label: "1 hour", Window: 3600, Bucket: 60, RefreshMS: 60_000},
	{Key: "24h", Label: "24 hours", Window: 86400, Bucket: 900, RefreshMS: 60_000},
	{Key: "7d", Label: "7 days", Window: 604800, Bucket: 3600, RefreshMS: 60_000},
	{Key: "30d", Label: "30 days", Window: 2592000, Bucket: 21600, RefreshMS: 60_000},
}

// retainDays is how long a sample is kept; the longest range is 30 days.
const retainDays = 35

// ValidMetricRange reports whether a range key is one of them.
func ValidMetricRange(key string) bool {
	for _, window := range MetricRanges {
		if window.Key == key {
			return true
		}
	}

	return false
}

func findMetricRange(key string) MetricRange {
	for _, window := range MetricRanges {
		if window.Key == key {
			return window
		}
	}

	return MetricRanges[0]
}

// MetricSample is one minute of this machine: CPU and memory as the minute's
// average and its highest one-second reading, the rest as read at the end of
// it. A CPU figure below zero is a minute with no reading to compare against.
type MetricSample struct {
	CPU, CPUPeak       float64
	Memory, MemoryPeak float64
	Disk, Swap, Load   float64

	MemoryUsed, MemoryTotal, DiskUsed, DiskTotal int64
}

// RecordMetrics stores one sample for the graphs. Called once a minute by the
// housekeeper. A CPU percentage of below zero is stored as NULL rather than as
// a zero the graph would draw a floor at.
func (s *Store) RecordMetrics(sample MetricSample) {
	cpu, cpuPeak := any(nil), any(nil)
	if sample.CPU >= 0 {
		cpu = sample.CPU
		cpuPeak = math.Max(sample.CPU, sample.CPUPeak)
	}

	s.db.Exec(
		`INSERT INTO metric_samples
		    (cpu_percent, cpu_max, memory_percent, memory_max, disk_percent, swap_percent,
		     memory_used, memory_total, disk_used, disk_total, load_1, sampled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cpu, cpuPeak, sample.Memory, math.Max(sample.Memory, sample.MemoryPeak),
		sample.Disk, sample.Swap, sample.MemoryUsed, sample.MemoryTotal,
		sample.DiskUsed, sample.DiskTotal, sample.Load,
		time.Now().UTC().Format("2006-01-02 15:04:05"))
}

// PruneMetrics deletes samples past the retention window.
func (s *Store) PruneMetrics() {
	s.db.Exec(`DELETE FROM metric_samples WHERE sampled_at < datetime('now', ?)`,
		"-"+strconv.Itoa(retainDays)+" days")
}

// MetricHistory is the recorded past of this machine's CPU, memory and disk,
// averaged into buckets by the database.
//
// The shape is the one the graphs already read: points oldest first, each with
// a unix timestamp and the three percentages, plus the byte figures the tooltip
// puts under them. `cpu_peak` and `memory_peak` are the highest second inside
// the bucket — the average alone flattens a spike into the minutes around it,
// and the further back the range the flatter it gets. Samples from before
// peaks were recorded fall back to their own average.
func (s *Store) MetricHistory(ctx context.Context, key string) map[string]any {
	window := findMetricRange(key)

	to := time.Now().UTC()
	from := to.Add(-time.Duration(window.Window) * time.Second)

	points := []map[string]any{}

	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT (strftime('%s', sampled_at) / ?) AS bucket,
		        avg(cpu_percent), avg(memory_percent), avg(disk_percent),
		        avg(memory_used), max(memory_total), avg(disk_used), max(disk_total),
		        max(coalesce(cpu_max, cpu_percent)), max(coalesce(memory_max, memory_percent))
		   FROM metric_samples
		  WHERE sampled_at >= ?
		  GROUP BY bucket
		  ORDER BY bucket`,
		window.Bucket, from.Format("2006-01-02 15:04:05"))

	if err == nil {
		defer rows.Close()

		for rows.Next() {
			var bucket int64
			var cpu, memory, disk, memoryUsed, memoryTotal, diskUsed, diskTotal nullFloat
			var cpuPeak, memoryPeak nullFloat

			if err := rows.Scan(&bucket, &cpu, &memory, &disk,
				&memoryUsed, &memoryTotal, &diskUsed, &diskTotal, &cpuPeak, &memoryPeak); err != nil {
				continue
			}

			points = append(points, map[string]any{
				// The start of the bucket, in unix seconds, which is what the
				// graph plots against.
				"t":            bucket * int64(window.Bucket),
				"cpu":          rounded(cpu),
				"cpu_peak":     rounded(cpuPeak),
				"memory":       rounded(memory),
				"memory_peak":  rounded(memoryPeak),
				"disk":         rounded(disk),
				"memory_used":  whole(memoryUsed),
				"memory_total": whole(memoryTotal),
				"disk_used":    whole(diskUsed),
				"disk_total":   whole(diskTotal),
			})
		}
	}

	return map[string]any{
		"range":          window.Key,
		"label":          window.Label,
		"bucket_seconds": window.Bucket,
		"from":           from.Format(time.RFC3339),
		"to":             to.Format(time.RFC3339),
		"points":         points,
	}
}

type nullFloat struct {
	Valid bool
	Value float64
}

// Scan takes the average of a column that may have been NULL for every row in
// the bucket — which is what the first CPU sample after a restart looks like.
func (n *nullFloat) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		n.Valid, n.Value = false, 0
	case float64:
		n.Valid, n.Value = true, typed
	case int64:
		n.Valid, n.Value = true, float64(typed)
	}

	return nil
}

// rounded is a percentage to one decimal, or null where nothing was recorded.
// A gap the graph can see is better than a zero it would draw a line through.
func rounded(value nullFloat) any {
	if !value.Valid {
		return nil
	}

	return float64(int(value.Value*10+0.5)) / 10
}

func whole(value nullFloat) any {
	if !value.Valid {
		return nil
	}

	return int64(value.Value)
}
