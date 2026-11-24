package system

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What used the CPU and memory, once a minute, for the dashboard graphs'
// tooltips.
//
// The process table is read once a minute and nothing more: each process's
// CPU time now, less what it was a minute ago, is what it used over the whole
// minute — so a twenty-second burst between two readings is still counted,
// without reading anything in between. Memory is what each held at the end.
//
// Processes are put together by the systemd service they run in, which
// /proc/<pid>/cgroup names and anyone may read: every php-fpm worker is one
// PHP-FPM line, MySQL's threads one MySQL line, and a Node site — which runs
// as a service of its own — its domain. PHP sites share one PHP-FPM pool, so
// they stay one line; telling them apart needs root, which this does not use.
// Anything not in a service (a login shell, a cron job) goes by its program.

// UsageShare is one thing that used the CPU or memory over a minute.
type UsageShare struct {
	Name    string  `json:"name"`
	Site    bool    `json:"site,omitempty"`
	Count   int     `json:"n"`            // processes
	Percent float64 `json:"pct"`          // of the whole machine
	MB      float64 `json:"mb,omitempty"` // memory held, for the memory list
	// Spike marks a CPU list taken at the minute's busiest second rather than
	// averaged over the minute; its lines add up to that second's CPU.
	Spike bool `json:"spike,omitempty"`
}

// MinuteUsage is a minute's biggest users. CPU is nil for the first minute
// after the panel starts: there is nothing to compare against yet.
type MinuteUsage struct {
	CPU    []UsageShare
	Memory []UsageShare
}

// usageTop is how many lines each list keeps.
const usageTop = 5

// A second at or above spikeFloor percent CPU, and spikeStep points above the
// last one read this minute, gets the process table read so the graph can name
// what caused it — at most spikeReads times a minute. An idle server never
// pays for this.
const (
	spikeFloor = 10
	spikeStep  = 5
	spikeReads = 5
)

type usageProc struct {
	ticks uint64
	start string // start time, so a reused pid is not mistaken for the old process
	name  string
	unit  string
	rssMB float64
}

// UsageReader remembers the last minute's process table to compare against.
type UsageReader struct {
	prev map[int]usageProc // at the minute's start
	at   time.Time

	last   map[int]usageProc // at the latest read, minute or spike
	lastAt time.Time

	services map[string]string
	rate     map[string]float64 // ticks a second each line used last minute: its background

	spike      []UsageShare
	spikePct   float64
	spikeCount int
}

type usageGroup struct {
	share UsageShare
	ticks float64
	mb    float64
}

// tally puts processes on their lines: the CPU ticks each used since then,
// and the memory each holds now.
func tally(now, then map[int]usageProc, services map[string]string) map[string]*usageGroup {
	groups := map[string]*usageGroup{}
	for pid, p := range now {
		name, site := usageLabel(p, services)

		g := groups[name]
		if g == nil {
			g = &usageGroup{share: UsageShare{Name: name, Site: site}}
			groups[name] = g
		}
		g.share.Count++
		g.mb += p.rssMB

		if before, ok := then[pid]; ok && before.start == p.start && p.ticks >= before.ticks {
			g.ticks += float64(p.ticks - before.ticks)
		}
	}
	return groups
}

// Read takes the minute. services maps a systemd unit (without .service) to
// the site domain it runs, for the Node sites. When a spike was read during
// the minute, the CPU list is what caused it, since that is what the graph's
// line shows; otherwise it is each line's average over the minute.
func (u *UsageReader) Read(services map[string]string) MinuteUsage {
	now := time.Now()
	current := readUsageProcs(u.last)
	elapsed := now.Sub(u.at).Seconds()

	var memory Metrics
	readMemory(&memory)
	totalMB := memory.MemoryTotal / (1 << 20)
	machine := float64(cores()) * elapsed

	groups := tally(current, u.prev, services)

	result := MinuteUsage{Memory: []UsageShare{}}
	if u.prev != nil && machine > 0 {
		result.CPU = []UsageShare{}
	}

	rate := map[string]float64{}
	for name, g := range groups {
		if elapsed > 0 {
			rate[name] = g.ticks / elapsed
		}
		if result.CPU != nil && g.ticks > 0 {
			share := g.share
			share.Percent = round(min(100, g.ticks/clockTicks/machine*100))
			if share.Percent > 0 {
				result.CPU = append(result.CPU, share)
			}
		}
		if totalMB > 0 && g.mb >= 1 {
			share := g.share
			share.MB = round(g.mb)
			share.Percent = round(min(100, g.mb/totalMB*100))
			result.Memory = append(result.Memory, share)
		}
	}

	result.CPU = topShares(result.CPU)
	result.Memory = topShares(result.Memory)
	if u.spike != nil {
		result.CPU = u.spike
	}

	u.prev, u.at, u.last, u.lastAt = current, now, current, now
	u.services, u.rate = services, rate
	u.spike, u.spikePct, u.spikeCount = nil, 0, 0

	return result
}

// Spike is told each second's CPU, and reads the process table when the
// second is busy enough to be worth naming (see spikeFloor). What each line
// used since the previous read, less what it uses in the background, is its
// share of the spike.
func (u *UsageReader) Spike(cpu float64) {
	if u.prev == nil || cpu < spikeFloor || cpu < u.spikePct+spikeStep || u.spikeCount >= spikeReads {
		return
	}
	u.spikeCount++

	now := time.Now()
	current := readUsageProcs(u.last)
	elapsed := now.Sub(u.lastAt).Seconds()
	groups := tally(current, u.last, u.services)
	u.last, u.lastAt = current, now

	if list := spikeShares(groups, u.rate, elapsed, cpu); list != nil {
		u.spike, u.spikePct = list, cpu
	}
}

