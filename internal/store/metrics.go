package store

import (
	"context"
	"time"
)

// DefaultMetricRange is what the graphs open on.
const DefaultMetricRange = "1h"

// metricRange is one option under the graphs.
type metricRange struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Hours  int    `json:"-"`
	Points int    `json:"-"`
}

// MetricRanges are the windows the dashboard offers. Every one of them is
// averaged into the same number of points, so a month costs the same handful of
// rows to render as an hour does.
var MetricRanges = []metricRange{
	{Key: "1h", Label: "Last hour", Hours: 1, Points: 60},
	{Key: "6h", Label: "6 hours", Hours: 6, Points: 72},
	{Key: "24h", Label: "24 hours", Hours: 24, Points: 96},
	{Key: "7d", Label: "7 days", Hours: 24 * 7, Points: 84},
	{Key: "30d", Label: "30 days", Hours: 24 * 30, Points: 90},
}

// ValidMetricRange reports whether a range key is one of them.
func ValidMetricRange(key string) bool {
	for _, window := range MetricRanges {
		if window.Key == key {
			return true
		}
	}

	return false
}

func findMetricRange(key string) metricRange {
	for _, window := range MetricRanges {
		if window.Key == key {
			return window
		}
	}

	return MetricRanges[0]
}

// RecordMetrics stores one sample for the graphs. Called once a minute by the
// housekeeper.
func (s *Store) RecordMetrics(cpu, memory, disk, swap, load float64, memoryUsed, memoryTotal, diskUsed, diskTotal int64) {
	cpuValue := any(nil)
	if cpu >= 0 {
		cpuValue = cpu
	}

	s.db.Exec(
		`INSERT INTO metric_samples
		    (cpu_percent, memory_percent, disk_percent, swap_percent,
		     memory_used, memory_total, disk_used, disk_total, load_1, sampled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cpuValue, memory, disk, swap, memoryUsed, memoryTotal, diskUsed, diskTotal, load,
		time.Now().UTC().Format("2006-01-02 15:04:05"))
}

// PruneMetrics deletes samples older than the longest window the graphs offer.
func (s *Store) PruneMetrics() {
	s.db.Exec(`DELETE FROM metric_samples WHERE sampled_at < datetime('now', '-31 days')`)
}

// MetricHistory is the recorded history for the graphs, averaged into buckets.
func (s *Store) MetricHistory(ctx context.Context, key string) map[string]any {
	window := findMetricRange(key)

	// One bucket per (range / points) minutes. The division happens in SQL so
	// that a month of samples is grouped by the database rather than carried
	// into Go a row at a time.
	minutes := window.Hours * 60 / window.Points
	if minutes < 1 {
		minutes = 1
	}

	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT strftime('%Y-%m-%dT%H:%M:00Z',
		            datetime((strftime('%s', sampled_at) / (? * 60)) * (? * 60), 'unixepoch')) AS bucket,
		        avg(cpu_percent), avg(memory_percent), avg(disk_percent), avg(swap_percent), avg(load_1)
		   FROM metric_samples
		  WHERE sampled_at >= datetime('now', ?)
		  GROUP BY bucket
		  ORDER BY bucket`,
		minutes, minutes, "-"+itoa(int64(window.Hours))+" hours")
	if err != nil {
		return map[string]any{"range": window.Key, "points": []any{}}
	}
	defer rows.Close()

	points := []map[string]any{}

	for rows.Next() {
		var bucket string
		var cpu, memory, disk, swap, load nullFloat

		if err := rows.Scan(&bucket, &cpu, &memory, &disk, &swap, &load); err != nil {
			continue
		}

		points = append(points, map[string]any{
			"sampled_at":     bucket,
			"cpu_percent":    round(cpu),
			"memory_percent": round(memory),
			"disk_percent":   round(disk),
			"swap_percent":   round(swap),
			"load_1":         round(load),
		})
	}

	return map[string]any{"range": window.Key, "points": points}
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

func round(value nullFloat) any {
	if !value.Valid {
		return nil
	}

	return float64(int(value.Value*10+0.5)) / 10
}

func itoa(value int64) string {
	digits := ""
	negative := value < 0

	if negative {
		value = -value
	}

	for {
		digits = string(rune('0'+value%10)) + digits
		value /= 10

		if value == 0 {
			break
		}
	}

	if negative {
		return "-" + digits
	}

	return digits
}
