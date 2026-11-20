package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The Security → Cloudflare tab works from a copy of Cloudflare kept in the
// panel's own database, so opening it costs Cloudflare nothing.
//
// Cloudflare is read in three cases only: a zone or an account the panel has
// never read, the Refresh button, and a write that finds its copy out of date
// (a rule deleted in Cloudflare's dashboard since). Every write answers with
// what Cloudflare holds afterwards, and that goes into the copy — so a change
// is one request to each zone it touches, never a write and then a read of
// everything, and the page shows the result without asking again.
//
// What is lost is noticing, by itself, a change made in Cloudflare's own
// dashboard. The page says when each copy was read and has the button.

func edgeZoneKey(zoneID string) string    { return "zone:" + zoneID }
func edgeZonesKey(accountID int64) string { return "zones:" + strconv.FormatInt(accountID, 10) }

// edgeIsGone is Cloudflare saying the rule is not there any more.
func edgeIsGone(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "404") ||
		strings.Contains(message, "does not exist") || strings.Contains(message, "no such rule")
}

// edgeIsDuplicate is Cloudflare refusing a rule the zone already has.
func edgeIsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") || strings.Contains(message, "already exists")
}

// edgeCacheMu makes each change to a zone's copy a read, a change and a write
// that nothing else interleaves with — the bulk handlers write four zones at
// once, and 404 protection writes from the background.
var edgeCacheMu sync.Mutex

// cachedZone is a zone's copy, if the panel has one.
func (a *App) cachedZone(ctx context.Context, zoneID string) (edgeZoneRules, bool) {
	body, _, ok := a.Store.EdgeCache(ctx, edgeZoneKey(zoneID))
	if !ok {
		return edgeZoneRules{}, false
	}

	var zone edgeZoneRules
	if json.Unmarshal([]byte(body), &zone) != nil || zone.ZoneID == "" {
		return edgeZoneRules{}, false
	}

	zone.Custom, zone.RateLimit, zone.Access = orEmpty(zone.Custom), orEmpty(zone.RateLimit), orEmpty(zone.Access)
	if zone.Errors == nil {
		zone.Errors = map[string]string{}
	}

	return zone, true
}

func (a *App) storeZone(ctx context.Context, zone edgeZoneRules) {
	body, err := json.Marshal(zone)
	if err != nil {
		return
	}

	if err := a.Store.PutEdgeCache(ctx, edgeZoneKey(zone.ZoneID), string(body), time.Unix(zone.FetchedAt, 0)); err != nil {
		a.Log.Warn("could not keep a copy of a zone's Cloudflare rules", "zone", zone.ZoneName, "error", err)
	}
}

// patchZone changes a zone's copy in place. A zone with no copy is left
// without one: the next look reads it whole.
func (a *App) patchZone(ctx context.Context, zoneID string, change func(*edgeZoneRules)) {
	edgeCacheMu.Lock()
	defer edgeCacheMu.Unlock()

	zone, ok := a.cachedZone(ctx, zoneID)
	if !ok {
		return
	}

	change(&zone)
	a.storeZone(ctx, zone)
}

// readZone reads a zone's security from Cloudflare — the four requests the
// copy saves — and keeps it.
func (a *App) readZone(ctx context.Context, client *dns.Client, target edgeTarget) edgeZoneRules {
	zone := edgeZoneRules{
		Account: target.Account, ZoneID: target.ZoneID, ZoneName: target.ZoneName,
		Errors: map[string]string{}, FetchedAt: time.Now().Unix(),
	}

	var (
		custom, limit                         dns.Ruleset
		errCustom, errLimit, errAccess, errLv error
		wait                                  sync.WaitGroup
	)

	wait.Add(4)
	go func() { defer wait.Done(); custom, errCustom = client.ReadRuleset(target.ZoneID, dns.PhaseCustom) }()
	go func() { defer wait.Done(); limit, errLimit = client.ReadRuleset(target.ZoneID, dns.PhaseRateLimit) }()
	go func() { defer wait.Done(); zone.Access, errAccess = client.AccessRules(target.ZoneID) }()
	go func() { defer wait.Done(); zone.Level, errLv = client.SecurityLevel(target.ZoneID) }()
	wait.Wait()

	zone.Custom, zone.CustomRuleset = custom.Rules, custom.ID
	zone.RateLimit, zone.RateLimitRuleset = limit.Rules, limit.ID

	for key, err := range map[string]error{"custom": errCustom, "ratelimit": errLimit, "access": errAccess, "level": errLv} {
		if err != nil {
			zone.Errors[key] = err.Error()
		}
	}

	zone.Custom, zone.RateLimit, zone.Access = orEmpty(zone.Custom), orEmpty(zone.RateLimit), orEmpty(zone.Access)

	edgeCacheMu.Lock()
	a.storeZone(ctx, zone)
	edgeCacheMu.Unlock()

	return zone
}

// knownRuleset is a phase's ruleset from the copy, for a write to start from.
// nil when there is no copy or the copy could not read that phase.
func (a *App) knownRuleset(ctx context.Context, zoneID, phase string) *dns.Ruleset {
	zone, ok := a.cachedZone(ctx, zoneID)
	if !ok {
		return nil
	}

	key := phaseKey(phase)
	if zone.Errors[key] != "" {
		return nil
	}

	if key == "custom" {
		return &dns.Ruleset{ID: zone.CustomRuleset, Rules: zone.Custom}
	}

	return &dns.Ruleset{ID: zone.RateLimitRuleset, Rules: zone.RateLimit}
}

