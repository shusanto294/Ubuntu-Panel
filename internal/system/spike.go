package system

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// spikeTop is how many lines a breakdown keeps; whatever is left over is one
// more line, "Other", so the lines always add up to the second's figure.
const spikeTop = 6

// kernelTime is the part of a second that no process is charged for, as
// percent of the whole machine: interrupts (irq and softirq — network and
// disk work done by the kernel) and steal (time the host kept for others).
type kernelTime struct {
	interrupts float64
	steal      float64
}

// Spike is a minute's busiest second, and what was using it. Every minute has
// one, however quiet: a graph at 2% is still somebody's 2%.
type Spike struct {
	At  int64          `json:"at"`  // unix seconds
	CPU float64        `json:"cpu"` // percent of the whole machine
	Top []SpikeProcess `json:"top"`
}

// SpikeProcess is one line of a spike: a site, with everything that was
// working for it, or a program, with every process of that name added up —
// twelve mariadbd threads are one line.
type SpikeProcess struct {
	Name      string  `json:"name"`
	Command   string  `json:"command"`   // the busiest one's command line
	Processes int     `json:"processes"` // how many were busy
	CPU       float64 `json:"cpu"`       // percent of the whole machine, like the graph

	// Kernel is set on the lines that are not a process: the kernel's
	// interrupts and the host's steal. Other is set on the line that holds
	// the rest — the programs past the first few, and time no reading could
	// put down to anything (a program that started and was gone between two
	// readings with nobody waiting for it yet).
	Kernel bool `json:"kernel,omitempty"`
	Other  bool `json:"other,omitempty"`

	// Site is set when Name is a site's domain rather than a program, and
	// Programs then says what was running for it: "PHP ×3".
	Site     bool   `json:"site,omitempty"`
	Programs string `json:"programs,omitempty"`

	// Finished is the part of CPU that came from programs these started and
	// that had already exited by the end of the second — a sudo, a
	// fail2ban-client, a cron job's shell. They are gone before anything can
	// see them, but the kernel adds their time to the parent that waited for
	// them, which is where they are counted.
	Finished float64 `json:"finished"`
}

// Usage is what was holding memory or disk at one moment: the biggest sites
// and programs, each as a share of the whole.
type Usage struct {
	At  int64       `json:"at"` // unix seconds
	Top []UsageItem `json:"top"`
}

// UsageItem is one line of a Usage.
type UsageItem struct {
	Name    string  `json:"name"`
	Site    bool    `json:"site,omitempty"`
	Detail  string  `json:"detail,omitempty"`
	Bytes   int64   `json:"bytes"`
	Percent float64 `json:"percent"` // of the whole machine's memory or disk
}

// attribution is which sites a process was working for; nil when it was
// working for none of them.
type attribution func(pid int) []siteShare

// programLabel is how a program is named under a site: what it is, rather
// than what its binary is called.
func programLabel(name string) string {
	switch {
	case strings.HasPrefix(name, "php"):
		return "PHP"
	case name == "node" || strings.HasPrefix(name, "node "):
		return "Node.js"
	}

	return name
}

// programList is "PHP ×3, Node.js", busiest first.
func programList(counts map[string]int, order map[string]float64) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		if order[names[i]] != order[names[j]] {
			return order[names[i]] > order[names[j]]
		}
		return names[i] < names[j]
	})

	parts := make([]string, 0, len(names))
	for _, name := range names {
		if counts[name] > 1 {
			parts = append(parts, name+" ×"+strconv.Itoa(counts[name]))
		} else {
			parts = append(parts, name)
		}
	}

	return strings.Join(parts, ", ")
}

// procTicks is a process's CPU time so far and its name, read from
// /proc/<pid>/stat alone — the cheapest read there is, since the recorder
// does it every second.
type procTicks struct {
	ticks    uint64 // its own
	children uint64 // its finished children's, which the kernel adds on wait
	parent   int
	name     string
	rssMB    float64
}

func readProcTicks() map[int]procTicks {
	out := map[int]procTicks{}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}

	page := float64(os.Getpagesize()) / (1 << 20)

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}

		if sample, ok := parseStat(string(stat), page); ok {
			children, parent := childTicks(string(stat))
			out[pid] = procTicks{ticks: sample.ticks, children: children, parent: parent,
				name: sample.name, rssMB: sample.rssMB}
		}
	}

	return out
}

