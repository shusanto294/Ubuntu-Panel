package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The security rules Cloudflare enforces in front of the server — WAF custom
// rules, rate limiting rules, IP access rules and the zone's security level —
// read and written through a DNS credential, from Security → Cloudflare. The
// page talks to these as JSON, from the panel's copy of Cloudflare
// (edge_cache.go), and each write keeps that copy true from Cloudflare's own
// answer. A refusal is shown beside the part of the page it concerns rather
// than as a flash over the whole of it. internal/dns/edge.go is the
// Cloudflare side.

// edgeAccounts are the user's credentials at a host that has edge rules, for
// the Security page's account picker.
func (a *App) edgeAccounts(ctx context.Context, userID int64) []map[string]any {
	accounts, err := a.Store.DNSAccounts(ctx, userID)
	if err != nil {
		return []map[string]any{}
	}

	out := []map[string]any{}
	for _, account := range accounts {
		if dns.SupportsEdgeRules(account.Provider) {
			out = append(out, map[string]any{
				"id":       account.ID,
				"label":    account.Label,
				"provider": dns.ProviderLabel(account.Provider),
			})
		}
	}

	return out
}

// siteDomains are the hostnames of the sites on this server, so the
// Cloudflare tab can tell which zones hold one.
func (a *App) siteDomains(ctx context.Context) []string {
	domains := []string{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			domains = append(domains, site.Domain)
		}
	}
	return domains
}

// edgeClient is the credential a request names, when it is the user's and at
// a host with edge rules, and the zone it asks about. It answers the request
// itself when it cannot be served.
func (a *App) edgeClient(w http.ResponseWriter, r *http.Request, zoneID string) (*dns.Client, *store.DNSAccount, bool) {
	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), userFrom(r.Context()).ID)
	if err != nil {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such DNS credential."})
		return nil, nil, false
	}

	if !dns.SupportsEdgeRules(account.Provider) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": dns.ProviderLabel(account.Provider) + " only answers DNS queries; it never sees the requests, so it has no security rules.",
		})
		return nil, nil, false
	}

	if !safeID(zoneID) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose a zone."})
		return nil, nil, false
	}

	return dns.New(a.Store, account), account, true
}

// safeID is a Cloudflare identifier: it goes into a URL path, so nothing but
// the hex and dashes they are made of.
func safeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}

	for _, c := range id {
		if !(c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}

	return true
}

func (a *App) edgeFailed(w http.ResponseWriter, err error) {
	a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
}

func (a *App) edgeDone(w http.ResponseWriter, r *http.Request, account *store.DNSAccount, zoneID, what, message string) {
	a.Log.Info("cloudflare security changed", "account", account.ID, "zone", zoneID, "change", what,
		"ip", session.ClientIP(r))
	a.writeJSON(w, http.StatusOK, map[string]any{
		"message": message,
		"zones":   a.zoneCopies(r.Context(), []edgeTarget{{ZoneID: zoneID}}),
	})
}

// edgeIndex is everything a zone's security is, from the panel's copy, or
// read from Cloudflare when there is none or ?refresh=1. Each part is asked
// for on its own, so a token that may read the WAF but not the access rules
// shows the one and says why it cannot show the other.
func (a *App) edgeIndex(w http.ResponseWriter, r *http.Request) {
	zoneID := r.URL.Query().Get("zone_id")

	client, account, ok := a.edgeClient(w, r, zoneID)
	if !ok {
		return
	}

	zone, cached := a.cachedZone(r.Context(), zoneID)
	if !cached || r.URL.Query().Get("refresh") == "1" {
		name := zoneID
		if cached {
			name = zone.ZoneName
		}
		zone = a.readZone(r.Context(), client, edgeTarget{Account: account.ID, ZoneID: zoneID, ZoneName: name})
	}

	errors := map[string]string{"custom": "", "ratelimit": "", "access": "", "level": ""}
	for key, message := range zone.Errors {
		errors[key] = message
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"custom":     zone.Custom,
		"ratelimit":  zone.RateLimit,
		"access":     zone.Access,
		"level":      zone.Level,
		"errors":     errors,
		"fetched_at": zone.FetchedAt,
	})
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// The rate limiting periods and timeouts Cloudflare accepts, in seconds. The
// Free plan takes 10 for both; the others are a paid plan's.
var (
	rateLimitPeriods  = []int{10, 60, 120, 300, 600, 3600}
	rateLimitTimeouts = []int{0, 10, 60, 120, 300, 600, 3600, 86400}
)

