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
}

// MinuteUsage is a minute's biggest users. CPU is nil for the first minute
// after the panel starts: there is nothing to compare against yet.
type MinuteUsage struct {
	CPU    []UsageShare
	Memory []UsageShare
}

// usageTop is how many lines each list keeps.
const usageTop = 5

type usageProc struct {
	ticks uint64
	start string // start time, so a reused pid is not mistaken for the old process
	name  string
	unit  string
	rssMB float64
}

// UsageReader remembers the last minute's process table to compare against.
type UsageReader struct {
	prev map[int]usageProc
	at   time.Time
}

// Read takes the minute. services maps a systemd unit (without .service) to
// the site domain it runs, for the Node sites.
func (u *UsageReader) Read(services map[string]string) MinuteUsage {
	now := time.Now()
	current := readUsageProcs(u.prev)
	elapsed := now.Sub(u.at).Seconds()

	var memory Metrics
	readMemory(&memory)
	totalMB := memory.MemoryTotal / (1 << 20)
	machine := float64(cores()) * elapsed

	type group struct {
		share UsageShare
		cpu   float64
		mb    float64
	}
	groups := map[string]*group{}

	for pid, p := range current {
		name, site := usageLabel(p, services)

		g := groups[name]
		if g == nil {
			g = &group{share: UsageShare{Name: name, Site: site}}
			groups[name] = g
		}
		g.share.Count++
		g.mb += p.rssMB

		if then, ok := u.prev[pid]; ok && then.start == p.start && p.ticks >= then.ticks {
			g.cpu += float64(p.ticks - then.ticks)
		}
	}

	result := MinuteUsage{Memory: []UsageShare{}}
	if u.prev != nil && machine > 0 {
		result.CPU = []UsageShare{}
	}

	for _, g := range groups {
		if result.CPU != nil && g.cpu > 0 {
			share := g.share
			share.Percent = round(min(100, g.cpu/clockTicks/machine*100))
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

	u.prev, u.at = current, now

	return result
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
