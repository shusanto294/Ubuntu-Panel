package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// Redirects — a hostname, or a path on it, sent somewhere else by Cloudflare
// before the request reaches any server. Any domain on a connected Cloudflare
// account can have them, whether or not it has a site here: a mail domain's
// bare apex is the usual one. internal/dns/redirect.go is the Cloudflare side.
//
// Like the Security → Cloudflare tab, the page works from a copy of what
// Cloudflare last said (in edge_cache), read when a zone is first opened or
// the Refresh button asks, and kept true by Cloudflare's answer to each write.

func redirectsKey(zoneID string) string { return "redirects:" + zoneID }

// redirectZone is a zone's redirects as the panel last read them.
type redirectZone struct {
	Account   int64          `json:"account"`
	ZoneID    string         `json:"zone_id"`
	ZoneName  string         `json:"zone_name"`
	Ruleset   string         `json:"ruleset"`
	Rules     []dns.EdgeRule `json:"rules"`
	FetchedAt int64          `json:"fetched_at"`
}

func (a *App) cachedRedirects(ctx context.Context, zoneID string) (redirectZone, bool) {
	body, _, ok := a.Store.EdgeCache(ctx, redirectsKey(zoneID))
	if !ok {
		return redirectZone{}, false
	}

	var zone redirectZone
	if json.Unmarshal([]byte(body), &zone) != nil || zone.ZoneID == "" {
		return redirectZone{}, false
	}
	zone.Rules = orEmpty(zone.Rules)

	return zone, true
}

func (a *App) keepRedirects(ctx context.Context, zone redirectZone) {
	body, err := json.Marshal(zone)
	if err != nil {
		return
	}
	if err := a.Store.PutEdgeCache(ctx, redirectsKey(zone.ZoneID), string(body), time.Unix(zone.FetchedAt, 0)); err != nil {
		a.Log.Warn("could not keep a copy of a zone's redirects", "zone", zone.ZoneName, "error", err)
	}
}

// redirectsAnswer is a zone as the page draws it.
func redirectsAnswer(zone redirectZone) map[string]any {
	return map[string]any{
		"account":    zone.Account,
		"zone_id":    zone.ZoneID,
		"zone_name":  zone.ZoneName,
		"redirects":  dns.ParseRedirects(dns.Ruleset{ID: zone.Ruleset, Rules: zone.Rules}),
		"fetched_at": zone.FetchedAt,
	}
}

// redirectsFailed answers a refusal, saying which permission a token made
// only for DNS is missing when that is what Cloudflare means.
func (a *App) redirectsFailed(w http.ResponseWriter, err error) {
	message := err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "authentication") || strings.Contains(lower, "not authorized") ||
		strings.Contains(lower, "permission") || strings.Contains(lower, "unauthorized") {
		message += " — the API token needs Zone · Single Redirect · Edit (as well as Zone · DNS · Edit) on this zone."
	}

	body := map[string]any{"error": message}
	var notProxied *dns.NotProxiedError
	if errors.As(err, &notProxied) {
		body["needs_proxy"] = !notProxied.Mail
	}

	a.writeJSON(w, http.StatusUnprocessableEntity, body)
}

// redirectsIndex is the page: the Cloudflare credentials to pick zones from.
// The zones and their redirects are asked for by the page itself.
func (a *App) redirectsIndex(w http.ResponseWriter, r *http.Request) {
	a.Render.Render(w, r, "Redirects/Index", inertia.Props{
		"accounts": a.edgeAccounts(r.Context(), userFrom(r.Context()).ID),
		"codes":    dns.RedirectCodes,
	})
}

