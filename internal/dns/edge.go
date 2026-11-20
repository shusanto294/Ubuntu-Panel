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
// Cloudflare is the record of which rules exist. The panel keeps a copy of
// what it last read (internal/web/edge_cache.go) so the page opens without
// asking, and every write here answers what Cloudflare holds afterwards, so
// the copy is kept true without reading it all again.

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

	// Raw is the rule as Cloudflare sent it: an update starts from it, so
	// whatever the panel's form does not show — logging, a custom response,
	// the products a skip rule skips — survives being edited here. Kept with
	// the panel's cached copy, so an edit needs no read first.
	Raw map[string]any `json:"raw,omitempty"`
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

// Ruleset is one phase's rules, in the order Cloudflare runs them, and the
// id of the ruleset holding them — "" when the zone has never had one.
type Ruleset struct {
	ID    string     `json:"id"`
	Rules []EdgeRule `json:"rules"`
}

// EdgeRules are a phase's rules, in the order Cloudflare runs them, and the
// id of the ruleset holding them — "" when the zone has never had one.
func (c *Client) EdgeRules(zoneID, phase string) (string, []EdgeRule, error) {
	set, err := c.ReadRuleset(zoneID, phase)

	return set.ID, set.Rules, err
}

// ReadRuleset is EdgeRules as one value.
func (c *Client) ReadRuleset(zoneID, phase string) (Ruleset, error) {
	if err := c.edgeOnly(); err != nil {
		return Ruleset{}, err
	}

	body, status, err := c.requestStatus("GET", zoneURL(zoneID, "rulesets", "phases", phase, "entrypoint"), nil)
	if status == http.StatusNotFound {
		return Ruleset{Rules: []EdgeRule{}}, nil
	}
	if err != nil {
		return Ruleset{}, err
	}

	return parseRuleset(object(body["result"])), nil
}

// parseRuleset reads a ruleset as Cloudflare sends it — from a read, and
// from every write to one of its rules, which answers with the whole set.
func parseRuleset(result map[string]any) Ruleset {
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
			Raw:         raw,
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

	return Ruleset{ID: str(result, "id", ""), Rules: rules}
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
	_, err := c.AddEdgeRule(zoneID, phase, nil, rule)
	return err
}

// The writes below take the ruleset as the panel last saw it, so a change to
// a zone costs Cloudflare one request rather than a read and then a write,
// and answer the ruleset as Cloudflare holds it afterwards. known may be nil,
// and is not trusted where being wrong would cost something: a copy that no
// longer matches Cloudflare is read again, once.

// AddEdgeRule adds a rule at the end of a phase.
//
// A known ruleset with no id is never believed. Making the ruleset is a PUT
// of its whole rule list, and a zone that has gained one in Cloudflare's
// dashboard since would lose every rule in it.
func (c *Client) AddEdgeRule(zoneID, phase string, known *Ruleset, rule EdgeRule) (Ruleset, error) {
	if err := c.edgeOnly(); err != nil {
		return Ruleset{}, err
	}

	body := edgeRuleBody(nil, rule)

	if known != nil && known.ID != "" {
		answer, status, err := c.requestStatus("POST", zoneURL(zoneID, "rulesets", known.ID, "rules"), body)
		if err == nil {
			return parseRuleset(object(answer["result"])), nil
		}
		if status != http.StatusNotFound {
			return Ruleset{}, err
		}
	}

	current, err := c.ReadRuleset(zoneID, phase)
	if err != nil {
		return Ruleset{}, err
	}

	if current.ID == "" {
		answer, err := c.request("PUT", zoneURL(zoneID, "rulesets", "phases", phase, "entrypoint"),
			map[string]any{"rules": []any{body}})
		if err != nil {
			return Ruleset{}, err
		}
		return parseRuleset(object(answer["result"])), nil
	}

	answer, err := c.request("POST", zoneURL(zoneID, "rulesets", current.ID, "rules"), body)
	if err != nil {
		return Ruleset{}, err
	}

	return parseRuleset(object(answer["result"])), nil
}

// ruleIn is where a rule is in a ruleset, or -1.
func ruleIn(set *Ruleset, ruleID string) int {
	if set == nil || set.ID == "" {
		return -1
	}

	for i, rule := range set.Rules {
		if rule.ID == ruleID && rule.Raw != nil {
			return i
		}
	}

	return -1
}

// onRule runs one write against a rule: from the known ruleset when it has
// the rule, and from a fresh read when it does not or Cloudflare says the
// known one is out of date.
func (c *Client) onRule(zoneID, phase string, known *Ruleset, ruleID string,
	write func(set Ruleset, at int) (map[string]any, int, error)) (Ruleset, error) {
	if err := c.edgeOnly(); err != nil {
		return Ruleset{}, err
	}

	if at := ruleIn(known, ruleID); at >= 0 {
		answer, status, err := write(*known, at)
		if err == nil {
			return parseRuleset(object(answer["result"])), nil
		}
		if status != http.StatusNotFound && status != http.StatusConflict {
			return Ruleset{}, err
		}
	}

	current, err := c.ReadRuleset(zoneID, phase)
	if err != nil {
		return Ruleset{}, err
	}

	at := ruleIn(&current, ruleID)
	if at < 0 {
		return current, errors.New("Cloudflare has no such rule any more — it may have been changed in its dashboard")
	}

	answer, _, err := write(current, at)
	if err != nil {
		return Ruleset{}, err
	}

	return parseRuleset(object(answer["result"])), nil
}

