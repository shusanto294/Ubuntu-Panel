package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// An address's report: everything the server has on one address, for the
// page a notification about it opens and for /logs/ip/{ip}. What it asked
// the sites for, from the access logs; what the server logged about it; its
// panel sign-ins and SSH attempts; whether
// it is trusted, or a useful bot — and if it says it is a search engine,
// whether it really is.

// ipLine is one request it made.
type ipLine struct {
	At     int64  `json:"at"`
	Site   string `json:"site"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Agent  string `json:"agent"`
	Threat string `json:"threat,omitempty"`
}

type ipReport struct {
	IP       string          `json:"ip"`
	Trusted  string          `json:"trusted,omitempty"`
	GoodBot  *weblog.GoodBot `json:"good_bot,omitempty"`
	BotCheck *botCheck       `json:"bot_check,omitempty"`

	Requests int            `json:"requests"`
	First    int64          `json:"first"`
	Last     int64          `json:"last"`
	Statuses map[string]int `json:"statuses"`
	Sites    []weblog.Count `json:"sites"`
	Paths    []weblog.Count `json:"paths"`
	Agents   []weblog.Count `json:"agents"`
	Threats  map[string]int `json:"threats"`
	Lines    []ipLine       `json:"lines"`

	Events []store.EventEntry `json:"events"`
}

// ipLinesPerFile is how many of an address's most recent lines are read from
// each access log and its rotated copy.
const ipLinesPerFile = 500

func (a *App) ipReport(ctx context.Context, ip, viewer string) ipReport {
	report := ipReport{
		IP: ip, Statuses: map[string]int{}, Threats: map[string]int{},
		Sites: []weblog.Count{}, Paths: []weblog.Count{}, Agents: []weblog.Count{},
		Lines: []ipLine{}, Events: []store.EventEntry{},
	}

	if net.ParseIP(ip) == nil {
		return report
	}

	report.Trusted = trustReason(a.trusted(ctx), ip, viewer)

	// What it asked the sites for.
	pattern := "^" + strings.ReplaceAll(ip, ".", `\.`) + " "
	script := `for f in /var/log/nginx/*access.log /var/log/nginx/*access.log.1; do ` +
		`[ -e "$f" ] && grep -sa -- ` + proc.Quote(pattern) + ` "$f" | tail -n ` + fmt.Sprint(ipLinesPerFile) +
		` | sed "s|^|$f\t|"; done`

	output := proc.Run("sudo sh -c "+proc.Quote(script), time.Minute).Output

	sites, paths, agents := map[string]int{}, map[string]int{}, map[string]int{}
	lines := []ipLine{}

	for _, raw := range strings.Split(output, "\n") {
		file, text, ok := strings.Cut(raw, "\t")
		if !ok {
			continue
		}

		entry, ok := weblog.Parse(text)
		if !ok {
			continue
		}

		site := siteOfLog(file)
		line := ipLine{
			At: entry.Time.Unix(), Site: site, Method: entry.Method, Path: entry.Path,
			Status: entry.Status, Agent: entry.UserAgent, Threat: weblog.Threat(entry),
		}
		if report.Trusted != "" {
			line.Threat = ""
		}

		lines = append(lines, line)
		sites[site]++
		paths[entry.Path]++
		agents[entry.UserAgent]++
		report.Statuses[fmt.Sprintf("%dxx", entry.Status/100)]++
		if line.Threat != "" {
			report.Threats[line.Threat]++
		}
	}

	sort.Slice(lines, func(i, j int) bool { return lines[i].At > lines[j].At })

	report.Requests = len(lines)
	if len(lines) > 0 {
		report.Last, report.First = lines[0].At, lines[len(lines)-1].At
	}
	if len(lines) > 100 {
		lines = lines[:100]
	}
	report.Lines = lines
	report.Sites, report.Paths, report.Agents = topCounts(sites, 10), topCounts(paths, 20), topCounts(agents, 5)

	// Which useful bot it says it is, and whether that is true.
	for _, agent := range report.Agents {
		if bot := weblog.FindGoodBot(agent.Name); bot != nil {
			report.GoodBot = bot
			if len(bot.Domains) > 0 {
				check := verifyBot(ctx, ip, bot)
				report.BotCheck = &check
			}
			break
		}
	}

	// What else the server and the panel recorded about it.
	if events, _, err := a.Store.Events(ctx, store.EventFilter{
		Since: time.Now().AddDate(0, 0, -30).Unix(), Search: ip,
	}, 30, 0); err == nil {
		report.Events = events
	}

	return report
}

// siteOfLog is the site an access log belongs to: the panel names them
// <domain>-access.log, and nginx's own is the requests no site matched.
func siteOfLog(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".1")
	if domain, ok := strings.CutSuffix(base, "-access.log"); ok {
		return domain
	}

	return unmatchedSite
}

func topCounts(counts map[string]int, n int) []weblog.Count {
	list := make([]weblog.Count, 0, len(counts))
	for name, count := range counts {
		list = append(list, weblog.Count{Name: name, Count: count})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Name < list[j].Name
	})

	if len(list) > n {
		list = list[:n]
	}

	return list
}

// --------------------------------------------------------- bot verification

// botCheck is whether an address that says it is a search engine's crawler
// is one: its reverse DNS is the engine's, and that name resolves back to it.
type botCheck struct {
	Verified bool   `json:"verified"`
	Host     string `json:"host"`
}

var botChecks struct {
	sync.Mutex
	results map[string]botCheck
	at      map[string]time.Time
}

func verifyBot(ctx context.Context, ip string, bot *weblog.GoodBot) botCheck {
	key := ip + " " + bot.Name

	botChecks.Lock()
	if at, ok := botChecks.at[key]; ok && time.Since(at) < 24*time.Hour {
		result := botChecks.results[key]
		botChecks.Unlock()
		return result
	}
	botChecks.Unlock()

	result := checkBotDNS(ctx, ip, bot.Domains)

	botChecks.Lock()
	if botChecks.results == nil {
		botChecks.results, botChecks.at = map[string]botCheck{}, map[string]time.Time{}
	}
	botChecks.results[key], botChecks.at[key] = result, time.Now()
	botChecks.Unlock()

	return result
}

func checkBotDNS(ctx context.Context, ip string, domains []string) botCheck {
	host, verified := weblog.CheckDNS(ctx, ip, domains)
	return botCheck{Verified: verified, Host: host}
}

// ------------------------------------------------------------------ pages

// logsIP is one address's page. The report greps the access logs, so it is
// asked for once the page is up.
func (a *App) logsIP(w http.ResponseWriter, r *http.Request) {
	ip := r.PathValue("ip")
	if net.ParseIP(ip) == nil {
		a.notFoundError(w, r, "That is not an IP address.")
		return
	}

	props := inertia.Props{"ip": ip}

	if inertia.Asked(r, "Logs/Ip", "report") {
		props["report"] = a.ipReport(context.WithoutCancel(r.Context()), ip, session.ClientIP(r))
	}

	a.Render.Render(w, r, "Logs/Ip", props)
}

var ipInText = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// notificationIPs are the addresses a notification is about: the ones it
// carries, or — for one written before notifications carried any — the ones
// its text names.
func notificationIPs(n *store.Notification) []string {
	ips := []string{}
	seen := map[string]bool{}

	add := func(ip string) {
		if net.ParseIP(ip) != nil && !seen[ip] && len(ips) < 10 {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}

	if list, ok := n.Data["ips"].([]any); ok {
		for _, item := range list {
			if ip, ok := item.(string); ok {
				add(ip)
			}
		}
	}

	aboutAddresses := map[string]bool{"attacks": true, "web_ban": true, "ssh_ban": true,
		"panel_login": true, "panel_lockout": true, "ssh_login": true}

	if len(ips) == 0 && aboutAddresses[n.Kind] {
		for _, ip := range ipInText.FindAllString(n.Title+" "+n.Body, -1) {
			add(ip)
		}
	}

	return ips
}

// notificationShow is one notification, with what it is about: the reports
// on its addresses, the log lines it counted, the task that failed. Opening
// it marks it seen.
func (a *App) notificationShow(w http.ResponseWriter, r *http.Request) {
	n, err := a.Store.FindNotification(r.Context(), routeID(r, "notification"))
	if err != nil {
		a.notFoundError(w, r, "No such notification.")
		return
	}

	if n.ReadAt == nil {
		a.Store.ReadNotifications(r.Context(), []int64{n.ID})
		now := time.Now().Unix()
		n.ReadAt = &now
	}

	props := inertia.Props{"notification": n, "ips": notificationIPs(n)}

	if inertia.Asked(r, "Notifications/Show", "related") {
		props["related"] = a.notificationRelated(context.WithoutCancel(r.Context()), n, session.ClientIP(r))
	}

	a.Render.Render(w, r, "Notifications/Show", props)
}

func (a *App) notificationRelated(ctx context.Context, n *store.Notification, viewer string) map[string]any {
	related := map[string]any{"reports": []ipReport{}, "events": []store.ServerEvent{}}

	reports := []ipReport{}
	for _, ip := range notificationIPs(n) {
		reports = append(reports, a.ipReport(ctx, ip, viewer))
	}
	related["reports"] = reports

	// The critical lines it counted.
	if after, ok := n.Data["events_after"].(float64); ok {
		until, _ := n.Data["events_until"].(float64)
		events, _ := a.Store.ServerEventsAfter(ctx, int64(after), []string{"critical"}, 100)

		kept := []store.ServerEvent{}
		for _, event := range events {
			if until == 0 || event.ID <= int64(until) {
				kept = append(kept, event)
			}
		}
		related["events"] = kept
	}

	// The task that failed.
	if id, ok := n.Data["task"].(float64); ok {
		if task, err := a.Store.FindTask(ctx, int64(id)); err == nil {
			related["task"] = task.Console(0)
		}
	}

	return related
}