// redirectsList is a zone's redirects: the copy, or Cloudflare's when there
// is none or ?refresh=1.
func (a *App) redirectsList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	zoneID := query.Get("zone_id")

	client, account, ok := a.edgeClient(w, r, zoneID)
	if !ok {
		return
	}

	zone, cached := a.cachedRedirects(r.Context(), zoneID)
	if cached && query.Get("refresh") != "1" {
		a.writeJSON(w, http.StatusOK, redirectsAnswer(zone))
		return
	}

	set, err := client.ReadRuleset(zoneID, dns.PhaseRedirect)
	if err != nil {
		a.redirectsFailed(w, err)
		return
	}

	name := strings.ToLower(query.Get("zone_name"))
	if !dns.ValidHost(name) {
		name = zone.ZoneName
	}

	zone = redirectZone{
		Account: account.ID, ZoneID: zoneID, ZoneName: name,
		Ruleset: set.ID, Rules: orEmpty(set.Rules), FetchedAt: time.Now().Unix(),
	}
	a.keepRedirects(r.Context(), zone)

	a.writeJSON(w, http.StatusOK, redirectsAnswer(zone))
}

// redirectInput is the redirect a form describes, inside the zone it names.
func redirectInput(input map[string]string, zoneName string) (dns.Redirect, string) {
	redirect := dns.Redirect{
		Hosts:      []string{strings.ToLower(strings.TrimSpace(input["host"]))},
		PathMode:   input["path_mode"],
		Path:       input["path"],
		Target:     input["target"],
		StatusCode: int(intInput(input, "status_code", 301)),
		KeepPath:   boolInput(input, "keep_path", false),
		KeepQuery:  boolInput(input, "keep_query", true),
		Enabled:    boolInput(input, "enabled", true),
	}

	if boolInput(input, "include_www", false) && !strings.HasPrefix(redirect.Hosts[0], "www.") {
		redirect.Hosts = append(redirect.Hosts, "www."+redirect.Hosts[0])
	}

	if err := dns.CheckRedirect(&redirect); err != nil {
		return redirect, err.Error()
	}

	for _, host := range redirect.Hosts {
		if host != zoneName && !strings.HasSuffix(host, "."+zoneName) {
			return redirect, host + " is not in " + zoneName + "."
		}
	}

	// Sending a hostname to itself is a loop the browser gives up on.
	for _, host := range redirect.Hosts {
		if strings.EqualFold(hostOf(redirect.Target), host) && redirect.PathMode == dns.PathAll {
			return redirect, "That sends " + host + " to itself — every visit would loop."
		}
	}

	return redirect, ""
}

