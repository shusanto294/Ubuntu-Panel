package dns

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Edge rules are the security rules a DNS host enforces in front of the
// server, before a request reaches it: Cloudflare's WAF custom rules, its
// rate limiting rules and its IP access rules. Only Cloudflare proxies the
// traffic it resolves, so only Cloudflare has them — the other hosts the panel
// writes to answer DNS queries and never see a request. Every function here
// says so to a provider that has none, so another one can be added beside
// Cloudflare's without its callers changing.
//
// Nothing is stored in the panel: Cloudflare is the record of which rules
// exist, and a copy here would be wrong the first time somebody used its
// dashboard.

// The two rule phases the panel manages, by Cloudflare's names for them.
const (
	PhaseCustom    = "http_request_firewall_custom"
	PhaseRateLimit = "http_ratelimit"
)

// EdgePhases are the phases by the short key the panel's pages use.
var EdgePhases = map[string]string{
	"custom":    PhaseCustom,
	"ratelimit": PhaseRateLimit,
}

// EdgeActions are what a rule in each phase may do. "log" is Enterprise-only
// at Cloudflare; it is offered, and Cloudflare says so if the plan lacks it.
var EdgeActions = map[string][]string{
	PhaseCustom:    {"block", "managed_challenge", "js_challenge", "challenge", "skip", "log"},
	PhaseRateLimit: {"block", "managed_challenge", "js_challenge", "challenge", "log"},
}

// AccessModes are what an IP access rule does to the address it names.
var AccessModes = []string{"block", "managed_challenge", "js_challenge", "challenge", "whitelist"}

// AccessTargets are what an IP access rule can name.
var AccessTargets = []string{"ip", "ip6", "ip_range", "asn", "country"}

// SecurityLevels are Cloudflare's zone security levels, mildest first.
var SecurityLevels = []string{"essentially_off", "low", "medium", "high", "under_attack"}

// SupportsEdgeRules reports whether a provider enforces security rules in
// front of the server.
func SupportsEdgeRules(provider string) bool {
	return provider == "cloudflare"
}

func (c *Client) edgeOnly() error {
	if !SupportsEdgeRules(c.Account.Provider) {
		return fmt.Errorf("%s only answers DNS queries — it never sees the requests, so it has no security rules", c.label())
	}

	return nil
}

// EdgeRule is one WAF custom or rate limiting rule.
type EdgeRule struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Expression  string `json:"expression"`
	Action      string `json:"action"`
	Enabled     bool   `json:"enabled"`
	LastUpdated string `json:"last_updated"`

	RateLimit *RateLimit `json:"ratelimit,omitempty"`

	// raw is the rule as Cloudflare sent it: an update starts from it, so
	// whatever the panel's form does not show — logging, a custom response,
	// the products a skip rule skips — survives being edited here.
	raw map[string]any
}

// RateLimit is how a rate limiting rule counts.
type RateLimit struct {
	Characteristics   []string `json:"characteristics"`
	Period            int      `json:"period"`
	RequestsPerPeriod int      `json:"requests_per_period"`
	MitigationTimeout int      `json:"mitigation_timeout"`
}

// AccessRule is one IP access rule.
type AccessRule struct {
	ID        string `json:"id"`
	Mode      string `json:"mode"`
	Target    string `json:"target"`
	Value     string `json:"value"`
	Notes     string `json:"notes"`
	CreatedOn string `json:"created_on"`

	// Scope is where the rule was made: "zone" for this zone's own, or
	// "account"/"user" for one that applies to every zone and can only be
	// changed where it was made.
	Scope string `json:"scope"`
}

func zoneURL(zoneID string, rest ...string) string {
	return "https://api.cloudflare.com/client/v4/zones/" + zoneID + "/" + strings.Join(rest, "/")
}

// ---------------------------------------------------------------- rulesets

// EdgeRules are a phase's rules, in the order Cloudflare runs them, and the
// id of the ruleset holding them — "" when the zone has never had one.
func (c *Client) EdgeRules(zoneID, phase string) (string, []EdgeRule, error) {
	if err := c.edgeOnly(); err != nil {
		return "", nil, err
	}

	body, status, err := c.requestStatus("GET", zoneURL(zoneID, "rulesets", "phases", phase, "entrypoint"), nil)
	if status == http.StatusNotFound {
		return "", []EdgeRule{}, nil
	}
	if err != nil {
		return "", nil, err
	}

	result := object(body["result"])
	rules := []EdgeRule{}

	for _, item := range list(result["rules"]) {
		raw := object(item)
		if raw == nil {
			continue
		}

		rule := EdgeRule{
			ID:          str(raw, "id", ""),
			Description: str(raw, "description", ""),
			Expression:  str(raw, "expression", ""),
			Action:      str(raw, "action", ""),
			Enabled:     boolean(raw, "enabled", true),
			LastUpdated: str(raw, "last_updated", ""),
			raw:         raw,
		}

		if limit := object(raw["ratelimit"]); limit != nil {
			rule.RateLimit = &RateLimit{
				Characteristics:   strings_(limit["characteristics"]),
				Period:            number(limit, "period", 0),
				RequestsPerPeriod: number(limit, "requests_per_period", 0),
				MitigationTimeout: number(limit, "mitigation_timeout", 0),
			}
		}

		rules = append(rules, rule)
	}

	return str(result, "id", ""), rules, nil
}

