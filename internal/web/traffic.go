package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// The Logs page has three views. Activity is what the panel itself recorded.
// Traffic and Server logs are what the machine's own programs wrote: nginx's
// access log for every site read and added up — which site the requests went
// to, from where, for what, and which of them are an attack — and the raw
// files of nginx, PHP-FPM, MySQL and the system, for the line the
// summary cannot give.
//
// Traffic exists for the moment the CPU is pinned: it puts what is using the
// processor right now next to where the requests are going, so "PHP-FPM is at
// 380%" and "92% of PHP requests are POST /wp-login.php on shop.example from
// one address" are on the same screen.

// ----------------------------------------------------------------- traffic

// trafficWindow is one of the periods the page can be read over.
type trafficWindow struct {
	key     string
	span    time.Duration
	buckets int
	lines   int           // how far back each site's log is read, at most
	rotated int           // how many rotated files are read too: .1, then .2.gz and on
	keep    time.Duration // how long a read is reused
}

// The same periods as the log lists. logrotate turns nginx's logs over daily
// and keeps two weeks by default, compressed from the second day; a month
// reads whatever of it is still there.
var trafficWindows = []trafficWindow{
	{"1h", time.Hour, 30, 150000, 1, 20 * time.Second},
	{"24h", 24 * time.Hour, 48, 600000, 1, 2 * time.Minute},
	{"7d", 7 * 24 * time.Hour, 56, 1500000, 8, 10 * time.Minute},
	{"30d", 30 * 24 * time.Hour, 60, 3000000, 31, 10 * time.Minute},
}

func findTrafficWindow(key string) trafficWindow {
	for _, window := range trafficWindows {
		if window.key == key {
			return window
		}
	}

	return trafficWindows[0]
}

// unmatchedSite is what requests nginx answered with its default server are
// filed under: ones for the bare IP or a hostname no site has, which is where
// most scanners knock first.
const unmatchedSite = "(no site — server IP)"

// trafficSource is one access log the Analytics page reads, under the name
// it is filed as there.
type trafficSource struct {
	Name string
	Path string
	Site bool // a site's own, rather than a mail hostname's
}

// trafficSources are every hostname the server answers that has a log of its
// own: each site, and each mail domain's webmail (mail.<domain>) and
// autoconfiguration (autoconfig.<domain>) hostnames. Those last two answer
// directly rather than through Cloudflare, so scanners find them; their
// requests are shown under their own names rather than as the bare server IP,
// which is only nginx's shared log — the default server's.
func (a *App) trafficSources(ctx context.Context) []trafficSource {
	sources := []trafficSource{}
	seen := map[string]bool{}

	add := func(name string, site bool) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		sources = append(sources, trafficSource{Name: name, Path: "/var/log/nginx/" + name + "-access.log", Site: site})
	}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			add(site.Domain, true)
		}
	}

	if domains, err := a.Store.EmailDomains(ctx); err == nil {
		for _, domain := range domains {
			add(domain.WebmailHost(), false)
			add(domain.AutoconfigHost(), false)
		}
	}

	return sources
}

// trafficNames are the names the Analytics page can be narrowed to, sites
// first.
func (a *App) trafficNames(ctx context.Context) []string {
	names := []string{}
	for _, source := range a.trafficSources(ctx) {
		names = append(names, source.Name)
	}
	return names
}

// trafficReport is the access logs added up, with the read's own limits.
type trafficReport struct {
	weblog.Report

	Window string `json:"window"`

	// Partial are the sites whose log had more in the window than was read:
	// the counts for them are the newest part of it.
	Partial []string `json:"partial"`

	TookMS int64 `json:"took_ms"`
}

var trafficCache struct {
	sync.Mutex
	at      map[string]time.Time
	reports map[string]trafficReport
}

// traffic reads every site's access log over a window. Kept for twenty
// seconds: the page refreshes while it is open, and a 24-hour read of a busy
// machine is a few seconds of work nobody needs done twice in a row.
func (a *App) traffic(ctx context.Context, window trafficWindow) trafficReport {
	return a.siteTraffic(ctx, window, "")
}

