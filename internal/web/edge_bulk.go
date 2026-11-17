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
}

// edgeOutcome is what happened in one zone.
type edgeOutcome struct {
	Zone   string `json:"zone"`
	Status string `json:"status"` // "added", "exists" or "failed"
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

		if seen[target.ZoneID] {
			continue
		}
		seen[target.ZoneID] = true

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

// bulkAnswer is the summary line and the per-zone list.
func (a *App) bulkAnswer(w http.ResponseWriter, r *http.Request, what string, outcomes []edgeOutcome) {
	added, exists, failed := 0, 0, 0
	for _, outcome := range outcomes {
		switch outcome.Status {
		case "added":
			added++
		case "exists":
			exists++
		default:
			failed++
		}
	}

	parts := []string{fmt.Sprintf("Added to %d zone%s", added, plural(added))}
	if exists > 0 {
		parts = append(parts, fmt.Sprintf("%d already had it", exists))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}

	a.Log.Info("cloudflare security changed in bulk", "change", what, "added", added, "existing", exists,
		"failed", failed, "ip", session.ClientIP(r))

	a.writeJSON(w, http.StatusOK, map[string]any{
		"message":  strings.Join(parts, ", ") + ".",
		"failed":   failed,
		"outcomes": outcomes,
	})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// edgeBulkRuleStore adds one custom or rate limiting rule to many zones.
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

	outcomes := eachZone(targets, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		_, existing, err := client.EdgeRules(target.ZoneID, phase)
		if err != nil {
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		// The same rule is the same name and the same expression: a rule
		// renamed or changed since is the operator's, and left beside it.
		for _, have := range existing {
			if strings.EqualFold(have.Description, rule.Description) && have.Expression == rule.Expression {
				return edgeOutcome{Status: "exists"}
			}
		}

		if err := client.CreateEdgeRule(target.ZoneID, phase, rule); err != nil {
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		return edgeOutcome{Status: "added"}
	})

	a.bulkAnswer(w, r, "rule added: "+rule.Description, outcomes)
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

	outcomes := eachZone(targets, clients, func(client *dns.Client, target edgeTarget) edgeOutcome {
		existing, err := client.AccessRules(target.ZoneID)
		if err != nil {
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		// Cloudflare allows one rule per address in a zone; one already
		// there — the zone's own or the account's — is that address handled.
		for _, have := range existing {
			if have.Target == rule.Target && strings.EqualFold(have.Value, rule.Value) {
				if have.Mode == rule.Mode {
					return edgeOutcome{Status: "exists"}
				}
				return edgeOutcome{Status: "failed", Error: fmt.Sprintf(
					"already has a rule for %s that says %s — change that one instead", value, have.Mode)}
			}
		}

		if err := client.CreateAccessRule(target.ZoneID, rule); err != nil {
			return edgeOutcome{Status: "failed", Error: err.Error()}
		}

		return edgeOutcome{Status: "added"}
	})

	a.bulkAnswer(w, r, "access rule added: "+rule.Mode+" "+value, outcomes)
}
