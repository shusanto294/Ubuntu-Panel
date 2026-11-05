package system

import (
	"testing"
	"time"
)

func fakeCommand(pid int, name string) string {
	if pid == 20 || pid == 21 {
		return "php-fpm: pool example.com"
	}
	return "/usr/bin/" + name
}

// Workers of one pool are one line, named for the pool, and the shares are
// of the whole machine — two cores, so one busy core is 50%.
func TestSpikeGroupsAPoolAndSharesTheMachine(t *testing.T) {
	before := map[int]procTicks{
		20: {ticks: 1000, name: "php-fpm8.3"},
		21: {ticks: 1000, name: "php-fpm8.3"},
		30: {ticks: 500, name: "mariadbd"},
	}
	after := map[int]procTicks{
		20: {ticks: 1060, name: "php-fpm8.3"}, // 0.6 s
		21: {ticks: 1040, name: "php-fpm8.3"}, // 0.4 s
		30: {ticks: 520, name: "mariadbd"},    // 0.2 s
	}

	spike := spikeOf(before, after, time.Second, 2, 70, time.Unix(100, 0), fakeCommand)

	if len(spike.Top) != 2 {
		t.Fatalf("top is %+v", spike.Top)
	}

	pool := spike.Top[0]
	if pool.Name != "php-fpm: pool example.com" || pool.Processes != 2 || pool.CPU != 50 {
		t.Fatalf("pool is %+v", pool)
	}

	if db := spike.Top[1]; db.Name != "mariadbd" || db.CPU != 10 {
		t.Fatalf("database is %+v", db)
	}
}

// A program that started and exited inside the second is counted on the
// parent that waited for it, and said to be finished.
func TestSpikeCountsFinishedChildrenOnTheParent(t *testing.T) {
	before := map[int]procTicks{1: {ticks: 10, children: 0, name: "ubuntu-panel"}}
	after := map[int]procTicks{1: {ticks: 12, children: 80, name: "ubuntu-panel"}}

	spike := spikeOf(before, after, time.Second, 2, 41, time.Unix(100, 0), fakeCommand)

	if len(spike.Top) != 1 || spike.Top[0].CPU != 41 || spike.Top[0].Finished != 40 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

// A child that had been running a long time and was reaped in this second
// brings its whole life into the parent's children time; only what it used
// in this second may be counted.
func TestSpikeDoesNotCountALongChildsWholeLife(t *testing.T) {
	before := map[int]procTicks{
		1:  {ticks: 10, children: 0, name: "php-fpm8.3"},
		50: {ticks: 360000, parent: 1, name: "php-fpm8.3"}, // an hour
	}
	after := map[int]procTicks{
		1: {ticks: 10, children: 360010, name: "php-fpm8.3"}, // the hour, plus 0.1 s
	}

	spike := spikeOf(before, after, time.Second, 2, 55, time.Unix(100, 0), fakeCommand)

	if len(spike.Top) != 1 || spike.Top[0].CPU != 5 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

func TestChildTicks(t *testing.T) {
	stat := "1234 (php-fpm: pool (www)) S 77 1234 1234 0 -1 4194624 500 0 0 0 250 50 30 12 20 0 1 0 100 300000000 2560 18446744073709551615"

	children, parent := childTicks(stat)
	if children != 42 || parent != 77 {
		t.Fatalf("children %d, parent %d", children, parent)
	}
}

// The window carries the busiest second it saw, and taking it starts again.
func TestSamplerKeepsTheBusiestSpike(t *testing.T) {
	var s Sampler

	s.mu.Lock()
	s.spike = &Spike{CPU: 80}
	s.mu.Unlock()

	if window := s.Take(); window.Spike == nil || window.Spike.CPU != 80 {
		t.Fatalf("window is %+v", window)
	}

	if next := s.Take(); next.Spike != nil {
		t.Fatalf("an empty window has %+v", next.Spike)
	}
}
