package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// analyticsIndex is the Analytics page: what the sites' traffic adds up to
// over a period, for every site or one. Its totals, the timeline, the sites
// and the pages visited most, the status codes, where visitors came from,
// who sent the requests, what they said they were and which of them attacked.
//
// It is the access logs read and added up (traffic.go). Like the boxes that
// used to sit on the Logs page, nothing is read on the first paint: the page
// is up at once and asks for the report after, because reading a busy
// machine's logs takes seconds.
func (a *App) analyticsIndex(w http.ResponseWriter, r *http.Request) {
	window := findTrafficWindow(r.URL.Query().Get("window"))
	site := a.analyticsSite(r.Context(), r.URL.Query().Get("site"))

	windows := make([]string, 0, len(trafficWindows))
	for _, w := range trafficWindows {
		windows = append(windows, w.key)
	}

	domains := []string{}
	if sites, err := a.Store.Sites(r.Context()); err == nil {
		for _, s := range sites {
			domains = append(domains, s.Domain)
		}
	}

	// The bar of the chart that is open, kept in the address so a spike can
	// be reloaded, bookmarked or sent on. Read when the page is up.
	from, to, _, _, ok := periodBounds(r.URL.Query(), window)
	if !ok {
		from, to = 0, 0
	}

	props := inertia.Props{
		"window":    window.key,
		"windows":   windows,
		"site":      site,
		"sites":     a.trafficNames(r.Context()),
		"unmatched": unmatchedSite,
		"from":      from,
		"to":        to,

		// Blocking an address is a Cloudflare IP access rule on the sites'
		// zones, so it is offered only where there is a Cloudflare account.
		"edgeAccounts": a.edgeAccounts(r.Context(), userFrom(r.Context()).ID),
		"siteDomains":  domains,
	}

	if inertia.Asked(r, "Analytics/Index", "traffic") {
		ctx := context.WithoutCancel(r.Context())

		report, reasons := a.markTrusted(ctx, a.siteTraffic(ctx, window, site), session.ClientIP(r))

		props["traffic"] = report
		props["trustedIps"] = reasons
		props["stats"] = a.trafficStats(ctx, report)
	}

	a.Render.Render(w, r, "Analytics/Index", props)
}

// periodBounds are the from and to a request asked for: one bar of the
// chart, or any part of the window. A bar is a part of the window, and the
// window ends now; a little slack for the chart having been drawn a while
// ago. Neither given is the whole window.
func periodBounds(query url.Values, window trafficWindow) (from, to int64, start, end time.Time, ok bool) {
	now := time.Now()

	if query.Get("from") == "" && query.Get("to") == "" {
		return 0, 0, now.Add(-window.span), now, true
	}

	from, errFrom := strconv.ParseInt(query.Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(query.Get("to"), 10, 64)
	start, end = time.Unix(from, 0), time.Unix(to, 0)
	if errFrom != nil || errTo != nil || !end.After(start) || end.Sub(start) > window.span ||
		start.Before(now.Add(-window.span-time.Hour)) || start.After(now) {
		return 0, 0, time.Time{}, time.Time{}, false
	}

	if end.After(now) {
		end = now
	}

	return from, to, start, end, true
}

// periodFeed is how many of the newest requests a period's page can list,
// twenty a page: a bar of the chart holds them all; a whole busy day is the
// newest of it.
const periodFeed = 10000

// periodCache keeps a period's read while its pages are turned: every
// request, not only the pages, which a few page turns would otherwise read
// again. Each read expires as the window's do, and only a few are kept.
var periodCache struct {
	sync.Mutex
	entries map[string]periodEntry
}

type periodEntry struct {
	at     time.Time
	keep   time.Duration
	report trafficReport
}

// periodTraffic is every request between start and end — the URLs asked for
// most, all of them, and the newest requests of any kind, of the answers
// asked for ("", "ok" or "errors").
func (a *App) periodTraffic(ctx context.Context, window trafficWindow, site string, from, to int64, start, end time.Time, status string) trafficReport {
	key := fmt.Sprintf("%s|%s|%d|%d|%s", window.key, site, from, to, status)

	periodCache.Lock()
	if entry, ok := periodCache.entries[key]; ok && time.Since(entry.at) < entry.keep {
		periodCache.Unlock()
		return entry.report
	}
	periodCache.Unlock()

	report := a.readTraffic(ctx, window, site, start, end, 1, "", func(an *weblog.Analyzer) {
		an.Feed = periodFeed
		an.Everything = true
		an.FeedStatus = status
		an.AllPaths = true
	})

	periodCache.Lock()
	defer periodCache.Unlock()

	if periodCache.entries == nil {
		periodCache.entries = map[string]periodEntry{}
	}
	for k, entry := range periodCache.entries {
		if time.Since(entry.at) >= entry.keep {
			delete(periodCache.entries, k)
		}
	}
	for len(periodCache.entries) >= 4 {
		oldest := ""
		for k, entry := range periodCache.entries {
			if oldest == "" || entry.at.Before(periodCache.entries[oldest].at) {
				oldest = k
			}
		}
		delete(periodCache.entries, oldest)
	}
	stored := time.Now()
	periodCache.entries[key] = periodEntry{at: stored, keep: window.keep, report: report}

	// Dropped once it has expired, so an idle panel holds none of it.
	time.AfterFunc(window.keep, func() {
		periodCache.Lock()
		defer periodCache.Unlock()
		if entry, ok := periodCache.entries[key]; ok && entry.at.Equal(stored) {
			delete(periodCache.entries, key)
		}
	})

	return report
}

// urlRows are the paths as the pages list them, most requested first. Only a
// page some of whose answers succeeded is a link: the rest are a scanner's
// guesses.
func urlRows(base map[string]string, paths []weblog.PathStat) []map[string]any {
	rows := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		url := ""
		if base[p.Site] != "" && p.Threat == "" && p.Failed < p.Requests {
			url = base[p.Site] + p.Path
		}
		rows = append(rows, map[string]any{
			"site":     p.Site,
			"path":     p.Path,
			"url":      url,
			"requests": p.Requests,
			"ips":      p.IPs,
			"failed":   p.Failed,
			"threat":   p.Threat,
		})
	}
	return rows
}

