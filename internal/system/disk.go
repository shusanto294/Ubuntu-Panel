package system

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// DiskPlace is a directory the disk breakdown names on its own when it holds
// anything: the databases, the mail, the logs.
type DiskPlace struct {
	Name string
	Path string
}

// DiskPlaces are the directories other than the sites that are worth a line.
var DiskPlaces = []DiskPlace{
	{Name: "Databases (MySQL / MariaDB)", Path: "/var/lib/mysql"},
	{Name: "Databases (MongoDB)", Path: "/var/lib/mongodb"},
	{Name: "Databases (PostgreSQL)", Path: "/var/lib/postgresql"},
	{Name: "Mail", Path: "/var/vmail"},
	{Name: "Backups", Path: "/var/backups/ubuntu-panel"},
	{Name: "Logs", Path: "/var/log"},
	{Name: "Docker", Path: "/var/lib/docker"},
}

// DiskUsage is what is using the disk: each site's directory, the places
// above, and the rest of the machine as one line. It walks every file in them
// (du), idly, so it is something to do once an hour rather than once a minute.
func DiskUsage(sites []SiteRoot) *Usage {
	var metrics Metrics
	readDisk(&metrics)

	if metrics.DiskTotal <= 0 {
		return nil
	}

	names := map[string]string{}
	isSite := map[string]bool{}
	paths := []string{}

	for _, root := range newSiteIndex(sites) {
		if _, seen := names[root.Root]; !seen {
			names[root.Root], isSite[root.Root] = root.Domain, true
			paths = append(paths, root.Root)
		}
	}

	for _, place := range DiskPlaces {
		if _, seen := names[place.Path]; !seen {
			names[place.Path] = place.Name
			paths = append(paths, place.Path)
		}
	}

	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = proc.Quote(path)
	}

	// Missing directories are said so on stderr and left out; that is fine.
	result := proc.Run("sudo -n nice -n 19 ionice -c3 du -sb --one-file-system "+
		strings.Join(quoted, " ")+" 2>/dev/null", 15*time.Minute)

	sizes := parseDu(result.Output)

	usage := &Usage{At: time.Now().Unix(), Top: []UsageItem{}}
	counted := int64(0)

	for path, size := range sizes {
		name, ok := names[path]
		if !ok || size <= 0 {
			continue
		}

		counted += size
		usage.Top = append(usage.Top, UsageItem{Name: name, Site: isSite[path], Bytes: size})
	}

	sort.Slice(usage.Top, func(i, j int) bool { return usage.Top[i].Bytes > usage.Top[j].Bytes })

	if len(usage.Top) > spikeTop {
		usage.Top = usage.Top[:spikeTop]
	}

	// Everything not in a line of its own: the system, the apps, and the
	// smaller sites and places that did not make the list.
	shown := int64(0)
	for _, item := range usage.Top {
		shown += item.Bytes
	}

	if rest := int64(metrics.DiskUsed) - shown; rest > 0 {
		detail := "the system and installed software"
		if counted > shown {
			detail = "the system, installed software and smaller sites"
		}
		usage.Top = append(usage.Top, UsageItem{Name: "Everything else", Detail: detail, Bytes: rest})
	}

	for i := range usage.Top {
		usage.Top[i].Percent = round(min(100, float64(usage.Top[i].Bytes)/metrics.DiskTotal*100))
	}

	return usage
}

// parseDu reads `du -sb` output: a size in bytes, a tab, the path.
func parseDu(output string) map[string]int64 {
	sizes := map[string]int64{}

	for _, line := range strings.Split(output, "\n") {
		size, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}

		if bytes, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64); err == nil {
			sizes[strings.TrimRight(path, "/")] = bytes
		}
	}

	return sizes
}