// spikeOf is what used the CPU between two readings of the process table,
// elapsed apart, on a machine of cores. A process working for a site is put
// down to the site; everything else to its program.
//
// The lines add up to cpu, the second's figure on the graph, to the tenth: a
// breakdown that leaves most of a spike out says nothing about the spike.
func spikeOf(before, after map[int]procTicks, elapsed time.Duration, cores int, cpu float64,
	kernel kernelTime, at time.Time, command func(pid int, name string) string, sites attribution) Spike {
	spike := Spike{At: at.Unix(), CPU: round(cpu), Top: []SpikeProcess{}}

	seconds := elapsed.Seconds()
	if seconds <= 0 || cores <= 0 {
		return spike
	}

	type group struct {
		SpikeProcess
		busiest    float64
		busiestPID int
		programs   map[string]int
		programCPU map[string]float64
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

	add := func(key, name string, site bool, pid int, program string, used, finished float64) {
		g := groups[key]
		if g == nil {
			g = &group{SpikeProcess: SpikeProcess{Name: name, Site: site},
				programs: map[string]int{}, programCPU: map[string]float64{}}
			groups[key] = g
		}

		g.Processes++
		g.CPU += used
		g.Finished += finished
		g.programs[program]++
		g.programCPU[program] += used

		if used > g.busiest {
			g.busiest, g.busiestPID = used, pid
		}
	}

	for pid, now := range after {
		// A process not in the first reading started inside the second, so
		// all of its time so far is this second's: the panel's minute jobs
		// are a sudo started and still running when the second ends.
		then := before[pid]

		own, finished := delta(then.ticks, now.ticks), delta(then.children+counted[pid], now.children)
		if own+finished == 0 {
			continue
		}

		// Percent of the whole machine, so the lines add up to the graph.
		share := func(ticks uint64) float64 {
			return float64(ticks) / clockTicks / seconds / float64(cores) * 100
		}
		used, done := share(own+finished), share(finished)

		var shares []siteShare
		if sites != nil {
			shares = sites(pid)
		}

		if len(shares) > 0 {
			for _, part := range shares {
				add("site:"+part.domain, part.domain, true, pid, programLabel(now.name),
					used*part.weight, done*part.weight)
			}

			continue
		}

		key, name := now.name, now.name
		if strings.HasPrefix(now.name, "php-fpm") {
			// A worker no site could be found for: named for its pool, which
			// is in the command line, not the name — "php-fpm: pool www".
			name = command(pid, now.name)
			key = name
		}

		add(key, name, false, pid, now.name, used, done)
	}

	if kernel.interrupts > 0 {
		groups["kernel:interrupts"] = &group{SpikeProcess: SpikeProcess{Name: "Kernel interrupts",
			Kernel: true, CPU: kernel.interrupts, Command: "network and disk work done by the kernel"}}
	}
	if kernel.steal > 0 {
		groups["kernel:steal"] = &group{SpikeProcess: SpikeProcess{Name: "Steal",
			Kernel: true, CPU: kernel.steal, Command: "time the host gave to other machines"}}
	}

	list := make([]*group, 0, len(groups))
	sum := 0.0
	for _, g := range groups {
		list = append(list, g)
		sum += g.CPU
	}

	// The process table and /proc/stat are read a moment apart, so what the
	// lines found can come to a little more than the figure; they are scaled
	// to it rather than adding up to more than the whole.
	if sum > cpu && sum > 0 {
		scale := cpu / sum
		for _, g := range list {
			g.CPU *= scale
			g.Finished *= scale
		}
		sum = cpu
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].CPU != list[j].CPU {
			return list[i].CPU > list[j].CPU
		}
		return list[i].Name < list[j].Name
	})

	rest := max(0, cpu-sum)
	if len(list) > spikeTop {
		for _, g := range list[spikeTop:] {
			rest += g.CPU
		}
		list = list[:spikeTop]
	}

	kept := make([]*group, 0, len(list)+1)
	kept = append(kept, list...)
	if rest > 0 {
		kept = append(kept, &group{SpikeProcess: SpikeProcess{Name: "Other", Other: true, CPU: rest}})
	}

	// Each line to the tenth, the leftover tenths going to the lines that
	// lost most to rounding, so the shown lines add up to the shown figure.
	values := make([]float64, len(kept))
	for i, g := range kept {
		values[i] = g.CPU
	}
	tenths := roundToTotal(values, spike.CPU)

	for i, g := range kept {
		if tenths[i] == 0 {
			continue
		}

		g.CPU = float64(tenths[i]) / 10
		g.Finished = round(min(g.CPU, g.Finished))
		// The command line only for the few kept: a file read each.
		if !g.Kernel && !g.Other {
			g.Command = command(g.busiestPID, g.Name)
		}
		if g.Site {
			g.Programs = programList(g.programs, g.programCPU)
		}
		spike.Top = append(spike.Top, g.SpikeProcess)
	}

	// Other is last whatever its size: it is what is left, not a program.
	sort.SliceStable(spike.Top, func(i, j int) bool {
		return !spike.Top[i].Other && spike.Top[j].Other
	})

	return spike
}

