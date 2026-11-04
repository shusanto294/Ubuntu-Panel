package system

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Process is one program and what it used over the sample.
type Process struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	Name    string  `json:"name"`
	Command string  `json:"command"`
	CPU     float64 `json:"cpu"`       // percent of one core, so 4 busy cores is 400
	Memory  float64 `json:"memory_mb"` // resident
}

// ProcessGroup is every process with one name, added up: twelve php-fpm
// workers at 30% each are one PHP-FPM at 360%, and that is the line that
// explains a pinned machine.
type ProcessGroup struct {
	Name      string  `json:"name"`
	Processes int     `json:"processes"`
	CPU       float64 `json:"cpu"`
	Memory    float64 `json:"memory_mb"`
}

// Processes is what the machine was doing over the sample.
type Processes struct {
	Cores  int            `json:"cores"`
	CPU    float64        `json:"cpu"` // all of it, percent of the whole machine
	Load   [3]float64     `json:"load"`
	Top    []Process      `json:"top"`
	Groups []ProcessGroup `json:"groups"`
}

// clockTicks is USER_HZ, which every Linux the panel runs on fixes at 100.
const clockTicks = 100

type procSample struct {
	ticks  uint64
	name   string
	uid    string
	rssMB  float64
	ticked bool
}

// SampleProcesses reads every process's CPU time, waits, and reads it again.
// The difference is what each one is using now — unlike ps's %CPU, which is
// an average over the process's whole life and says a worker started a week
// ago is idle while it is pinning a core.
func SampleProcesses(wait time.Duration) Processes {
	before := readProcs()
	started := time.Now()

	time.Sleep(wait)

	after := readProcs()
	elapsed := time.Since(started).Seconds()

	users := userNames()

	result := Processes{Cores: cores(), Top: []Process{}, Groups: []ProcessGroup{}}

	var metrics Metrics
	readLoad(&metrics)
	result.Load = metrics.Load

	groups := map[string]*ProcessGroup{}
	all := []Process{}
	total := 0.0

	for pid, now := range after {
		used := 0.0
		if then, ok := before[pid]; ok && now.ticks >= then.ticks && elapsed > 0 {
			used = float64(now.ticks-then.ticks) / clockTicks / elapsed * 100
		}

		total += used

		user := users[now.uid]
		if user == "" {
			user = now.uid
		}

		process := Process{PID: pid, User: user, Name: now.name, CPU: round(used), Memory: round(now.rssMB)}
		all = append(all, process)

		group := groups[now.name]
		if group == nil {
			group = &ProcessGroup{Name: now.name}
			groups[now.name] = group
		}
		group.Processes++
		group.CPU += used
		group.Memory += now.rssMB
	}

	if result.Cores > 0 {
		result.CPU = round(min(100, total/float64(result.Cores)))
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].CPU != all[j].CPU {
			return all[i].CPU > all[j].CPU
		}
		return all[i].Memory > all[j].Memory
	})
	if len(all) > 60 {
		all = all[:60]
	}

	// The command line only for the ones shown: it is a file read per process.
	for i := range all {
		all[i].Command = commandLine(all[i].PID, all[i].Name)
	}
	result.Top = all

	for _, group := range groups {
		group.CPU, group.Memory = round(group.CPU), round(group.Memory)
		result.Groups = append(result.Groups, *group)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].CPU != result.Groups[j].CPU {
			return result.Groups[i].CPU > result.Groups[j].CPU
		}
		return result.Groups[i].Memory > result.Groups[j].Memory
	})
	if len(result.Groups) > 40 {
		result.Groups = result.Groups[:40]
	}

	return result
}

func readProcs() map[int]procSample {
	samples := map[int]procSample{}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return samples
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

		sample, ok := parseStat(string(stat), page)
		if !ok {
			continue
		}

		sample.uid = statusUID(pid)
		samples[pid] = sample
	}

	return samples
}

// parseStat reads /proc/<pid>/stat. The name is in parentheses and may hold
// spaces or parentheses of its own, so the fields are counted from the last
// closing one.
func parseStat(stat string, pageMB float64) (procSample, bool) {
	open, closing := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		return procSample{}, false
	}

	fields := strings.Fields(stat[closing+1:])
	// state is field 3 of the file and [0] here; utime 14 → [11], stime 15 →
	// [12], rss 24 → [21].
	if len(fields) < 22 {
		return procSample{}, false
	}

	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rss, _ := strconv.ParseFloat(fields[21], 64)

	return procSample{ticks: utime + stime, name: stat[open+1 : closing], rssMB: rss * pageMB}, true
}

func statusUID(pid int) string {
	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(scanner.Text(), "Uid:"); ok {
			if fields := strings.Fields(rest); len(fields) > 0 {
				return fields[0]
			}
		}
	}

	return ""
}

func commandLine(pid int, fallback string) string {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(contents) == 0 {
		return "[" + fallback + "]"
	}

	command := strings.TrimSpace(strings.ReplaceAll(string(contents), "\x00", " "))
	if len(command) > 240 {
		command = command[:240] + "…"
	}

	return command
}

func userNames() map[string]string {
	names := map[string]string{}

	contents, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return names
	}

	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 2 {
			names[fields[2]] = fields[0]
		}
	}

	return names
}