// periodPerPage is how many rows a list shows at a time.
const periodPerPage = 20

// periodSummary is what the pages show of a period: its totals, and the URLs
// and requests — the first page of each, or one page of one of them.
func (a *App) periodSummary(ctx context.Context, report trafficReport, status, view string, page int) map[string]any {
	base := a.siteBases(ctx)

	// How many requests have the answers asked for: every one the feed could
	// have kept, which it did unless there were more than it holds.
	matching := report.Requests
	switch status {
	case "ok":
		matching = report.Requests - report.Statuses["4xx"] - report.Statuses["5xx"]
	case "errors":
		matching = report.Statuses["4xx"] + report.Statuses["5xx"]
	}

	cut := func(total int, list func(lo, hi int) []map[string]any, only string) (rows []map[string]any, pages int, at int) {
		pages = max(1, (total+periodPerPage-1)/periodPerPage)
		at = 1
		if view == only {
			at = min(max(1, page), pages)
		}
		lo := min(total, (at-1)*periodPerPage)
		hi := min(total, lo+periodPerPage)
		return list(lo, hi), pages, at
	}

	urls, urlPages, urlPage := cut(len(report.Paths), func(lo, hi int) []map[string]any {
		return urlRows(base, report.Paths[lo:hi])
	}, "urls")

	recent, recentPages, recentPage := cut(len(report.Recent), func(lo, hi int) []map[string]any {
		return requestRows(base, report.Recent[lo:hi])
	}, "requests")

	return map[string]any{
		"requests":      report.Requests,
		"ips":           report.IPs,
		"client_errors": report.Statuses["4xx"],
		"server_errors": report.Statuses["5xx"],
		"partial":       report.Partial,

		"urls":       urls,
		"urls_total": len(report.Paths),
		"urls_pages": urlPages,
		"urls_page":  urlPage,

		"recent":        recent,
		"recent_total":  len(report.Recent),
		"recent_pages":  recentPages,
		"recent_page":   recentPage,
		"matching":      matching,
		"recent_capped": matching > len(report.Recent),

		"per_page": periodPerPage,
	}
}

// analyticsPeriod is a part of the window on a page of its own — one bar of
// the chart, or the whole window — with the URLs asked for most and the
// newest requests, twenty of each, and each in full a page at a time. All of
// it is in the address, so a spike can be bookmarked or sent on.
func (a *App) analyticsPeriod(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	window := findTrafficWindow(query.Get("window"))
	site := a.analyticsSite(r.Context(), query.Get("site"))

	from, to, start, end, ok := periodBounds(query, window)
	if !ok {
		from, to, start, end, _ = periodBounds(url.Values{}, window)
	}

	view := query.Get("view")
	if view != "urls" && view != "requests" {
		view = ""
	}

	status := query.Get("status")
	if status != "ok" && status != "errors" {
		status = ""
	}

	page, _ := strconv.Atoi(query.Get("page"))
	page = max(1, page)

	props := inertia.Props{
		"window":    window.key,
		"site":      site,
		"from":      from,
		"to":        to,
		"view":      view,
		"status":    status,
		"page":      page,
		"unmatched": unmatchedSite,
	}

	if inertia.Asked(r, "Analytics/Period", "period") {
		ctx := context.WithoutCancel(r.Context())
		report := a.periodTraffic(ctx, window, site, from, to, start, end, status)
		props["period"] = a.periodSummary(ctx, report, status, view, page)
	}

	a.Render.Render(w, r, "Analytics/Period", props)
}

// analyticsSlice is one bar of the page's chart opened, for the box under the
// chart: the period's first page of URLs and requests, from the same read
// its own page lists in full, so opening that is at once.
func (a *App) analyticsSlice(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	window := findTrafficWindow(query.Get("window"))
	site := a.analyticsSite(r.Context(), query.Get("site"))

	from, to, start, end, ok := periodBounds(query, window)
	if !ok || from == 0 {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "That is not a part of the period."})
		return
	}

	ctx := context.WithoutCancel(r.Context())
	summary := a.periodSummary(ctx, a.periodTraffic(ctx, window, site, from, to, start, end, ""), "", "", 1)
	summary["from"] = from
	summary["to"] = to

	a.writeJSON(w, http.StatusOK, summary)
}

// analyticsSite is the site a page was narrowed to, when it is one the panel
// has a log for — a site, a mail hostname — or the requests no site matched.
// Anything else is every site.
func (a *App) analyticsSite(ctx context.Context, asked string) string {
	if asked == "" {
		return ""
	}

	if asked == unmatchedSite {
		return asked
	}

	for _, name := range a.trafficNames(ctx) {
		if name == asked {
			return asked
		}
	}

	return ""
}

// analyticsMoved sends an old /logs/stats or /logs/ip address to the page it
// is now, with its query, so a bookmark or an old notification still lands.
func (a *App) analyticsMoved(target func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		location := target(r)
		if r.URL.RawQuery != "" {
			location += "?" + r.URL.RawQuery
		}

		http.Redirect(w, r, location, http.StatusFound)
	}
}
