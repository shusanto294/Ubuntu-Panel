package system

import (
	"testing"
	"time"
)

// Nobody using the panel: the CPU and memory are still read, but no process
// table is kept and no helper runs, so the minute has no breakdown.
func TestSamplerUnwatchedReadsNoProcesses(t *testing.T) {
	watched := true
	s := Sampler{Watched: func() bool { return watched }}

	s.Tick()
	time.Sleep(minCPUWindow + 50*time.Millisecond)
	s.Tick()

	watched = false
	s.Tick()

	if s.procs != nil {
		t.Fatal("the process table was kept while nobody was watching")
	}

	s.cwds.mu.Lock()
	running := s.cwds.running && s.cwds.cmd != nil
	s.cwds.mu.Unlock()

	if running {
		t.Fatal("the helper is still running while nobody is watching")
	}

	window := s.Take()

	if window.Spike != nil || window.Memory != nil {
		t.Fatalf("an unwatched minute has a breakdown: %+v", window)
	}
}
