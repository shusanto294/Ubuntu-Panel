package web

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// What is using the disk, for the disk graph's tooltip. Measured when somebody
// hovers the graph — never on a clock — and kept for diskUsageFresh, so
// hovering again does not walk the disk again. Requests that arrive while it
// is being measured wait for that one measurement.

const diskUsageFresh = 10 * time.Minute

var diskUsage struct {
	sync.Mutex
	shares []system.DiskShare
	at     time.Time
}

func (a *App) diskUsageEndpoint(w http.ResponseWriter, r *http.Request) {
	diskUsage.Lock()
	defer diskUsage.Unlock()

	if diskUsage.shares == nil || time.Since(diskUsage.at) > diskUsageFresh || r.URL.Query().Get("refresh") == "1" {
		diskUsage.shares = system.DiskUsage(a.diskPlaces(r))
		diskUsage.at = time.Now()
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"items":       diskUsage.shares,
		"measured_at": diskUsage.at.Unix(),
	})
}

// diskPlaces are each site's directory — the folder under the sites root that
// holds its document root, so a site's files outside public/ count too — and
// the other places worth a line.
func (a *App) diskPlaces(r *http.Request) []system.DiskPlace {
	places := []system.DiskPlace{}
	root := strings.TrimRight(a.Config.SitesRoot, "/")

	if sites, err := a.Store.Sites(r.Context()); err == nil {
		for _, site := range sites {
			path := filepath.Clean(site.RootPath)
			if rest, ok := strings.CutPrefix(path, root+"/"); ok && root != "" {
				path = root + "/" + strings.SplitN(rest, "/", 2)[0]
			}
			if path == "/" || path == "." {
				continue
			}
			places = append(places, system.DiskPlace{Name: site.Domain, Path: path, Site: true})
		}
	}

	return append(places, system.DiskPlaces...)
}
