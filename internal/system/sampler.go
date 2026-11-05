package system

import (
	"sync"
	"time"
)

// Sampler watches CPU and memory once a second between two recordings, so a
// recording can say what the minute averaged and how high it went.
//
// One reading a minute is a snapshot of one instant: a spike that the live
// figure showed at 100% for twenty seconds falls between two of them and is
// never written down.
type Sampler struct {
	cpu CPUMeter

	// The process table at the last reading, so a busy second can say what
	// was busy in it. Only the recorder's goroutine touches these.
	procs   map[int]procTicks
	procsAt time.Time
	cores   int

	mu     sync.Mutex
	cpuSum float64
	cpuMax float64
	cpuN   int
	memSum float64
	memMax float64
	memN   int

	// spike is the minute's busiest second at or above SpikeAt, nil if none.
	spike *Spike
}

// Window is what a sampler saw since it was last taken. A figure is -1 when
// there was no reading of it.
type Window struct {
	CPUAverage    float64
	CPUPeak       float64
	MemoryAverage float64
	MemoryPeak    float64

	// Spike is the window's busiest second at or above SpikeAt and what was
	// using it; nil when the CPU never got that high.
	Spike *Spike
}

// Tick takes one reading.
func (s *Sampler) Tick() {
	cpu := s.cpu.Read()
	s.watchProcesses(cpu)

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

// watchProcesses reads the process table beside the CPU reading — the same
// second, since both are measured from the reading before — and, when the
// CPU was at SpikeAt or above, writes down what was using it.
//
// Reading the table is a stat file per process, which is a few milliseconds
// every second: what htop does, and the only way to know afterwards what a
// one-second spike was.
func (s *Sampler) watchProcesses(cpu float64) {
	now := time.Now()

	// A reading too soon after the last (see minCPUWindow) is the CPU meter
	// repeating itself; the table is left to measure the whole second.
	if !s.procsAt.IsZero() && now.Sub(s.procsAt) < minCPUWindow {
		return
	}

	current := readProcTicks()
	previous, elapsed := s.procs, now.Sub(s.procsAt)
	s.procs, s.procsAt = current, now

	if previous == nil || cpu < SpikeAt {
		return
	}

	s.mu.Lock()
	busier := s.spike == nil || cpu > s.spike.CPU
	s.mu.Unlock()

	if !busier {
		return
	}

	if s.cores == 0 {
		s.cores = cores()
	}

	spike := spikeOf(previous, current, elapsed, s.cores, cpu, now, commandLine)

	s.mu.Lock()
	s.spike = &spike
	s.mu.Unlock()
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

	window.Spike, s.spike = s.spike, nil

	s.cpuSum, s.cpuMax, s.cpuN = 0, 0, 0
	s.memSum, s.memMax, s.memN = 0, 0, 0

	return window
}
