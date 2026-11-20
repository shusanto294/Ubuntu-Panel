package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// Adding one rule to many zones at once — every site's, usually — rather than
// zone by zone. The zones can be on different Cloudflare accounts: each target
// names its credential, which has to be the user's.
//
// A zone that already has the rule is left alone and said so, so running the
// same "add to every site" twice, or after adding a site, does not make
// copies. Each zone succeeds or fails on its own; the answer lists them all.

// edgeTarget is one zone a bulk add goes to.
type edgeTarget struct {
	Account  int64  `json:"account"`
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`

	// RuleID is the zone's own copy of a rule, when the change is to rules
	// that already exist rather than a new one.
	RuleID string `json:"rule_id,omitempty"`
}

// edgeOutcome is what happened in one zone.
type edgeOutcome struct {
	Zone   string `json:"zone"`
	Status string `json:"status"` // "added", "done", "exists" or "failed"
	Error  string `json:"error,omitempty"`
}

// maxBulkZones keeps one click from becoming thousands of API calls by
// mistake; Cloudflare allows 1,200 requests in five minutes per user.
const maxBulkZones = 200

// edgeTargets reads the zones a request names, each with a client for its
// credential. It answers the request itself when they cannot be used.
func (a *App) edgeTargets(w http.ResponseWriter, r *http.Request, raw string) ([]edgeTarget, map[int64]*dns.Client, bool) {
	var targets []edgeTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil || len(targets) == 0 {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose at least one zone."})
		return nil, nil, false
	}

	if len(targets) > maxBulkZones {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": fmt.Sprintf("That is more than %d zones at once.", maxBulkZones),
		})
		return nil, nil, false
	}

	user := userFrom(r.Context())
	clients := map[int64]*dns.Client{}
	seen := map[string]bool{}
	unique := []edgeTarget{}

	for _, target := range targets {
		if !safeID(target.ZoneID) {
			a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "One of the zones is not a zone."})
			return nil, nil, false
		}

		if target.RuleID != "" && !safeID(target.RuleID) {
			a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "One of the rules is not a rule."})
			return nil, nil, false
		}

		key := target.ZoneID + "/" + target.RuleID
		if seen[key] {
			continue
		}
		seen[key] = true

		if clients[target.Account] == nil {
			account, err := a.Store.FindDNSAccount(r.Context(), target.Account, user.ID)
			if err != nil || !dns.SupportsEdgeRules(account.Provider) {
				a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "One of the zones is not on a Cloudflare account connected here.",
				})
				return nil, nil, false
			}
			clients[target.Account] = dns.New(a.Store, account)
		}

		if target.ZoneName == "" {
			target.ZoneName = target.ZoneID
		}
		unique = append(unique, target)
	}

	return unique, clients, true
}

// eachZone runs work on every target, four at a time, and collects what
// happened in the order the zones were asked for.
func eachZone(targets []edgeTarget, clients map[int64]*dns.Client, work func(*dns.Client, edgeTarget) edgeOutcome) []edgeOutcome {
	outcomes := make([]edgeOutcome, len(targets))
	slots := make(chan struct{}, 4)

	var wait sync.WaitGroup
	for i, target := range targets {
		wait.Add(1)
		go func(i int, target edgeTarget) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			outcome := work(clients[target.Account], target)
			outcome.Zone = target.ZoneName
			outcomes[i] = outcome
		}(i, target)
	}
	wait.Wait()

	return outcomes
}

// bulkAnswer is the summary line, the per-zone list, and the panel's copy of
// each zone touched as it now stands — which the page puts in place of its
// own, so the result shows without the zones being read again. verb is what
// happened where it worked: "Added to", "Saved on", "Deleted from".
func (a *App) bulkAnswer(w http.ResponseWriter, r *http.Request, what, verb string, outcomes []edgeOutcome,
	targets []edgeTarget) {
	done, exists, failed := 0, 0, 0
	for _, outcome := range outcomes {
		switch outcome.Status {
		case "added", "done":
			done++
		case "exists":
			exists++
		default:
			failed++
		}
	}

	parts := []string{fmt.Sprintf("%s %d zone%s", verb, done, plural(done))}
	if exists > 0 {
		parts = append(parts, fmt.Sprintf("%d already had it", exists))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}

	a.Log.Info("cloudflare security changed in bulk", "change", what, "done", done, "existing", exists,
		"failed", failed, "ip", session.ClientIP(r))

	a.writeJSON(w, http.StatusOK, map[string]any{
		"message":  strings.Join(parts, ", ") + ".",
		"failed":   failed,
		"outcomes": outcomes,
		"zones":    a.zoneCopies(r.Context(), targets),
	})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// edgeBulkRuleStore adds one custom or rate limiting rule to many zones.
//
// Whether a zone already has it is answered from the panel's copy when there
// is one, so each zone costs Cloudflare the one request that adds the rule.
func (a *App) edgeBulkRuleStore(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	phase, rule, problem := edgeRuleInput(input)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	targets, clients, ok := a.edgeTargets(w, r, input["targets"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(targets, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		known := a.knownRuleset(ctx, target.ZoneID, phase)
		if known == nil {
			current, err := client.ReadRuleset(target.ZoneID, phase)
			if err != nil {
				return edgeOutcome{Status: "failed", Error: err.Error()}
			}
			known = &current
		}

		// The same rule is the same name and the same expression: a rule
		// renamed or changed since is the operator's, and left beside it.
		for _, have := range known.Rules {
			if strings.EqualFold(have.Description, rule.Description) && have.Expression == rule.Expression {
				return edgeOutcome{Status: "exists"}
			}
		}

		set, err := client.AddEdgeRule(target.ZoneID, phase, known, rule)
		a.keepRuleset(ctx, target.ZoneID, phase, set)
		if err != nil {
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		return edgeOutcome{Status: "added"}
	})

	a.bulkAnswer(w, r, "rule added: "+rule.Description, "Added to", outcomes, targets)
}

// edgeBulkAccessStore adds one IP access rule to many zones.
func (a *App) edgeBulkAccessStore(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
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

	targets, clients, ok := a.edgeTargets(w, r, input["targets"])
	if !ok {
		return
	}

	rule := dns.AccessRule{Mode: input["mode"], Target: input["target"], Value: value, Notes: notes}
	ctx := r.Context()

	outcomes := eachZone(targets, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		// Cloudflare allows one rule per address in a zone; one already
		// there — the zone's own or the account's — is that address handled.
		// Asked of the copy; with none, Cloudflare's own refusal of a
		// duplicate says the same without reading every rule first.
		if zone, ok := a.cachedZone(ctx, target.ZoneID); ok && zone.Errors["access"] == "" {
			for _, have := range zone.Access {
				if have.Target == rule.Target && strings.EqualFold(have.Value, rule.Value) {
					if have.Mode == rule.Mode {
						return edgeOutcome{Status: "exists"}
					}
					return edgeOutcome{Status: "failed", Error: fmt.Sprintf(
						"already has a rule for %s that says %s — change that one instead", value, have.Mode)}
				}
			}
		}

		made, err := client.AddAccessRule(target.ZoneID, rule)
		if err != nil {
			if edgeIsDuplicate(err) {
				return edgeOutcome{Status: "failed", Error: fmt.Sprintf(
					"already has a rule for %s — Refresh to see it, and change that one instead", value)}
			}
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		a.keepAccessRule(ctx, target.ZoneID, made)

		return edgeOutcome{Status: "added"}
	})

	a.bulkAnswer(w, r, "access rule added: "+rule.Mode+" "+value, "Added to", outcomes, targets)
}

// ------------------------------------------------- reading every zone at once

// edgeZoneRules is one zone's security, as the panel keeps a copy of it.
type edgeZoneRules struct {
	Account   int64             `json:"account"`
	ZoneID    string            `json:"zone_id"`
	ZoneName  string            `json:"zone_name"`
	Level     string            `json:"level"`
	Custom    []dns.EdgeRule    `json:"custom"`
	RateLimit []dns.EdgeRule    `json:"ratelimit"`
	Access    []dns.AccessRule  `json:"access"`
	Errors    map[string]string `json:"errors"`

	// The rulesets holding Custom and RateLimit, so a write to one of their
	// rules goes straight to it.
	CustomRuleset    string `json:"custom_ruleset"`
	RateLimitRuleset string `json:"ratelimit_ruleset"`

	// FetchedAt is when Cloudflare was last read for the zone as a whole.
	FetchedAt int64 `json:"fetched_at"`
}

// edgeAll answers the rules of many zones — every site's, usually — so the
// page can show them side by side and change a rule everywhere it is at once.
//
// From the panel's copy: Cloudflare is read only for a zone with none, or for
// every zone asked about when refresh is "1".
func (a *App) edgeAll(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	targets, clients, ok := a.edgeTargets(w, r, input["targets"])
	if !ok {
		return
	}

	refresh := input["refresh"] == "1"
	ctx := r.Context()

	zones := make([]edgeZoneRules, len(targets))
	slots := make(chan struct{}, 4)

	var wait sync.WaitGroup
	for i, target := range targets {
		if !refresh {
			if zone, ok := a.cachedZone(ctx, target.ZoneID); ok {
				zone.Account, zone.ZoneName = target.Account, target.ZoneName
				zones[i] = zone
				continue
			}
		}

		wait.Add(1)
		go func(i int, target edgeTarget) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			zones[i] = a.readZone(ctx, clients[target.Account], target)
		}(i, target)
	}
	wait.Wait()

	a.writeJSON(w, http.StatusOK, map[string]any{"zones": zones})
}

// ------------------------------------------- changing a rule everywhere it is

// copiesOf reads the zones' own copies of one rule, and refuses a target
// without the copy's id.
func (a *App) copiesOf(w http.ResponseWriter, r *http.Request, raw string) ([]edgeTarget, map[int64]*dns.Client, bool) {
	targets, clients, ok := a.edgeTargets(w, r, raw)
	if !ok {
		return nil, nil, false
	}

	for _, target := range targets {
		if target.RuleID == "" {
			a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Say which rule to change in " + target.ZoneName + "."})
			return nil, nil, false
		}
	}

	return targets, clients, true
}

func outcomeOf(err error) edgeOutcome {
	if err != nil {
		return edgeOutcome{Status: "failed", Error: err.Error()}
	}
	return edgeOutcome{Status: "done"}
}

// edgeBulkRuleUpdate rewrites one custom or rate limiting rule in every zone
// that has it. Each copy is updated from what Cloudflare last said about it,
// so what the form does not show survives.
func (a *App) edgeBulkRuleUpdate(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	phase, rule, problem := edgeRuleInput(input)
	if problem != "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": problem})
		return
	}

	copies, clients, ok := a.copiesOf(w, r, input["copies"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(copies, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		set, err := client.ChangeEdgeRule(target.ZoneID, phase, a.knownRuleset(ctx, target.ZoneID, phase), target.RuleID, rule)
		a.keepRuleset(ctx, target.ZoneID, phase, set)
		return outcomeOf(err)
	})

	a.bulkAnswer(w, r, "rule updated: "+rule.Description, "Saved on", outcomes, copies)
}

// edgeBulkRuleDestroy removes one custom or rate limiting rule from every
// zone that has it.
func (a *App) edgeBulkRuleDestroy(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	phase := dns.EdgePhases[input["phase"]]
	if phase == "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose custom rules or rate limiting."})
		return
	}

	copies, clients, ok := a.copiesOf(w, r, input["copies"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(copies, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		set, err := client.RemoveEdgeRule(target.ZoneID, phase, a.knownRuleset(ctx, target.ZoneID, phase), target.RuleID)
		a.keepRuleset(ctx, target.ZoneID, phase, set)

		// Already gone from Cloudflare is what was asked for.
		if edgeIsGone(err) {
			return edgeOutcome{Status: "done"}
		}
		return outcomeOf(err)
	})

	a.bulkAnswer(w, r, "rule deleted: "+input["description"], "Deleted from", outcomes, copies)
}

// edgeBulkAccessUpdate changes what one IP access rule does, and its note,
// in every zone that has it.
func (a *App) edgeBulkAccessUpdate(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !slices.Contains(dns.AccessModes, input["mode"]) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose what the rule does."})
		return
	}

	notes := strings.TrimSpace(input["notes"])
	if len(notes) > 500 {
		notes = notes[:500]
	}

	copies, clients, ok := a.copiesOf(w, r, input["copies"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(copies, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		err := client.UpdateAccessRule(target.ZoneID, target.RuleID, input["mode"], notes)
		switch {
		case err == nil:
			a.changeAccessRule(ctx, target.ZoneID, target.RuleID, input["mode"], notes)
		case edgeIsGone(err):
			a.dropAccessRule(ctx, target.ZoneID, target.RuleID)
		}
		return outcomeOf(err)
	})

	a.bulkAnswer(w, r, "access rule updated: "+input["mode"], "Saved on", outcomes, copies)
}

// edgeBulkAccessDestroy removes one IP access rule from every zone that has it.
func (a *App) edgeBulkAccessDestroy(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	copies, clients, ok := a.copiesOf(w, r, input["copies"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(copies, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		err := client.DeleteAccessRule(target.ZoneID, target.RuleID)
		if err == nil || edgeIsGone(err) {
			a.dropAccessRule(ctx, target.ZoneID, target.RuleID)
			return edgeOutcome{Status: "done"}
		}
		return outcomeOf(err)
	})

	a.bulkAnswer(w, r, "access rule deleted: "+input["value"], "Deleted from", outcomes, copies)
}

// edgeBulkLevel sets the security level of many zones.
func (a *App) edgeBulkLevel(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !slices.Contains(dns.SecurityLevels, input["level"]) {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose a security level."})
		return
	}

	targets, clients, ok := a.edgeTargets(w, r, input["targets"])
	if !ok {
		return
	}

	ctx := r.Context()

	outcomes := eachZone(targets, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		err := client.SetSecurityLevel(target.ZoneID, input["level"])
		if err == nil {
			a.keepLevel(ctx, target.ZoneID, input["level"])
		}
		return outcomeOf(err)
	})

	a.bulkAnswer(w, r, "security level: "+input["level"], "Set on", outcomes, targets)
}