// siteTraffic is the same read narrowed to one site's log: "" is every site
// and the server's own, and a domain the panel has no site for reads nothing.
func (a *App) siteTraffic(ctx context.Context, window trafficWindow, only string) trafficReport {
	now := time.Now()
	return a.readTraffic(ctx, window, only, now.Add(-window.span), now, window.buckets, window.key+"|"+only)
}

// readTraffic reads the access logs between from and to, with the files and
// the cap a window reads: the whole window, or one bar of its chart. A read
// with no cache key — a bar, asked for once — is not kept. tune sets the
// analyzer's options before anything is read.
func (a *App) readTraffic(ctx context.Context, window trafficWindow, only string, from, to time.Time, buckets int, cacheKey string, tune ...func(*weblog.Analyzer)) trafficReport {
	trafficCache.Lock()
	if at, ok := trafficCache.at[cacheKey]; ok && cacheKey != "" && time.Since(at) < window.keep {
		report := trafficCache.reports[cacheKey]
		trafficCache.Unlock()
		return report
	}
	trafficCache.Unlock()

	started := time.Now()

	analyzer := weblog.New(from, to, buckets)
	analyzer.Trusted = a.trustedSet(ctx)
	for _, set := range tune {
		set(analyzer)
	}

	sources := map[string]string{}
	domains := []string{}

	if only == "" || only == unmatchedSite {
		sources[unmatchedSite] = "/var/log/nginx/access.log"
	}

	for _, source := range a.trafficSources(ctx) {
		if only != "" && source.Name != only {
			continue
		}
		sources[source.Name] = source.Path
		if source.Site {
			domains = append(domains, source.Name)
		}
	}

	analyzer.Proxied = proxiedSites(domains)

	var (
		locked  sync.Mutex
		partial []string
		wait    sync.WaitGroup
		slots   = make(chan struct{}, 4)
	)

	if isGentle(ctx) {
		slots = make(chan struct{}, 1)
	}

	for domain, path := range sources {
		wait.Add(1)

		go func(domain, path string) {
			defer wait.Done()

			slots <- struct{}{}
			defer func() { <-slots }()

			// Oldest first, so tail keeps the newest when the cap is reached.
			files := []string{}
			for n := window.rotated; n >= 2; n-- {
				files = append(files, path+"."+strconv.Itoa(n)+".gz")
			}
			if window.rotated >= 1 {
				files = append(files, path+".1")
			}
			files = append(files, path)

			read := 0

			sink := &lineWriter{line: func(line string) {
				read++

				locked.Lock()
				analyzer.Line(domain, line)
				locked.Unlock()
			}}

			quoted := make([]string, len(files))
			for i, file := range files {
				quoted[i] = proc.Quote(file)
			}

			// A file last written before the window began has nothing in it,
			// and is not read: the hour's scan every five minutes used to
			// stream all of yesterday's rotated log only to throw it away. A
			// plain file is read from its end, as far as the cap, rather than
			// through from the start.
			script := fmt.Sprintf(`for f in %s; do [ -e "$f" ] && [ "$(stat -c %%Y -- "$f")" -ge %d ] || continue; `+
				`case "$f" in *.gz) zcat -f -- "$f";; *) tail -n %d -- "$f";; esac; done`,
				strings.Join(quoted, " "), from.Unix(), window.lines)
			command := fmt.Sprintf("sudo sh -c %s 2>/dev/null | tail -n %d", proc.Quote(script), window.lines)

			if err := proc.Pipe(ctx, command, 5*time.Minute, nil, sink); err != nil {
				a.Log.Debug("could not read an access log", "path", path, "error", err)
			}
			sink.Flush()

			if read >= window.lines {
				locked.Lock()
				partial = append(partial, domain)
				locked.Unlock()
			}
		}(domain, path)
	}

	wait.Wait()

	slices.Sort(partial)

	report := trafficReport{
		Report:  analyzer.Report(),
		Window:  window.key,
		Partial: append([]string{}, partial...),
		TookMS:  time.Since(started).Milliseconds(),
	}

	if cacheKey == "" {
		return report
	}

	trafficCache.Lock()
	if trafficCache.at == nil {
		trafficCache.at = map[string]time.Time{}
		trafficCache.reports = map[string]trafficReport{}
	}
	trafficCache.at[cacheKey] = time.Now()
	trafficCache.reports[cacheKey] = report
	trafficCache.Unlock()

	return report
}

