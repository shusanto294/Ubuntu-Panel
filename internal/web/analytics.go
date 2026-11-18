package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
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

	props := inertia.Props{
		"window":    window.key,
		"windows":   windows,
		"site":      site,
		"sites":     domains,
		"unmatched": unmatchedSite,

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

// analyticsSlice is one bar of the page's chart opened: the requests between
// from and to — the pages asked for most then and the newest of the requests
// themselves. Read only when a bar is clicked, from the same files and with
// the same cap as the window it is a part of.
func (a *App) analyticsSlice(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	window := findTrafficWindow(query.Get("window"))
	site := a.analyticsSite(r.Context(), query.Get("site"))

	from, errFrom := strconv.ParseInt(query.Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(query.Get("to"), 10, 64)

	// A bar is a part of the window, and the window ends now; a little slack
	// for the chart having been drawn a while ago.
	now := time.Now()
	start, end := time.Unix(from, 0), time.Unix(to, 0)
	if errFrom != nil || errTo != nil || !end.After(start) || end.Sub(start) > window.span ||
		start.Before(now.Add(-window.span-time.Hour)) || start.After(now) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "That is not a part of the period."})
		return
	}

	if end.After(now) {
		end = now
	}

	ctx := context.WithoutCancel(r.Context())
	stats := a.trafficStats(ctx, a.readTraffic(ctx, window, site, start, end, 1, ""))

	a.writeJSON(w, http.StatusOK, map[string]any{
		"from":          from,
		"to":            to,
		"requests":      stats["requests"],
		"ips":           stats["ips"],
		"client_errors": stats["client_errors"],
		"server_errors": stats["server_errors"],
		"pages":         stats["pages"],
		"recent":        stats["recent"],
		"partial":       stats["partial"],
	})
}

// analyticsSite is the site a page was narrowed to, when it is one the panel
// has — or the requests no site matched. Anything else is every site.
func (a *App) analyticsSite(ctx context.Context, asked string) string {
	if asked == "" {
		return ""
	}

	if asked == unmatchedSite {
		return asked
	}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.Domain == asked {
				return asked
			}
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
