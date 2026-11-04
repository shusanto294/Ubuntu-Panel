package system

import "testing"

// A short spike survives into the minute's peak even though the average
// hardly moves — which is the spike a once-a-minute snapshot used to miss.
func TestSamplerKeepsTheMinutesPeak(t *testing.T) {
	var s Sampler

	for i := 0; i < 59; i++ {
		s.observe(2, 30, true)
	}
	s.observe(100, 80, true)

	window := s.Take()

	if window.CPUPeak != 100 || window.MemoryPeak != 80 {
		t.Fatalf("peaks are %+v", window)
	}

	if window.CPUAverage < 3 || window.CPUAverage > 4 {
		t.Fatalf("average is %v, want about 3.6", window.CPUAverage)
	}

	// Taking starts a new window.
	if next := s.Take(); next.CPUPeak != -1 || next.MemoryAverage != -1 {
		t.Fatalf("an empty window is %+v", next)
	}
}

// The first CPU reading has nothing to compare against and is left out,
// rather than counted as an idle second.
func TestSamplerSkipsCPUReadingsItCannotMake(t *testing.T) {
	var s Sampler

	s.observe(-1, 40, true)
	s.observe(50, 40, true)

	if window := s.Take(); window.CPUAverage != 50 {
		t.Fatalf("average is %v, want 50", window.CPUAverage)
	}
}

func TestParseStat(t *testing.T) {
	stat := "1234 (php-fpm: pool (www)) S 1 1234 1234 0 -1 4194624 500 0 0 0 250 50 0 0 20 0 1 0 100 300000000 2560 18446744073709551615"

	sample, ok := parseStat(stat, 4096.0/(1<<20))
	if !ok {
		t.Fatal("did not parse")
	}

	if sample.name != "php-fpm: pool (www)" || sample.ticks != 300 || sample.rssMB != 10 {
		t.Fatalf("parsed %+v", sample)
	}
}