// edgeRuleInput is the rule a form describes, and the phase it is in.
func edgeRuleInput(input map[string]string) (string, dns.EdgeRule, string) {
	phase := dns.EdgePhases[input["phase"]]
	if phase == "" {
		return "", dns.EdgeRule{}, "Choose custom rules or rate limiting."
	}

	rule := dns.EdgeRule{
		Description: strings.TrimSpace(input["description"]),
		Expression:  strings.TrimSpace(input["expression"]),
		Action:      input["action"],
		Enabled:     boolInput(input, "enabled", true),
	}

	switch {
	case rule.Description == "":
		return "", rule, "Give the rule a name."
	case len(rule.Description) > 500:
		return "", rule, "The name is longer than Cloudflare keeps."
	case rule.Expression == "":
		return "", rule, "Say which requests the rule matches."
	case len(rule.Expression) > 4096:
		return "", rule, "The expression is longer than Cloudflare accepts (4,096 characters)."
	case !slices.Contains(dns.EdgeActions[phase], rule.Action):
		return "", rule, "Choose what the rule does."
	}

	if phase == dns.PhaseRateLimit {
		limit := &dns.RateLimit{
			Period:            int(intInput(input, "period", 10)),
			RequestsPerPeriod: int(intInput(input, "requests_per_period", 0)),
			MitigationTimeout: int(intInput(input, "mitigation_timeout", 10)),
		}

		// Counting per address is what the panel offers; a rule made in
		// Cloudflare's dashboard that counts by something else keeps it.
		if input["characteristics"] != "keep" {
			limit.Characteristics = []string{"cf.colo.id", "ip.src"}
		}

		switch {
		case !slices.Contains(rateLimitPeriods, limit.Period):
			return "", rule, "Choose a period Cloudflare offers."
		case limit.RequestsPerPeriod < 1 || limit.RequestsPerPeriod > 1000000:
			return "", rule, "Say how many requests an address may make in the period."
		case !slices.Contains(rateLimitTimeouts, limit.MitigationTimeout):
			return "", rule, "Choose how long Cloudflare keeps acting once the limit is passed."
		}

		rule.RateLimit = limit
	}

	return phase, rule, ""
}

// edgeRuleStore adds a custom or rate limiting rule.
func (a *App) edgeRuleStore(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	phase, rule, problem := edgeRuleInput(input)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	set, err := client.AddEdgeRule(input["zone_id"], phase, a.knownRuleset(r.Context(), input["zone_id"], phase), rule)
	a.keepRuleset(r.Context(), input["zone_id"], phase, set)
	if err != nil {
		a.edgeFailed(w, err)
		return
	}

	a.edgeDone(w, r, account, input["zone_id"], "rule added: "+rule.Description, "Rule “"+rule.Description+"” added.")
}

// edgeRuleUpdate rewrites one, or only switches it on or off.
func (a *App) edgeRuleUpdate(w http.ResponseWriter, r *http.Request) {
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
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such rule."})
		return
	}

	phase, rule, problem := edgeRuleInput(input)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	set, err := client.ChangeEdgeRule(input["zone_id"], phase, a.knownRuleset(r.Context(), input["zone_id"], phase), ruleID, rule)
	a.keepRuleset(r.Context(), input["zone_id"], phase, set)
	if err != nil {
		a.edgeFailed(w, err)
		return
	}

	a.edgeDone(w, r, account, input["zone_id"], "rule updated: "+rule.Description, "Rule “"+rule.Description+"” saved.")
}

// edgeRuleMove changes where a rule runs among the others.
func (a *App) edgeRuleMove(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	phase := dns.EdgePhases[input["phase"]]
	ruleID := r.PathValue("rule")
	position, err := strconv.Atoi(input["position"])
	if phase == "" || !safeID(ruleID) || err != nil {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Say where the rule goes."})
		return
	}

	set, err := client.PlaceEdgeRule(input["zone_id"], phase, a.knownRuleset(r.Context(), input["zone_id"], phase), ruleID, position)
	a.keepRuleset(r.Context(), input["zone_id"], phase, set)
	if err != nil {
		a.edgeFailed(w, err)
		return
	}

	a.edgeDone(w, r, account, input["zone_id"], "rule moved to "+input["position"], "Order saved.")
}