func strings_(value any) []string {
	out := []string{}
	for _, item := range list(value) {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// edgeRuleBody is what Cloudflare is sent for a rule: the one it already has,
// if any, with the panel's fields written over it.
func edgeRuleBody(existing map[string]any, rule EdgeRule) map[string]any {
	body := map[string]any{}
	for key, value := range existing {
		switch key {
		case "id", "version", "last_updated", "categories":
			// Cloudflare's own bookkeeping, not something a write sets.
		default:
			body[key] = value
		}
	}

	// Parameters belong to an action: a block rule's custom response means
	// nothing to a challenge.
	if str(existing, "action", "") != rule.Action {
		delete(body, "action_parameters")
	}

	body["description"] = rule.Description
	body["expression"] = rule.Expression
	body["action"] = rule.Action
	body["enabled"] = rule.Enabled

	// A skip rule has to say what it skips. Made here, it skips the rest of
	// the custom rules — the "allow this, whatever comes after" rule.
	if rule.Action == "skip" && body["action_parameters"] == nil {
		body["action_parameters"] = map[string]any{"ruleset": "current"}
	}

	if rule.RateLimit != nil {
		limit := map[string]any{}
		for key, value := range object(existing["ratelimit"]) {
			limit[key] = value
		}
		if len(rule.RateLimit.Characteristics) > 0 || limit["characteristics"] == nil {
			limit["characteristics"] = rule.RateLimit.Characteristics
		}
		limit["period"] = rule.RateLimit.Period
		limit["requests_per_period"] = rule.RateLimit.RequestsPerPeriod
		limit["mitigation_timeout"] = rule.RateLimit.MitigationTimeout
		body["ratelimit"] = limit
	}

	return body
}

// CreateEdgeRule adds a rule at the end of a phase, making the phase's
// ruleset first when the zone has never had one.
func (c *Client) CreateEdgeRule(zoneID, phase string, rule EdgeRule) error {
	rulesetID, _, err := c.EdgeRules(zoneID, phase)
	if err != nil {
		return err
	}

	body := edgeRuleBody(nil, rule)

	if rulesetID == "" {
		_, err = c.request("PUT", zoneURL(zoneID, "rulesets", "phases", phase, "entrypoint"),
			map[string]any{"rules": []any{body}})
		return err
	}

	_, err = c.request("POST", zoneURL(zoneID, "rulesets", rulesetID, "rules"), body)
	return err
}

// findEdgeRule is a rule and its ruleset's id.
func (c *Client) findEdgeRule(zoneID, phase, ruleID string) (string, []EdgeRule, int, error) {
	rulesetID, rules, err := c.EdgeRules(zoneID, phase)
	if err != nil {
		return "", nil, -1, err
	}

	for i, rule := range rules {
		if rule.ID == ruleID {
			return rulesetID, rules, i, nil
		}
	}

	return "", nil, -1, errors.New("Cloudflare has no such rule any more — it may have been changed in its dashboard")
}

// UpdateEdgeRule rewrites a rule in place, keeping whatever about it the
// panel does not set.
func (c *Client) UpdateEdgeRule(zoneID, phase, ruleID string, rule EdgeRule) error {
	rulesetID, rules, at, err := c.findEdgeRule(zoneID, phase, ruleID)
	if err != nil {
		return err
	}

	_, err = c.request("PATCH", zoneURL(zoneID, "rulesets", rulesetID, "rules", ruleID),
		edgeRuleBody(rules[at].raw, rule))

	return err
}

// MoveEdgeRule puts a rule at a position in its phase, 1 being first:
// Cloudflare runs custom rules in order, and the first that blocks or skips
// is the last that runs.
func (c *Client) MoveEdgeRule(zoneID, phase, ruleID string, position int) error {
	rulesetID, rules, at, err := c.findEdgeRule(zoneID, phase, ruleID)
	if err != nil {
		return err
	}

	position = max(1, min(position, len(rules)))
	if position == at+1 {
		return nil
	}

	body := edgeRuleBody(rules[at].raw, rules[at])
	body["position"] = map[string]any{"index": position}

	_, err = c.request("PATCH", zoneURL(zoneID, "rulesets", rulesetID, "rules", ruleID), body)
	return err
}

// DeleteEdgeRule removes a rule.
func (c *Client) DeleteEdgeRule(zoneID, phase, ruleID string) error {
	rulesetID, _, _, err := c.findEdgeRule(zoneID, phase, ruleID)
	if err != nil {
		return err
	}

	_, err = c.request("DELETE", zoneURL(zoneID, "rulesets", rulesetID, "rules", ruleID), nil)
	return err
}

// ------------------------------------------------------------- access rules

// AccessRules are the IP access rules that apply to a zone, its own and the
// account's.
func (c *Client) AccessRules(zoneID string) ([]AccessRule, error) {
	if err := c.edgeOnly(); err != nil {
		return nil, err
	}

	rules := []AccessRule{}

	for page := 1; page <= 20; page++ {
		body, err := c.request("GET", zoneURL(zoneID, "firewall", "access_rules", "rules")+
			"?per_page=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, err
		}

		for _, item := range list(body["result"]) {
			raw := object(item)
			if raw == nil {
				continue
			}

			configuration := object(raw["configuration"])
			rules = append(rules, AccessRule{
				ID:        str(raw, "id", ""),
				Mode:      str(raw, "mode", ""),
				Target:    str(configuration, "target", ""),
				Value:     str(configuration, "value", ""),
				Notes:     str(raw, "notes", ""),
				CreatedOn: str(raw, "created_on", ""),
				Scope:     str(object(raw["scope"]), "type", "zone"),
			})
		}

		if page >= number(object(body["result_info"]), "total_pages", 1) {
			break
		}
	}

	return rules, nil
}

// CreateAccessRule adds an IP access rule to a zone.
func (c *Client) CreateAccessRule(zoneID string, rule AccessRule) error {
	if err := c.edgeOnly(); err != nil {
		return err
	}

	_, err := c.request("POST", zoneURL(zoneID, "firewall", "access_rules", "rules"), map[string]any{
		"mode":          rule.Mode,
		"configuration": map[string]any{"target": rule.Target, "value": rule.Value},
		"notes":         rule.Notes,
	})

	return err
}

// UpdateAccessRule changes what a rule does and its note; what it names is
// fixed once made.
func (c *Client) UpdateAccessRule(zoneID, ruleID, mode, notes string) error {
	if err := c.edgeOnly(); err != nil {
		return err
	}

	_, err := c.request("PATCH", zoneURL(zoneID, "firewall", "access_rules", "rules", ruleID),
		map[string]any{"mode": mode, "notes": notes})

	return err
}

// DeleteAccessRule removes an IP access rule from a zone.
func (c *Client) DeleteAccessRule(zoneID, ruleID string) error {
	if err := c.edgeOnly(); err != nil {
		return err
	}

	_, err := c.request("DELETE", zoneURL(zoneID, "firewall", "access_rules", "rules", ruleID), nil)
	return err
}

// --------------------------------------------------------------- checking

var (
	asnPattern     = regexp.MustCompile(`^(?i:AS)?\d{1,10}$`)
	countryPattern = regexp.MustCompile(`^([A-Z]{2}|T1)$`)
)

// NormaliseAccessTarget checks what an access rule names against the kind of
// thing it says it is, and puts it the way Cloudflare wants it: an ASN as
// AS13335, a country as two capitals.
func NormaliseAccessTarget(target, value string) (string, error) {
	value = strings.TrimSpace(value)

	switch target {
	case "ip":
		if ip := net.ParseIP(value); ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
		return "", errors.New("that is not an IPv4 address")
	case "ip6":
		if ip := net.ParseIP(value); ip != nil && ip.To4() == nil {
			return ip.String(), nil
		}
		return "", errors.New("that is not an IPv6 address")
	case "ip_range":
		if _, network, err := net.ParseCIDR(value); err == nil {
			ones, bits := network.Mask.Size()
			if (bits == 32 && ones != 16 && ones != 24) || (bits == 128 && !slices.Contains([]int{32, 48, 64}, ones)) {
				return "", errors.New("Cloudflare takes IPv4 ranges as /16 or /24, and IPv6 as /32, /48 or /64")
			}
			return network.String(), nil
		}
		return "", errors.New("write a range as an address and a prefix, like 203.0.113.0/24")
	case "asn":
		if asnPattern.MatchString(value) {
			return "AS" + strings.TrimPrefix(strings.ToUpper(value), "AS"), nil
		}
		return "", errors.New("an ASN is a number, like AS13335 or 13335")
	case "country":
		value = strings.ToUpper(value)
		if countryPattern.MatchString(value) {
			return value, nil
		}
		return "", errors.New("a country is its two-letter code, like US or DE (T1 is Tor)")
	}

	return "", errors.New("choose what the rule matches: an address, a range, an ASN or a country")
}