// spikeShares splits cpu percent between the lines by what each used above
// its background rate; nil when nothing stood out.
func spikeShares(groups map[string]*usageGroup, rate map[string]float64, elapsed, cpu float64) []UsageShare {
	total := 0.0
	for name, g := range groups {
		g.ticks = max(0, g.ticks-rate[name]*elapsed)
		total += g.ticks
	}
	if total == 0 {
		return nil
	}

	list := []UsageShare{}
	for _, g := range groups {
		if g.ticks > 0 {
			share := g.share
			share.Percent, share.Spike = cpu*g.ticks/total, true
			list = append(list, share)
		}
	}

	top := topShares(list)
	rest := cpu
	for i := range top {
		rest -= top[i].Percent
		top[i].Percent = round(top[i].Percent)
	}
	if rest = round(rest); rest > 0 {
		top = append(top, UsageShare{Name: "Everything else", Percent: rest, Spike: true})
	}
	return top
}

func topShares(list []UsageShare) []UsageShare {
	if list == nil {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Percent != list[j].Percent {
			return list[i].Percent > list[j].Percent
		}
		return list[i].MB > list[j].MB
	})
	if len(list) > usageTop {
		list = list[:usageTop]
	}
	return list
}

// readUsageProcs reads every process's CPU time, memory and service. The
// service is only read for a process not seen last minute.
func readUsageProcs(prev map[int]usageProc) map[int]usageProc {
	out := map[int]usageProc{}

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

		p, ok := parseUsageStat(string(stat), page)
		if !ok {
			continue
		}

		if then, seen := prev[pid]; seen && then.start == p.start {
			p.unit = then.unit
		} else {
			p.unit = serviceOf(pid)
		}

		out[pid] = p
	}

	return out
}

// parseUsageStat reads /proc/<pid>/stat: fields counted from the last ")",
// since a program's name may hold spaces and parentheses.
func parseUsageStat(stat string, pageMB float64) (usageProc, bool) {
	open, closing := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		return usageProc{}, false
	}

	fields := strings.Fields(stat[closing+1:])
	// utime is field 14 of the file and [11] here, stime 15 → [12], ppid
	// 4 → [1], starttime 22 → [19], rss 24 → [21].
	if len(fields) < 22 {
		return usageProc{}, false
	}

	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rss, _ := strconv.ParseFloat(fields[21], 64)

	name := stat[open+1 : closing]
	// Kernel threads are children of kthreadd (pid 2), or kthreadd itself.
	if fields[1] == "2" || fields[1] == "0" && name == "kthreadd" {
		name = "\x00kernel"
	}

	return usageProc{ticks: utime + stime, start: fields[19], name: name, rssMB: rss * pageMB}, true
}

// serviceOf is the systemd service a process runs in, without ".service",
// or "" when it is in none.
func serviceOf(pid int) string {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}

	return serviceFromCgroup(string(contents))
}

func serviceFromCgroup(contents string) string {
	for _, line := range strings.Split(contents, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || !strings.Contains(parts[2], "/system.slice/") {
			continue
		}

		// The innermost service: a site's unit, not the slice around it.
		segments := strings.Split(parts[2], "/")
		for i := len(segments) - 1; i >= 0; i-- {
			if unit, ok := strings.CutSuffix(segments[i], ".service"); ok && unit != "" {
				return unit
			}
		}
	}

	return ""
}

// usageLabel is the line a process is counted on, and whether it is a site.
func usageLabel(p usageProc, services map[string]string) (string, bool) {
	if p.name == "\x00kernel" {
		return "Linux kernel", false
	}

	if p.unit == "" {
		return p.name, false
	}

	if domain := services[p.unit]; domain != "" {
		return domain, true
	}

	return serviceLabel(p.unit), false
}

// serviceLabel is a service's name as someone looking after a server knows it.
func serviceLabel(unit string) string {
	if version, ok := strings.CutPrefix(unit, "php"); ok {
		if version, ok = strings.CutSuffix(version, "-fpm"); ok {
			return strings.TrimSpace("PHP " + version + " (PHP sites)")
		}
	}

	known := map[string]string{
		"mysql":               "MySQL",
		"mariadb":             "MariaDB",
		"mongod":              "MongoDB",
		"postgresql":          "PostgreSQL",
		"redis-server":        "Redis",
		"redis":               "Redis",
		"nginx":               "nginx",
		"apache2":             "Apache",
		"ubuntu-panel":        "This panel",
		"postfix":             "Postfix (mail)",
		"dovecot":             "Dovecot (mail)",
		"opendkim":            "OpenDKIM (mail)",
		"rspamd":              "Rspamd (mail)",
		"ssh":                 "SSH",
		"docker":              "Docker",
		"containerd":          "Docker (containerd)",
		"fail2ban":            "Fail2ban",
		"systemd-journald":    "System log (journald)",
		"snapd":               "Snap",
		"unattended-upgrades": "Automatic updates",
	}

	if label, ok := known[unit]; ok {
		return label
	}
	if strings.HasPrefix(unit, "postfix@") {
		return "Postfix (mail)"
	}

	return unit
}