// roundToTotal rounds values to tenths that add up to total, itself in
// tenths: each is rounded down, and the tenths still missing go to those
// with the largest remainders.
func roundToTotal(values []float64, total float64) []int {
	out := make([]int, len(values))
	if len(values) == 0 {
		return out
	}

	want := int(total*10 + 0.5)
	have := 0
	order := make([]int, len(values))

	for i, value := range values {
		out[i] = int(value * 10)
		have += out[i]
		order[i] = i
	}

	sort.SliceStable(order, func(a, b int) bool {
		ra := values[order[a]]*10 - float64(out[order[a]])
		rb := values[order[b]]*10 - float64(out[order[b]])
		return ra > rb
	})

	for i := 0; have < want; i = (i + 1) % len(order) {
		out[order[i]]++
		have++
	}

	for i := len(order) - 1; have > want; i = (i - 1 + len(order)) % len(order) {
		if out[order[i]] > 0 {
			out[order[i]]--
			have--
		}
	}

	return out
}

// memoryOf is what was holding memory in a reading of the process table, on
// a machine with totalMB of it. Resident memory counts what processes share
// once for each of them — PHP workers share their opcode cache — so a line
// can read higher than its true cost; the order is still the right one.
func memoryOf(procs map[int]procTicks, totalMB float64, at time.Time, sites func(pid int) string) Usage {
	usage := Usage{At: at.Unix(), Top: []UsageItem{}}

	if totalMB <= 0 {
		return usage
	}

	type group struct {
		UsageItem
		mb       float64
		programs map[string]int
		size     map[string]float64
	}

	groups := map[string]*group{}

	for pid, process := range procs {
		if process.rssMB <= 0 {
			continue
		}

		key, name, site, program := process.name, process.name, false, process.name
		if sites != nil {
			if domain := sites(pid); domain != "" {
				key, name, site, program = "site:"+domain, domain, true, programLabel(process.name)
			}
		}

		g := groups[key]
		if g == nil {
			g = &group{UsageItem: UsageItem{Name: name, Site: site},
				programs: map[string]int{}, size: map[string]float64{}}
			groups[key] = g
		}

		g.mb += process.rssMB
		g.programs[program]++
		g.size[program] += process.rssMB
	}

	list := make([]*group, 0, len(groups))
	for _, g := range groups {
		list = append(list, g)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].mb != list[j].mb {
			return list[i].mb > list[j].mb
		}
		return list[i].Name < list[j].Name
	})

	if len(list) > spikeTop {
		list = list[:spikeTop]
	}

	for _, g := range list {
		g.Bytes = int64(g.mb * (1 << 20))
		g.Percent = round(min(100, g.mb/totalMB*100))

		if g.Site {
			g.Detail = programList(g.programs, g.size)
		} else if n := g.programs[g.Name]; n > 1 {
			g.Detail = strconv.Itoa(n) + " processes"
		}

		usage.Top = append(usage.Top, g.UsageItem)
	}

	return usage
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