// edgeRuleDestroy removes one.
func (a *App) edgeRuleDestroy(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	client, account, ok := a.edgeClient(w, r, query.Get("zone_id"))
	if !ok {
		return
	}

	phase := dns.EdgePhases[query.Get("phase")]
	ruleID := r.PathValue("rule")
	if phase == "" || !safeID(ruleID) {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such rule."})
		return
	}

	set, err := client.RemoveEdgeRule(query.Get("zone_id"), phase, a.knownRuleset(r.Context(), query.Get("zone_id"), phase), ruleID)
	a.keepRuleset(r.Context(), query.Get("zone_id"), phase, set)
	if err != nil && !edgeIsGone(err) {
		a.edgeFailed(w, err)
		return
	}

	a.edgeDone(w, r, account, query.Get("zone_id"), "rule deleted: "+ruleID, "Rule deleted.")
}

// edgeAccessStore adds an IP access rule.
func (a *App) edgeAccessStore(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	if !slices.Contains(dns.AccessModes, input["mode"]) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose what the rule does."})
		return
	}

	value, err := dns.NormaliseAccessTarget(input["target"], input["value"])
	if err != nil {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + "."})
		return
	}

	notes := strings.TrimSpace(input["notes"])
	if len(notes) > 500 {
		notes = notes[:500]
	}

	rule := dns.AccessRule{Mode: input["mode"], Target: input["target"], Value: value, Notes: notes}
	made, err := client.AddAccessRule(input["zone_id"], rule)
	if err != nil {
		a.edgeFailed(w, err)
		return
	}
	a.keepAccessRule(r.Context(), input["zone_id"], made)

	a.edgeDone(w, r, account, input["zone_id"], "access rule added: "+rule.Mode+" "+value,
		"Access rule for "+value+" added.")
}

// edgeAccessUpdate changes what one does, or its note.
func (a *App) edgeAccessUpdate(w http.ResponseWriter, r *http.Request) {
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
	if !safeID(ruleID) || !slices.Contains(dns.AccessModes, input["mode"]) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose what the rule does."})
		return
	}

	notes := strings.TrimSpace(input["notes"])
	if len(notes) > 500 {
		notes = notes[:500]
	}

	if err := client.UpdateAccessRule(input["zone_id"], ruleID, input["mode"], notes); err != nil {
		if edgeIsGone(err) {
			a.dropAccessRule(r.Context(), input["zone_id"], ruleID)
		}
		a.edgeFailed(w, err)
		return
	}
	a.changeAccessRule(r.Context(), input["zone_id"], ruleID, input["mode"], notes)

	a.edgeDone(w, r, account, input["zone_id"], "access rule updated: "+ruleID+" "+input["mode"], "Access rule saved.")
}

// edgeAccessDestroy removes one.
func (a *App) edgeAccessDestroy(w http.ResponseWriter, r *http.Request) {
	zoneID := r.URL.Query().Get("zone_id")

	client, account, ok := a.edgeClient(w, r, zoneID)
	if !ok {
		return
	}

	ruleID := r.PathValue("rule")
	if !safeID(ruleID) {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such rule."})
		return
	}

	if err := client.DeleteAccessRule(zoneID, ruleID); err != nil && !edgeIsGone(err) {
		a.edgeFailed(w, err)
		return
	}
	a.dropAccessRule(r.Context(), zoneID, ruleID)

	a.edgeDone(w, r, account, zoneID, "access rule deleted: "+ruleID, "Access rule deleted.")
}

// edgeLevel sets the zone's security level — how suspicious a visitor has to
// look before Cloudflare challenges them, up to Under Attack mode's check on
// everyone.
func (a *App) edgeLevel(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, account, ok := a.edgeClient(w, r, input["zone_id"])
	if !ok {
		return
	}

	if !slices.Contains(dns.SecurityLevels, input["level"]) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose a security level."})
		return
	}

	if err := client.SetSecurityLevel(input["zone_id"], input["level"]); err != nil {
		a.edgeFailed(w, err)
		return
	}
	a.keepLevel(r.Context(), input["zone_id"], input["level"])

	a.edgeDone(w, r, account, input["zone_id"], "security level: "+input["level"], "Security level saved.")
}
