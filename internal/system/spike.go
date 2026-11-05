package system

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SpikeAt is the CPU reading from which a second's busiest processes are
// written down. Below it the machine is not busy enough for the list to say
// anything; at it and above, the list is what explains the graph.
const SpikeAt = 50.0

// spikeTop is how many processes a spike keeps.
const spikeTop = 5

// Spike is one second of the CPU at or above SpikeAt, and what was using it.
type Spike struct {
	At  int64          `json:"at"`  // unix seconds
	CPU float64        `json:"cpu"` // percent of the whole machine
	Top []SpikeProcess `json:"top"`
}

// SpikeProcess is one program in a spike: every process of that name added
// up, so twelve php-fpm workers are one line. A PHP-FPM pool is its own line,
// named for the pool — which is the site.
type SpikeProcess struct {
	Name      string  `json:"name"`
	Command   string  `json:"command"`   // the busiest one's command line
	Processes int     `json:"processes"` // how many were busy
	CPU       float64 `json:"cpu"`       // percent of the whole machine, like the graph

	// Finished is the part of CPU that came from programs these started and
	// that had already exited by the end of the second — a sudo, a
	// fail2ban-client, a cron job's shell. They are gone before anything can
	// see them, but the kernel adds their time to the parent that waited for
	// them, which is where they are counted.
	Finished float64 `json:"finished"`
}

// procTicks is a process's CPU time so far and its name, read from
// /proc/<pid>/stat alone — the cheapest read there is, since the recorder
// does it every second.
type procTicks struct {
	ticks    uint64 // its own
	children uint64 // its finished children's, which the kernel adds on wait
	parent   int
	name     string
}

func readProcTicks() map[int]procTicks {
	out := map[int]procTicks{}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}

		if sample, ok := parseStat(string(stat), 0); ok {
			children, parent := childTicks(string(stat))
			out[pid] = procTicks{ticks: sample.ticks, children: children, parent: parent, name: sample.name}
		}
	}

	return out
}

// spikeOf is what used the CPU between two readings of the process table,
// elapsed apart, on a machine of cores.
func spikeOf(before, after map[int]procTicks, elapsed time.Duration, cores int, cpu float64,
	at time.Time, command func(pid int, name string) string) Spike {
	spike := Spike{At: at.Unix(), CPU: round(cpu), Top: []SpikeProcess{}}

	seconds := elapsed.Seconds()
	if seconds <= 0 || cores <= 0 {
		return spike
	}

	type group struct {
		SpikeProcess
		busiest    float64
		busiestPID int
	}

	groups := map[string]*group{}

	// A child that was already there at the first reading and was reaped by
	// the second lands in its parent's children time whole — its whole life,
	// not just this second. What it had used by the first reading was counted
	// then (or before), so it is taken back off the parent here. A php-fpm
	// worker recycled after an hour would otherwise be an hour of CPU in one
	// second of its master.
	counted := map[int]uint64{}
	for pid, then := range before {
		if _, alive := after[pid]; !alive {
			counted[then.parent] += then.ticks + then.children
		}
	}

	for pid, now := range after {
		then, ok := before[pid]
		if !ok {
			continue
		}

		own, finished := delta(then.ticks, now.ticks), delta(then.children+counted[pid], now.children)
		if own+finished == 0 {
			continue
		}

		// Percent of the whole machine, so the lines add up to the graph.
		share := func(ticks uint64) float64 {
			return float64(ticks) / clockTicks / seconds / float64(cores) * 100
		}
		used := share(own + finished)

		key := now.name
		line := ""
		if strings.HasPrefix(now.name, "php-fpm") {
			// The pool is in the command line, not the name: "php-fpm: pool
			// example.com". One line per pool is one line per site.
			line = command(pid, now.name)
			key = line
		}

		g := groups[key]
		if g == nil {
			g = &group{SpikeProcess: SpikeProcess{Name: now.name}}
			if line != "" {
				g.Name = line
			}
			groups[key] = g
		}

		g.Processes++
		g.CPU += used
		g.Finished += share(finished)

		if used > g.busiest {
			g.busiest, g.busiestPID = used, pid
		}
	}

	list := make([]*group, 0, len(groups))
	for _, g := range groups {
		list = append(list, g)
	}

	sort.Slice(list, func(i, j int) bool { return list[i].CPU > list[j].CPU })

	if len(list) > spikeTop {
		list = list[:spikeTop]
	}

	for _, g := range list {
		g.CPU = round(min(100, g.CPU))
		g.Finished = round(min(g.CPU, g.Finished))
		// The command line only for the few kept: a file read each.
		g.Command = command(g.busiestPID, g.Name)
		spike.Top = append(spike.Top, g.SpikeProcess)
	}

	return spike
}

func delta(then, now uint64) uint64 {
	if now <= then {
		return 0
	}

	return now - then
}

// childTicks is cutime + cstime from /proc/<pid>/stat — the CPU time of the
// children it has waited for — and its parent's pid. Fields 16, 17 and 4:
// [13], [14] and [1] counted from the state after the name, as parseStat
// counts.
func childTicks(stat string) (uint64, int) {
	closing := strings.LastIndexByte(stat, ')')
	if closing < 0 {
		return 0, 0
	}

	fields := strings.Fields(stat[closing+1:])
	if len(fields) < 15 {
		return 0, 0
	}

	parent, _ := strconv.Atoi(fields[1])
	cutime, _ := strconv.ParseUint(fields[13], 10, 64)
	cstime, _ := strconv.ParseUint(fields[14], 10, 64)

	return cutime + cstime, parent
}