// lineWriter hands what is written to it on one line at a time.
type lineWriter struct {
	pending []byte
	line    func(string)
}

func (w *lineWriter) Write(chunk []byte) (int, error) {
	w.pending = append(w.pending, chunk...)

	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}

		w.line(string(w.pending[:end]))
		w.pending = w.pending[end+1:]
	}

	// A line longer than any request nginx accepts is junk; dropping it keeps
	// one binary blob from holding the whole read in memory.
	if len(w.pending) > 64<<10 {
		w.pending = w.pending[:0]
	}

	return len(chunk), nil
}

// Flush hands over a last line that had no newline after it.
func (w *lineWriter) Flush() {
	if len(w.pending) > 0 {
		w.line(string(w.pending))
		w.pending = w.pending[:0]
	}
}

var _ io.Writer = (*lineWriter)(nil)

// fpmPressure is each PHP-FPM telling, in its own log, that it ran out of
// workers. Every request past that point waits in a queue, so a site under a
// flood is slow for every visitor, not only the attacker.
func fpmPressure() []map[string]any {
	found := []map[string]any{}

	for _, version := range system.InstalledPHPVersions() {
		path := "/var/log/php" + version + "-fpm.log"

		output := proc.Run(fmt.Sprintf(
			"sudo tail -n 5000 %s 2>/dev/null | grep -E 'max_children|seems busy' | tail -n 200",
			proc.Quote(path)), 30*time.Second).Output

		output = strings.TrimSpace(output)
		if output == "" {
			continue
		}

		lines := strings.Split(output, "\n")

		found = append(found, map[string]any{
			"version": version,
			"path":    path,
			"count":   len(lines),
			"last":    lines[len(lines)-1],
		})
	}

	return found
}

// --------------------------------------------------------------- raw files

