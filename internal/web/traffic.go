package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// The Logs page has three views. Activity is what the panel itself recorded.
// Traffic and Server logs are what the machine's own programs wrote: nginx's
// access log for every site read and added up — which site the requests went
// to, from where, for what, and which of them are an attack — and the raw
// files of nginx, PHP-FPM, MySQL, fail2ban and the system, for the line the
// summary cannot give.
//
// Traffic exists for the moment the CPU is pinned: it puts what is using the
// processor right now next to where the requests are going, so "PHP-FPM is at
// 380%" and "92% of PHP requests are POST /wp-login.php on shop.example from
// one address" are on the same screen.

// ----------------------------------------------------------------- traffic

// trafficWindow is one of the periods the page can be read over.
type trafficWindow struct {
	key      string
	span     time.Duration
	buckets  int
	lines    int  // how far back each site's log is read, at most
	previous bool // whether yesterday's rotated file is read too
}

var trafficWindows = []trafficWindow{
	{"15m", 15 * time.Minute, 15, 40000, false},
	{"1h", time.Hour, 30, 100000, false},
	{"6h", 6 * time.Hour, 36, 250000, true},
	{"24h", 24 * time.Hour, 48, 500000, true},
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
	trafficCache.Lock()
	if at, ok := trafficCache.at[window.key]; ok && time.Since(at) < 20*time.Second {
		report := trafficCache.reports[window.key]
		trafficCache.Unlock()
		return report
	}
	trafficCache.Unlock()

	started := time.Now()
	now := time.Now()

	analyzer := weblog.New(now.Add(-window.span), now, window.buckets)

	sources := map[string]string{unmatchedSite: "/var/log/nginx/access.log"}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			sources[site.Domain] = "/var/log/nginx/" + site.Domain + "-access.log"
		}
	}

	var (
		locked  sync.Mutex
		partial []string
		wait    sync.WaitGroup
		slots   = make(chan struct{}, 4)
	)

	for domain, path := range sources {
		wait.Add(1)

		go func(domain, path string) {
			defer wait.Done()

			slots <- struct{}{}
			defer func() { <-slots }()

			files := proc.Quote(path)
			if window.previous {
				files = proc.Quote(path+".1") + " " + files
			}

			read := 0

			sink := &lineWriter{line: func(line string) {
				read++

				locked.Lock()
				analyzer.Line(domain, line)
				locked.Unlock()
			}}

			command := fmt.Sprintf("sudo cat %s 2>/dev/null | tail -n %d", files, window.lines)

			if err := proc.Pipe(ctx, command, 2*time.Minute, nil, sink); err != nil {
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

	trafficCache.Lock()
	if trafficCache.at == nil {
		trafficCache.at = map[string]time.Time{}
		trafficCache.reports = map[string]trafficReport{}
	}
	trafficCache.at[window.key] = time.Now()
	trafficCache.reports[window.key] = report
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
		serverLog{Key: "fail2ban", Group: "Security", Label: "fail2ban", Path: "/var/log/fail2ban.log"},
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

	// One sudo for every file rather than one each.
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, proc.Quote(candidate.Path))
	}

	sizes := map[string]int64{}

	output := proc.Run("sudo stat -c '%s %n' "+strings.Join(paths, " ")+" 2>/dev/null", 30*time.Second).Output
	for _, line := range strings.Split(output, "\n") {
		size, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(size, 10, 64); err == nil {
			sizes[name] = n
		}
	}

	found := []serverLog{}
	for _, candidate := range candidates {
		if size, ok := sizes[candidate.Path]; ok {
			candidate.Size = size
			found = append(found, candidate)
		}
	}

	return found
}

// serverLogPerPage is how many lines a page of a log file is.
const serverLogPerPage = 20