func hostOf(target string) string {
	rest := target[strings.Index(target, "//")+2:]
	if end := strings.IndexAny(rest, "/?#:"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// knownRedirects is the zone's ruleset from the copy, for a write to start
// from, and the copy itself.
func (a *App) knownRedirects(ctx context.Context, zoneID string) (*dns.Ruleset, redirectZone) {
	zone, ok := a.cachedRedirects(ctx, zoneID)
	if !ok {
		return nil, zone
	}
	return &dns.Ruleset{ID: zone.Ruleset, Rules: zone.Rules}, zone
}

// redirectsWritten keeps what Cloudflare answered and tells the page.
func (a *App) redirectsWritten(w http.ResponseWriter, r *http.Request, zone redirectZone, set dns.Ruleset,
	what, message string, notes []string) {
	zone.Ruleset, zone.Rules, zone.FetchedAt = set.ID, orEmpty(set.Rules), time.Now().Unix()
	a.keepRedirects(r.Context(), zone)

	a.Log.Info("cloudflare redirects changed", "account", zone.Account, "zone", zone.ZoneName, "change", what,
		"ip", session.ClientIP(r))

	answer := redirectsAnswer(zone)
	answer["message"] = message
	answer["notes"] = orEmpty(notes)
	a.writeJSON(w, http.StatusOK, answer)
}

// redirectZoneFrom is the zone a write names, from the input and the copy.
func redirectZoneFrom(input map[string]string, accountID int64, copy redirectZone) (redirectZone, string) {
	name := strings.ToLower(strings.TrimSpace(input["zone_name"]))
	if name == "" {
		name = copy.ZoneName
	}
	if !dns.ValidHost(name) {
		return redirectZone{}, "Choose a domain."
	}

	return redirectZone{Account: accountID, ZoneID: input["zone_id"], ZoneName: name}, ""
}

// redirectsStore adds a redirect, making its hostname resolve through
// Cloudflare first.
func (a *App) redirectsStore(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	known, copy := a.knownRedirects(r.Context(), input["zone_id"])
	zone, problem := redirectZoneFrom(input, account.ID, copy)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	redirect, problem := redirectInput(input, zone.ZoneName)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	notes, err := client.EnsureProxied(zone.ZoneID, zone.ZoneName, redirect.Hosts, boolInput(input, "proxy", false))
	if err != nil {
		a.redirectsFailed(w, err)
		return
	}

	rule := dns.RedirectRule(redirect)
	set, err := client.AddEdgeRule(zone.ZoneID, dns.PhaseRedirect, known, rule)
	if err != nil {
		a.redirectsFailed(w, err)
		return
	}

	a.redirectsWritten(w, r, zone, set, "added: "+rule.Description, "Redirect added.", notes)
}

// redirectsUpdate rewrites a redirect the panel made, or only switches any
// redirect on or off.
func (a *App) redirectsUpdate(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	ruleID := r.PathValue("rule")
	if !safeID(ruleID) {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such redirect."})
		return
	}

	known, copy := a.knownRedirects(r.Context(), input["zone_id"])
	zone, problem := redirectZoneFrom(input, account.ID, copy)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	var (
		rule  dns.EdgeRule
		notes []string
	)

	if input["only"] == "enabled" {
		if known == nil {
			fresh, err := client.ReadRuleset(zone.ZoneID, dns.PhaseRedirect)
			if err != nil {
				a.redirectsFailed(w, err)
				return
			}
			known = &fresh
		}

		found := false
		for _, have := range known.Rules {
			if have.ID == ruleID {
				rule = dns.EdgeRule{Description: have.Description, Expression: have.Expression,
					Action: have.Action, Enabled: boolInput(input, "enabled", true)}
				found = true
			}
		}
		if !found {
			a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "Cloudflare has no such redirect any more — refresh the list."})
			return
		}
	} else {
		redirect, problem := redirectInput(input, zone.ZoneName)
		if problem != "" {
			a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
			return
		}

		notes, err = client.EnsureProxied(zone.ZoneID, zone.ZoneName, redirect.Hosts, boolInput(input, "proxy", false))
		if err != nil {
			a.redirectsFailed(w, err)
			return
		}

		rule = dns.RedirectRule(redirect)
	}

	set, err := client.ChangeEdgeRule(zone.ZoneID, dns.PhaseRedirect, known, ruleID, rule)
	if err != nil {
		a.redirectsFailed(w, err)
		return
	}

	message := "Redirect saved."
	if input["only"] == "enabled" {
		message = map[bool]string{true: "Redirect switched on.", false: "Redirect switched off."}[rule.Enabled]
	}

	a.redirectsWritten(w, r, zone, set, "updated: "+rule.Description, message, notes)
}

// redirectsDestroy removes one. The DNS record it may have added stays: it
// may be serving something else by now, and an unused proxied placeholder
// costs nothing.
func (a *App) redirectsDestroy(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	zoneID := query.Get("zone_id")

	client, account, ok := a.edgeClient(w, r, zoneID)
	if !ok {
		return
	}

	ruleID := r.PathValue("rule")
	if !safeID(ruleID) {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such redirect."})
		return
	}

	known, copy := a.knownRedirects(r.Context(), zoneID)
	zone, problem := redirectZoneFrom(map[string]string{"zone_id": zoneID, "zone_name": query.Get("zone_name")}, account.ID, copy)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	set, err := client.RemoveEdgeRule(zoneID, dns.PhaseRedirect, known, ruleID)
	if err != nil && !edgeIsGone(err) {
		a.redirectsFailed(w, err)
		return
	}
	if set.Rules == nil {
		// Already gone: read what is left so the copy is right.
		if set, err = client.ReadRuleset(zoneID, dns.PhaseRedirect); err != nil {
			a.redirectsFailed(w, err)
			return
		}
	}

	a.redirectsWritten(w, r, zone, set, "deleted: "+ruleID, "Redirect deleted.", nil)
}