func phaseKey(phase string) string {
	if phase == dns.PhaseRateLimit {
		return "ratelimit"
	}
	return "custom"
}

// keepRuleset puts what Cloudflare answered a write with into the copy. A
// zero Ruleset is a write that learned nothing, and changes nothing.
func (a *App) keepRuleset(ctx context.Context, zoneID, phase string, set dns.Ruleset) {
	if set.Rules == nil {
		return
	}

	a.patchZone(ctx, zoneID, func(zone *edgeZoneRules) {
		delete(zone.Errors, phaseKey(phase))

		if phaseKey(phase) == "custom" {
			zone.Custom, zone.CustomRuleset = set.Rules, set.ID
		} else {
			zone.RateLimit, zone.RateLimitRuleset = set.Rules, set.ID
		}
	})
}

// keepAccessRule adds or replaces one access rule in the copy.
func (a *App) keepAccessRule(ctx context.Context, zoneID string, rule dns.AccessRule) {
	if rule.ID == "" {
		return
	}

	if rule.Scope == "" {
		rule.Scope = "zone"
	}
	if rule.CreatedOn == "" {
		rule.CreatedOn = time.Now().UTC().Format(time.RFC3339)
	}

	a.patchZone(ctx, zoneID, func(zone *edgeZoneRules) {
		for i, have := range zone.Access {
			if have.ID == rule.ID {
				zone.Access[i] = rule
				return
			}
		}
		zone.Access = append([]dns.AccessRule{rule}, zone.Access...)
	})
}

// changeAccessRule sets what a rule in the copy does and its note.
func (a *App) changeAccessRule(ctx context.Context, zoneID, ruleID, mode, notes string) {
	a.patchZone(ctx, zoneID, func(zone *edgeZoneRules) {
		for i := range zone.Access {
			if zone.Access[i].ID == ruleID {
				zone.Access[i].Mode, zone.Access[i].Notes = mode, notes
			}
		}
	})
}

// dropAccessRule takes one out of the copy.
func (a *App) dropAccessRule(ctx context.Context, zoneID, ruleID string) {
	a.patchZone(ctx, zoneID, func(zone *edgeZoneRules) {
		kept := zone.Access[:0]
		for _, rule := range zone.Access {
			if rule.ID != ruleID {
				kept = append(kept, rule)
			}
		}
		zone.Access = kept
	})
}

// keepLevel sets the copy's security level.
func (a *App) keepLevel(ctx context.Context, zoneID, level string) {
	a.patchZone(ctx, zoneID, func(zone *edgeZoneRules) {
		zone.Level = level
		delete(zone.Errors, "level")
	})
}

// forgetZone drops a zone's copy, for a change made some way that does not
// say what Cloudflare holds afterwards.
func (a *App) forgetZone(ctx context.Context, zoneID string) {
	a.Store.ForgetEdgeCache(ctx, edgeZoneKey(zoneID))
}

// zoneCopies are the copies of the zones a change touched, for the page to
// put in place of its own.
func (a *App) zoneCopies(ctx context.Context, targets []edgeTarget) []edgeZoneRules {
	seen := map[string]bool{}
	zones := []edgeZoneRules{}

	for _, target := range targets {
		if seen[target.ZoneID] {
			continue
		}
		seen[target.ZoneID] = true

		if zone, ok := a.cachedZone(ctx, target.ZoneID); ok {
			zones = append(zones, zone)
		}
	}

	return zones
}

// ----------------------------------------------------------------- zones

// accountZones are a Cloudflare account's zones: the copy, or Cloudflare's
// list when there is none or refresh asks for it.
func (a *App) accountZones(ctx context.Context, account *store.DNSAccount, refresh bool) ([]dns.Zone, time.Time, error) {
	key := edgeZonesKey(account.ID)

	if !refresh {
		if body, at, ok := a.Store.EdgeCache(ctx, key); ok {
			var zones []dns.Zone
			if json.Unmarshal([]byte(body), &zones) == nil {
				return zones, at, nil
			}
		}
	}

	zones, err := dns.New(a.Store, account).Zones()
	if err != nil {
		return nil, time.Time{}, err
	}

	now := time.Now()
	if body, err := json.Marshal(zones); err == nil {
		a.Store.PutEdgeCache(ctx, key, string(body), now)
	}

	return zones, now, nil
}

// edgeZones is every zone on the user's Cloudflare accounts, from the copy.
// ?refresh=1 reads Cloudflare's lists again.
func (a *App) edgeZones(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	refresh := r.URL.Query().Get("refresh") == "1"

	accounts, err := a.Store.DNSAccounts(r.Context(), user.ID)
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"accounts": []any{}})
		return
	}

	type answer struct {
		ID        int64      `json:"id"`
		Label     string     `json:"label"`
		Zones     []dns.Zone `json:"zones"`
		Error     string     `json:"error,omitempty"`
		FetchedAt int64      `json:"fetched_at"`
	}

	out := []answer{}

	for _, account := range accounts {
		if !dns.SupportsEdgeRules(account.Provider) {
			continue
		}

		zones, at, err := a.accountZones(r.Context(), account, refresh)
		row := answer{ID: account.ID, Label: account.Label, Zones: orEmpty(zones)}
		if err != nil {
			row.Error = err.Error()
		} else {
			row.FetchedAt = at.Unix()
		}
		out = append(out, row)
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}
