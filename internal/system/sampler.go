package system

import "sync"

// Sampler watches CPU and memory once a second between two recordings, so a
// recording can say what the minute averaged and how high it went.
//
// One reading a minute is a snapshot of one instant: a spike that the live
// figure showed at 100% for twenty seconds falls between two of them and is
// never written down.
type Sampler struct {
	cpu CPUMeter

	mu     sync.Mutex
	cpuSum float64
	cpuMax float64
	cpuN   int
	memSum float64
	memMax float64
	memN   int
}

// Window is what a sampler saw since it was last taken. A figure is -1 when
// there was no reading of it.
type Window struct {
	CPUAverage    float64
	CPUPeak       float64
	MemoryAverage float64
	MemoryPeak    float64
}

// Tick takes one reading.
func (s *Sampler) Tick() {
	cpu := s.cpu.Read()

	var memory Metrics
	readMemory(&memory)

	s.observe(cpu, memory.MemoryPercent, memory.MemoryTotal > 0)
}

func (s *Sampler) observe(cpu, memory float64, memoryKnown bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cpu >= 0 {
		s.cpuSum += cpu
		s.cpuN++
		if cpu > s.cpuMax {
			s.cpuMax = cpu
		}
	}

	if memoryKnown {
		s.memSum += memory
		s.memN++
		if memory > s.memMax {
			s.memMax = memory
		}
	}
}

// Take returns the window and starts the next one.
func (s *Sampler) Take() Window {
	s.mu.Lock()
	defer s.mu.Unlock()

	window := Window{CPUAverage: -1, CPUPeak: -1, MemoryAverage: -1, MemoryPeak: -1}

	if s.cpuN > 0 {
		window.CPUAverage, window.CPUPeak = s.cpuSum/float64(s.cpuN), s.cpuMax
	}

	if s.memN > 0 {
		window.MemoryAverage, window.MemoryPeak = s.memSum/float64(s.memN), s.memMax
	}

	s.cpuSum, s.cpuMax, s.cpuN = 0, 0, 0
	s.memSum, s.memMax, s.memN = 0, 0, 0

	return window
}