// readServerLog is one page of a file, counted from its end — page 1 is the
// newest lines — or of the lines matching a search, a fixed string,
// case-insensitive. It returns the page and how many lines there are in all.
func readServerLog(path string, page int, search string) (string, int) {
	quoted := proc.Quote(path)
	upTo := page * serverLogPerPage

	count := fmt.Sprintf("sudo wc -l < %s 2>/dev/null", quoted)
	read := fmt.Sprintf("sudo tail -n %d %s 2>/dev/null | head -n %d", upTo, quoted, serverLogPerPage)

	if search != "" {
		match := "sudo grep -F -i -a -- " + proc.Quote(search) + " " + quoted + " 2>/dev/null"
		count = match + " | wc -l"
		read = fmt.Sprintf("%s | tail -n %d | head -n %d", match, upTo, serverLogPerPage)
	}

	total, _ := strconv.Atoi(strings.TrimSpace(proc.Run(count, time.Minute).Output))

	// The last page is short: tail asked for more than there are, and head
	// must only take what lies past the pages after it.
	if total > 0 && upTo > total {
		take := total - (page-1)*serverLogPerPage
		if take <= 0 {
			return "", total
		}
		read = strings.Replace(read, fmt.Sprintf("head -n %d", serverLogPerPage), fmt.Sprintf("head -n %d", take), 1)
	}

	return strings.TrimRight(proc.Run(read, time.Minute).Output, "\n"), total
}

// ------------------------------------------------------------------ props

// logsTrafficProps are the Traffic view's: the access logs read over the
// window, and what the processor is doing now. Lazy, so the Activity view —
// and a partial reload that does not ask for them — never pays for the read.
func (a *App) logsTrafficProps(ctx context.Context, windowKey string) map[string]any {
	window := findTrafficWindow(windowKey)

	windows := make([]string, 0, len(trafficWindows))
	for _, w := range trafficWindows {
		windows = append(windows, w.key)
	}

	// The processor is measured before the logs are read, never during: the
	// read is real work, and measured alongside it the panel's own cat and
	// tail would be what the page says is using the CPU.
	var (
		sampled   sync.Once
		processes system.Processes
	)
	sample := func() system.Processes {
		sampled.Do(func() { processes = system.SampleProcesses(time.Second) })
		return processes
	}

	return map[string]any{
		"window":  window.key,
		"windows": windows,
		"traffic": inertia.Lazy(func() any {
			sample()
			return a.traffic(context.WithoutCancel(ctx), window)
		}),
		"processes": inertia.Lazy(func() any { return sample() }),
		"fpm":       inertia.Lazy(func() any { return fpmPressure() }),
	}
}

// logsFilesProps are the Log files view's: the list of files, and a page of
// the one chosen.
func (a *App) logsFilesProps(ctx context.Context, source string, page int, search string) map[string]any {
	if page < 1 {
		page = 1
	}

	search = strings.TrimSpace(search)
	if len(search) > 200 {
		search = search[:200]
	}

	logs := a.serverLogs(ctx)

	var chosen *serverLog
	for i := range logs {
		if logs[i].Key == source {
			chosen = &logs[i]
		}
	}
	if chosen == nil && len(logs) > 0 {
		chosen = &logs[0]
	}

	props := map[string]any{
		"serverLogs": logs,
		"search":     search,
		"serverLog":  nil,
	}

	if chosen != nil {
		contents, total := readServerLog(chosen.Path, page, search)

		pages := max(1, (total+serverLogPerPage-1)/serverLogPerPage)
		if page > pages {
			page = pages
			contents, total = readServerLog(chosen.Path, page, search)
		}

		props["serverLog"] = map[string]any{
			"key":      chosen.Key,
			"label":    chosen.Label,
			"group":    chosen.Group,
			"path":     chosen.Path,
			"size":     chosen.Size,
			"contents": contents,
		}
		props["filePagination"] = map[string]any{
			"page": page, "pages": pages, "total": total, "per_page": serverLogPerPage,
		}
	}

	return props
}