// serverLog is one file the Server logs view can show. Only these: the page
// names a key, never a path, so it cannot be pointed at /etc/shadow.
type serverLog struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Label string `json:"label"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
}

// serverLogs are the logs this machine has, in the order the page lists them.
// A file that does not exist is left out — MySQL's log on a machine without
// MySQL is not a choice anybody needs.
func (a *App) serverLogs(ctx context.Context) []serverLog {
	candidates := []serverLog{
		{Key: "nginx-error", Group: "Web server", Label: "nginx error log", Path: "/var/log/nginx/error.log"},
		{Key: "nginx-access", Group: "Web server", Label: "nginx access log (no site matched)", Path: "/var/log/nginx/access.log"},
	}

	for _, version := range system.InstalledPHPVersions() {
		candidates = append(candidates, serverLog{
			Key: "php-fpm-" + version, Group: "PHP", Label: "PHP-FPM " + version,
			Path: "/var/log/php" + version + "-fpm.log",
		})
	}

	candidates = append(candidates,
		serverLog{Key: "mysql", Group: "Databases", Label: "MySQL / MariaDB error log", Path: "/var/log/mysql/error.log"},
		serverLog{Key: "redis", Group: "Databases", Label: "Redis", Path: "/var/log/redis/redis-server.log"},
		serverLog{Key: "mongodb", Group: "Databases", Label: "MongoDB", Path: "/var/log/mongodb/mongod.log"},
		serverLog{Key: "auth", Group: "Security", Label: "Authentication (SSH, sudo)", Path: "/var/log/auth.log"},
		serverLog{Key: "ufw", Group: "Security", Label: "Firewall (ufw)", Path: "/var/log/ufw.log"},
		serverLog{Key: "syslog", Group: "System", Label: "System log", Path: "/var/log/syslog"},
		serverLog{Key: "kern", Group: "System", Label: "Kernel (out-of-memory kills)", Path: "/var/log/kern.log"},
		serverLog{Key: "mail", Group: "System", Label: "Mail", Path: "/var/log/mail.log"},
	)

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			group := "Site · " + site.Domain

			for _, file := range siteLogPaths(site) {
				candidates = append(candidates, serverLog{
					Key: "site:" + site.Domain + ":" + file.key, Group: group,
					Label: file.label, Path: file.path,
				})
			}

			// Where the application writes the errors it catches itself, which
			// nginx never sees.
			switch site.Type {
			case "wordpress":
				candidates = append(candidates, serverLog{
					Key: "site:" + site.Domain + ":wp-debug", Group: group,
					Label: "WordPress debug.log", Path: strings.TrimRight(site.DocumentRoot(), "/") + "/wp-content/debug.log",
				})
			case "laravel":
				candidates = append(candidates, serverLog{
					Key: "site:" + site.Domain + ":laravel", Group: group,
					Label: "laravel.log", Path: strings.TrimRight(site.RootPath, "/") + "/storage/logs/laravel.log",
				})
			}
		}
	}

	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.Path)
	}

	stats := statLogs(paths)

	found := []serverLog{}
	for _, candidate := range candidates {
		if stat, ok := stats[candidate.Path]; ok {
			candidate.Size = stat.size
			found = append(found, candidate)
		}
	}

	return found
}

// serverLogPerPage is how many lines a page of a log file is.
const serverLogPerPage = 20

// ------------------------------------------------------------------ props

// ------------------------------------------------------------ stats pages

// trafficStats are the Analytics page's boxes and lists: the period's
// totals, each a link to its own page, and the sites and pages with a link to
// each.
func (a *App) trafficStats(ctx context.Context, report trafficReport) map[string]any {

	suspicious := 0
	for _, threat := range report.Threats {
		suspicious += threat.Requests
	}

	attackers := 0
	for _, ip := range report.TopIPs {
		if ip.Danger {
			attackers++
		}
	}

	base := a.siteBases(ctx)

	sites := []map[string]any{}
	for _, site := range report.Sites {
		if site.Requests == 0 {
			continue
		}
		sites = append(sites, map[string]any{
			"domain":        site.Domain,
			"url":           base[site.Domain],
			"requests":      site.Requests,
			"ips":           site.IPs,
			"client_errors": site.NotFound,
			"server_errors": site.Errors,
			"last":          site.Last,
		})
	}

	recent := requestRows(base, report.Recent)

	// The server's IP and hostnames no site has are scanners, not pages.
	pages := []map[string]any{}
	for _, page := range report.Pages {
		if page.Site == unmatchedSite {
			continue
		}
		url := ""
		if base[page.Site] != "" {
			url = base[page.Site] + page.Path
		}
		pages = append(pages, map[string]any{
			"site":     page.Site,
			"path":     page.Path,
			"url":      url,
			"requests": page.Requests,
			"ips":      page.IPs,
		})
	}

	return map[string]any{
		"window":        report.Window,
		"sites":         sites,
		"pages":         pages,
		"urls":          urlRows(base, report.Paths[:min(len(report.Paths), sliceRows)]),
		"urls_total":    len(report.Paths),
		"recent":        recent,
		"bots":          report.Bots,
		"requests":      report.Requests,
		"bytes":         report.Bytes,
		"dynamic":       report.Dynamic,
		"ips":           report.IPs,
		"client_errors": report.Statuses["4xx"],
		"server_errors": report.Statuses["5xx"],
		"suspicious":    suspicious,
		"attackers":     attackers,
		"partial":       report.Partial,
		"took_ms":       report.TookMS,
	}
}

// siteBases are each site's address, so a page in a list is a link to it.
func (a *App) siteBases(ctx context.Context) map[string]string {
	base := map[string]string{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			scheme := "http://"
			if site.SSL {
				scheme = "https://"
			}
			base[site.Domain] = scheme + site.Domain
		}
	}
	return base
}

// requestRows are requests as the pages list them, each a link to what was
// asked for when it was a site's page. A request to the bare IP has nowhere
// to link to.
func requestRows(base map[string]string, requests []weblog.Request) []map[string]any {
	rows := make([]map[string]any, 0, len(requests))
	for _, request := range requests {
		url := ""
		if base[request.Site] != "" && request.Method == "GET" && request.Status < 400 {
			url = base[request.Site] + request.Path
		}
		rows = append(rows, map[string]any{
			"at":     request.At,
			"site":   request.Site,
			"method": request.Method,
			"path":   request.Path,
			"status": request.Status,
			"ip":     request.IP,
			"threat": request.Threat,
			"bot":    request.Bot,
			"url":    url,
		})
	}
	return rows
}

// statsSections are the pages the Logs page's boxes open.
var statsSections = map[string]string{
	"latest":        "Latest requests",
	"requests":      "Requests",
	"php":           "Requests to PHP and the apps",
	"addresses":     "Addresses",
	"not-found":     "Not found and refused",
	"server-errors": "Server errors",
	"suspicious":    "Suspicious activity",
}

// logsStatsPage is one of them. Like the boxes, the traffic is never read on
// the first paint: the page asks for it once it is up, and shows that it is
// reading meanwhile.
func (a *App) logsStatsPage(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")

	// The latest requests are the period's page now, which lists every one
	// of them rather than the newest two hundred.
	if section == "latest" {
		query := url.Values{"view": {"requests"}, "window": {r.URL.Query().Get("window")}}
		if site := r.URL.Query().Get("site"); site != "" {
			query.Set("site", site)
		}
		http.Redirect(w, r, "/analytics/period?"+query.Encode(), http.StatusFound)
		return
	}

	title, ok := statsSections[section]
	if !ok {
		a.notFoundError(w, r, "No such page.")
		return
	}

	window := findTrafficWindow(r.URL.Query().Get("window"))
	site := a.analyticsSite(r.Context(), r.URL.Query().Get("site"))

	windows := make([]string, 0, len(trafficWindows))
	for _, w := range trafficWindows {
		windows = append(windows, w.key)
	}

	props := inertia.Props{
		"section":   section,
		"title":     title,
		"window":    window.key,
		"windows":   windows,
		"site":      site,
		"unmatched": unmatchedSite,
	}

	viewer := session.ClientIP(r)
	ctx := context.WithoutCancel(r.Context())

	if inertia.Asked(r, "Logs/Stats", "traffic") {
		report, reasons := a.markTrusted(ctx, a.siteTraffic(ctx, window, site), viewer)
		props["traffic"] = report
		props["trustedIps"] = reasons

		// The feed with each request's address, which only the stats carry.
		if section == "latest" {
			props["stats"] = a.trafficStats(ctx, report)
		}
	}

	// What the CPU is doing is the PHP page's alone: it takes a second to
	// measure.
	if section == "php" && inertia.Asked(r, "Logs/Stats", "processes") {
		props["processes"] = system.SampleProcesses(time.Second)
		props["fpm"] = fpmPressure()
	}

	a.Render.Render(w, r, "Logs/Stats", props)
}

var proxiedCache struct {
	sync.Mutex
	at    map[string]time.Time
	known map[string]bool
}

// proxiedSites are the sites whose domain resolves to Cloudflare alone: their
// visitors arrive through it. Asked of DNS at most every ten minutes a site;
// the bare IP is never one.
func proxiedSites(domains []string) map[string]bool {
	proxied := map[string]bool{}

	proxiedCache.Lock()
	if proxiedCache.known == nil {
		proxiedCache.at, proxiedCache.known = map[string]time.Time{}, map[string]bool{}
	}
	stale := []string{}
	for _, domain := range domains {
		if at, ok := proxiedCache.at[domain]; ok && time.Since(at) < 10*time.Minute {
			proxied[domain] = proxiedCache.known[domain]
		} else {
			stale = append(stale, domain)
		}
	}
	proxiedCache.Unlock()

	for _, domain := range stale {
		lookup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		addresses, err := net.DefaultResolver.LookupHost(lookup, domain)
		cancel()
		behind := err == nil && len(addresses) > 0
		for _, address := range addresses {
			if !security.FromCloudflare(address) {
				behind = false
			}
		}

		proxiedCache.Lock()
		proxiedCache.at[domain], proxiedCache.known[domain] = time.Now(), behind
		proxiedCache.Unlock()
		proxied[domain] = behind
	}

	return proxied
}

type gentleKey struct{}

// gently marks a context as background work nobody is waiting on, which
// reads one thing at a time rather than spreading over every core.
func gently(ctx context.Context) context.Context {
	return context.WithValue(ctx, gentleKey{}, true)
}

func isGentle(ctx context.Context) bool {
	gentle, _ := ctx.Value(gentleKey{}).(bool)
	return gentle
}
