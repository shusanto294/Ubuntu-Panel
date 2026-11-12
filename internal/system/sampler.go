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

	// Sites lists the sites, so what a process does for one is shown as that
	// site rather than as "php-fpm: pool www". Read once a minute; nil leaves
	// every process under its program's name.
	Sites func() []SiteRoot

	sites    siteIndex
	sitesAt  time.Time
	cwds     cwdFeed
	lastSite map[int]string // the site a process was last seen working for
	memoryMB float64

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

	// spike is the minute's busiest second, nil before there was a second to
	// compare against.
	spike *Spike
}

// Window is what a sampler saw since it was last taken. A figure is -1 when
// there was no reading of it.
type Window struct {
	CPUAverage    float64
	CPUPeak       float64
	MemoryAverage float64
	MemoryPeak    float64

	// Spike is the window's busiest second and what was using it.
	Spike *Spike

	// Memory is what was holding memory at the end of the window; nil when
	// the process table could not be read.
	Memory *Usage
}

// Tick takes one reading.
func (s *Sampler) Tick() {
	cpu := s.cpu.Read()
	s.watchProcesses(cpu)

	var memory Metrics
	readMemory(&memory)

	s.memoryMB = memory.MemoryTotal / (1 << 20)

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
// second is the busiest of the minute so far, writes down what was using it.
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

	shares := s.watchSites(now, current)

	if previous == nil || cpu < 0 {
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

	spike := spikeOf(previous, current, elapsed, s.cores, cpu, now, commandLine,
		func(pid int) []siteShare { return shares[pid] })

	s.mu.Lock()
	s.spike = &spike
	s.mu.Unlock()
}

// watchSites is which sites each process in the table was working for in
// the last second, and remembers it: a PHP worker between two requests is
// back in / and is still the last site's until it works for another.
func (s *Sampler) watchSites(now time.Time, procs map[int]procTicks) map[int][]siteShare {
	if s.Sites == nil {
		return nil
	}

	if s.sites == nil || now.Sub(s.sitesAt) >= time.Minute {
		s.sites, s.sitesAt = newSiteIndex(s.Sites()), now
	}

	s.cwds.start()

	if s.lastSite == nil {
		s.lastSite = map[int]string{}
	}

	for pid := range s.lastSite {
		if _, alive := procs[pid]; !alive {
			delete(s.lastSite, pid)
		}
	}

	shares := map[int][]siteShare{}

	for pid, directories := range s.cwds.snapshot() {
		if found := siteShares(directories, s.sites); len(found) > 0 {
			shares[pid] = found
			s.lastSite[pid] = found[0].domain
		}
	}

	for pid, domain := range s.lastSite {
		if _, ok := shares[pid]; !ok {
			shares[pid] = []siteShare{{domain: domain, weight: 1}}
		}
	}

	return shares
}

// Take returns the window and starts the next one.
func (s *Sampler) Take() Window {
	window := s.take()

	// Only the recorder's goroutine touches the table and the sites, which
	// is the one that takes.
	if s.procs != nil {
		memory := memoryOf(s.procs, s.memoryMB, time.Now(), func(pid int) string { return s.lastSite[pid] })
		window.Memory = &memory
	}

	return window
}

func (s *Sampler) take() Window {
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
