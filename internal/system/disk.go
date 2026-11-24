package system

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// What is using the disk, measured when somebody hovers the disk graph and
// never in the background: it walks every file in the directories it names,
// which is the expensive part the panel does not do on a clock.

// DiskPlace is a directory with a line of its own when it holds anything.
type DiskPlace struct {
	Name string
	Path string
	Site bool
}

// DiskPlaces are the directories besides the sites worth naming.
var DiskPlaces = []DiskPlace{
	{Name: "Databases (MySQL / MariaDB)", Path: "/var/lib/mysql"},
	{Name: "Databases (MongoDB)", Path: "/var/lib/mongodb"},
	{Name: "Databases (PostgreSQL)", Path: "/var/lib/postgresql"},
	{Name: "Mail", Path: "/var/vmail"},
	{Name: "Backups", Path: "/var/backups/ubuntu-panel"},
	{Name: "Logs", Path: "/var/log"},
	{Name: "Docker", Path: "/var/lib/docker"},
}

// DiskShare is one line of it.
type DiskShare struct {
	Name    string  `json:"name"`
	Site    bool    `json:"site,omitempty"`
	Detail  string  `json:"detail,omitempty"`
	Bytes   int64   `json:"bytes"`
	Percent float64 `json:"pct"` // of the whole disk
}

// diskTop is how many named lines are kept before "Everything else".
const diskTop = 6

// DiskUsage measures the places (the sites' directories and DiskPlaces) and
// puts the rest of the used disk on one line. du runs at the lowest CPU and
// I/O priority, so the sites do not wait on it.
func DiskUsage(places []DiskPlace) []DiskShare {
	var metrics Metrics
	readDisk(&metrics)
	if metrics.DiskTotal <= 0 {
		return []DiskShare{}
	}

	byPath := map[string]DiskPlace{}
	quoted := []string{}
	for _, place := range places {
		path := strings.TrimRight(place.Path, "/")
		if path == "" {
			continue
		}
		if _, seen := byPath[path]; seen {
			continue
		}
		byPath[path] = place
		quoted = append(quoted, proc.Quote(path))
	}

	// Missing directories are said so on stderr and left out.
	result := proc.Run(`io=""; ionice -c2 -n7 true 2>/dev/null && io="ionice -c2 -n7"; `+
		`sudo -n nice -n 19 $io du -sb --one-file-system `+strings.Join(quoted, " ")+` 2>/dev/null`,
		3*time.Minute)

	shares := []DiskShare{}
	counted := int64(0)
	for path, size := range parseDu(result.Output) {
		place, ok := byPath[path]
		if !ok || size <= 0 {
			continue
		}
		counted += size
		shares = append(shares, DiskShare{Name: place.Name, Site: place.Site, Bytes: size})
	}

	sort.Slice(shares, func(i, j int) bool { return shares[i].Bytes > shares[j].Bytes })
	if len(shares) > diskTop {
		shares = shares[:diskTop]
	}

	shown := int64(0)
	for _, share := range shares {
		shown += share.Bytes
	}
	if rest := int64(metrics.DiskUsed) - shown; rest > 0 {
		detail := "the system and installed software"
		if counted > shown {
			detail = "the system, installed software and smaller sites"
		}
		shares = append(shares, DiskShare{Name: "Everything else", Detail: detail, Bytes: rest})
	}

	for i := range shares {
		shares[i].Percent = round(min(100, float64(shares[i].Bytes)/metrics.DiskTotal*100))
	}

	return shares
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