// UpdateEdgeRule rewrites a rule in place, keeping whatever about it the
// panel does not set.
func (c *Client) UpdateEdgeRule(zoneID, phase, ruleID string, rule EdgeRule) error {
	_, err := c.ChangeEdgeRule(zoneID, phase, nil, ruleID, rule)
	return err
}

// ChangeEdgeRule is UpdateEdgeRule from a known ruleset.
func (c *Client) ChangeEdgeRule(zoneID, phase string, known *Ruleset, ruleID string, rule EdgeRule) (Ruleset, error) {
	return c.onRule(zoneID, phase, known, ruleID, func(set Ruleset, at int) (map[string]any, int, error) {
		return c.requestStatus("PATCH", zoneURL(zoneID, "rulesets", set.ID, "rules", ruleID),
			edgeRuleBody(set.Rules[at].Raw, rule))
	})
}

// MoveEdgeRule puts a rule at a position in its phase, 1 being first:
// Cloudflare runs custom rules in order, and the first that blocks or skips
// is the last that runs.
func (c *Client) MoveEdgeRule(zoneID, phase, ruleID string, position int) error {
	_, err := c.PlaceEdgeRule(zoneID, phase, nil, ruleID, position)
	return err
}

// PlaceEdgeRule is MoveEdgeRule from a known ruleset.
func (c *Client) PlaceEdgeRule(zoneID, phase string, known *Ruleset, ruleID string, position int) (Ruleset, error) {
	return c.onRule(zoneID, phase, known, ruleID, func(set Ruleset, at int) (map[string]any, int, error) {
		position = max(1, min(position, len(set.Rules)))

		body := edgeRuleBody(set.Rules[at].Raw, set.Rules[at])
		body["position"] = map[string]any{"index": position}

		return c.requestStatus("PATCH", zoneURL(zoneID, "rulesets", set.ID, "rules", ruleID), body)
	})
}

// DeleteEdgeRule removes a rule.
func (c *Client) DeleteEdgeRule(zoneID, phase, ruleID string) error {
	_, err := c.RemoveEdgeRule(zoneID, phase, nil, ruleID)
	return err
}

// RemoveEdgeRule is DeleteEdgeRule from a known ruleset.
func (c *Client) RemoveEdgeRule(zoneID, phase string, known *Ruleset, ruleID string) (Ruleset, error) {
	return c.onRule(zoneID, phase, known, ruleID, func(set Ruleset, _ int) (map[string]any, int, error) {
		return c.requestStatus("DELETE", zoneURL(zoneID, "rulesets", set.ID, "rules", ruleID), nil)
	})
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

			rules = append(rules, parseAccessRule(raw))
		}

		if page >= number(object(body["result_info"]), "total_pages", 1) {
			break
		}
	}

	return rules, nil
}

func parseAccessRule(raw map[string]any) AccessRule {
	configuration := object(raw["configuration"])

	return AccessRule{
		ID:        str(raw, "id", ""),
		Mode:      str(raw, "mode", ""),
		Target:    str(configuration, "target", ""),
		Value:     str(configuration, "value", ""),
		Notes:     str(raw, "notes", ""),
		CreatedOn: str(raw, "created_on", ""),
		Scope:     str(object(raw["scope"]), "type", "zone"),
	}
}

// CreateAccessRule adds an IP access rule to a zone.
func (c *Client) CreateAccessRule(zoneID string, rule AccessRule) error {
	_, err := c.AddAccessRule(zoneID, rule)

	return err
}

// CreateAccessRuleID adds an IP access rule to a zone and answers the id
// Cloudflare gave it, for a rule the panel will take away again itself.
func (c *Client) CreateAccessRuleID(zoneID string, rule AccessRule) (string, error) {
	made, err := c.AddAccessRule(zoneID, rule)

	return made.ID, err
}

// AddAccessRule adds an IP access rule to a zone and answers it as Cloudflare
// made it, id and all — enough to keep a copy without reading the list again.
func (c *Client) AddAccessRule(zoneID string, rule AccessRule) (AccessRule, error) {
	if err := c.edgeOnly(); err != nil {
		return AccessRule{}, err
	}

	body, err := c.request("POST", zoneURL(zoneID, "firewall", "access_rules", "rules"), map[string]any{
		"mode":          rule.Mode,
		"configuration": map[string]any{"target": rule.Target, "value": rule.Value},
		"notes":         rule.Notes,
	})
	if err != nil {
		return AccessRule{}, err
	}

	made := parseAccessRule(object(body["result"]))
	if made.Target == "" {
		made.Target, made.Value = rule.Target, rule.Value
	}
	if made.Mode == "" {
		made.Mode, made.Notes = rule.Mode, rule.Notes
	}

	return made, nil
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
