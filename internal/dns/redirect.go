package dns

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Redirects are Cloudflare's Single Redirects: rules in the zone's dynamic
// redirect phase that answer a matching request with a 301/302/307/308 at
// Cloudflare's edge, before anything reaches a server. That is what lets the
// panel redirect a hostname with no site behind it — a mail domain's bare
// apex, an old brand's domain — as long as Cloudflare holds its zone.
//
// Cloudflare only sees a request whose hostname it proxies, so a redirect is
// only half of it: EnsureProxied makes the hostname resolve through
// Cloudflare, adding a placeholder record where there is none.
//
// The panel writes one shape of rule and reads that shape back for its form.
// A rule made in Cloudflare's dashboard is listed with its own expression and
// can be switched off or deleted here, but not edited.

// PhaseRedirect is Cloudflare's name for the Single Redirects phase.
const PhaseRedirect = "http_request_dynamic_redirect"

// RedirectCodes are the status codes Cloudflare takes for a redirect.
var RedirectCodes = []int{301, 302, 307, 308}

// PlaceholderAddress is what a redirect-only hostname points at: an address
// reserved for documentation that nothing answers on. Cloudflare answers for
// it, because the record is proxied; the address itself is never reached.
const PlaceholderAddress = "192.0.2.1"

// Path modes: every path on the hostname, one exact path, or everything under
// a path.
const (
	PathAll    = "all"
	PathExact  = "exact"
	PathPrefix = "prefix"
)

// Redirect is one redirect in the panel's shape.
type Redirect struct {
	ID          string   `json:"id"`
	Hosts       []string `json:"hosts"`
	PathMode    string   `json:"path_mode"`
	Path        string   `json:"path"`
	Target      string   `json:"target"`
	StatusCode  int      `json:"status_code"`
	KeepPath    bool     `json:"keep_path"`
	KeepQuery   bool     `json:"keep_query"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description"`
	Expression  string   `json:"expression"`
	LastUpdated string   `json:"last_updated"`

	// Managed is a rule in the shape the panel writes, which its form can
	// edit. Anything else was made in Cloudflare's dashboard.
	Managed bool `json:"managed"`
}

var (
	hostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)
)

// ValidHost is a hostname a redirect can match: lower case, no port, no
// wildcard.
func ValidHost(host string) bool {
	return len(host) <= 253 && hostPattern.MatchString(host)
}

// CheckRedirect makes a redirect the panel is about to write consistent, and
// says what is wrong with it when it cannot be.
func CheckRedirect(r *Redirect) error {
	if len(r.Hosts) == 0 {
		return errors.New("Say which hostname redirects.")
	}
	for i, host := range r.Hosts {
		host = strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
		if !ValidHost(host) {
			return fmt.Errorf("%q is not a hostname.", host)
		}
		r.Hosts[i] = host
	}

	switch r.PathMode {
	case "", PathAll:
		r.PathMode, r.Path = PathAll, ""
	case PathExact, PathPrefix:
		r.Path = strings.TrimSpace(r.Path)
		if r.PathMode == PathPrefix {
			r.Path = strings.TrimRight(strings.TrimSuffix(r.Path, "*"), "/") + "/"
		}
		if r.Path == "/" && r.PathMode == PathPrefix {
			r.PathMode, r.Path = PathAll, ""
		} else if !pathPattern.MatchString(r.Path) || len(r.Path) > 1024 {
			return errors.New("A path starts with / and has no spaces or quotes in it.")
		}
	default:
		return errors.New("Choose which paths redirect.")
	}

	target, err := url.Parse(strings.TrimSpace(r.Target))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		strings.ContainsAny(r.Target, "\"\\ \t\r\n") || len(r.Target) > 2048 {
		return errors.New("The destination is a full address, like https://example.com/page.")
	}
	r.Target = strings.TrimSpace(r.Target)

	if !slices.Contains(RedirectCodes, r.StatusCode) {
		return errors.New("Choose a redirect type.")
	}

	// One path has nothing left over to carry across.
	if r.PathMode == PathExact {
		r.KeepPath = false
	}

	return nil
}

// quote is a string in the rules language. CheckRedirect has already refused
// the quote and backslash that would need escaping; this is belt and braces.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// RedirectExpression is which requests a redirect matches.
func RedirectExpression(r Redirect) string {
	var host string
	if len(r.Hosts) == 1 {
		host = "http.host eq " + quote(r.Hosts[0])
	} else {
		quoted := make([]string, len(r.Hosts))
		for i, h := range r.Hosts {
			quoted[i] = quote(h)
		}
		host = "http.host in {" + strings.Join(quoted, " ") + "}"
	}

	switch r.PathMode {
	case PathExact:
		return "(" + host + " and http.request.uri.path eq " + quote(r.Path) + ")"
	case PathPrefix:
		return "(" + host + " and starts_with(http.request.uri.path, " + quote(r.Path) + "))"
	}

	return "(" + host + ")"
}

// RedirectRule is the rule Cloudflare is sent for a redirect.
func RedirectRule(r Redirect) EdgeRule {
	description := r.Description
	if description == "" {
		from := r.Hosts[0] + r.Path
		if r.PathMode == PathPrefix {
			from += "*"
		}
		description = "Redirect " + from + " → " + r.Target
		if len(description) > 500 {
			description = description[:500]
		}
	}

	target := map[string]any{"value": r.Target}
	if r.KeepPath {
		target = map[string]any{
			"expression": "concat(" + quote(strings.TrimRight(r.Target, "/")) + ", http.request.uri.path)",
		}
	}

	return EdgeRule{
		Description: description,
		Expression:  RedirectExpression(r),
		Action:      "redirect",
		Enabled:     r.Enabled,
		ActionParameters: map[string]any{
			"from_value": map[string]any{
				"target_url":            target,
				"status_code":           r.StatusCode,
				"preserve_query_string": r.KeepQuery,
			},
		},
	}
}

var (
	oneHost      = regexp.MustCompile(`^http\.host eq "([^"\\]+)"$`)
	manyHosts    = regexp.MustCompile(`^http\.host in \{((?:"[^"\\]+" ?)+)\}$`)
	exactPath    = regexp.MustCompile(`^(.+) and http\.request\.uri\.path eq "([^"\\]+)"$`)
	prefixPath   = regexp.MustCompile(`^(.+) and starts_with\(http\.request\.uri\.path, "([^"\\]+)"\)$`)
	keepPathExpr = regexp.MustCompile(`^concat\("([^"\\]+)", http\.request\.uri\.path\)$`)
	quotedHost   = regexp.MustCompile(`"([^"\\]+)"`)
)

// ParseRedirect reads a rule from the redirect phase back into the panel's
// shape. A rule the panel did not write keeps its expression and whatever
// of its target and code could be read, and is not Managed.
func ParseRedirect(rule EdgeRule) Redirect {
	r := Redirect{
		ID:          rule.ID,
		Enabled:     rule.Enabled,
		Description: rule.Description,
		Expression:  rule.Expression,
		LastUpdated: rule.LastUpdated,
		Hosts:       []string{},
	}

	from := object(object(rule.Raw["action_parameters"])["from_value"])
	r.StatusCode = number(from, "status_code", 301)
	r.KeepQuery = boolean(from, "preserve_query_string", false)

	target := object(from["target_url"])
	r.Target = str(target, "value", "")
	targetOK := r.Target != ""
	if expression := str(target, "expression", ""); expression != "" {
		if match := keepPathExpr.FindStringSubmatch(expression); match != nil {
			r.Target, r.KeepPath, targetOK = match[1], true, true
		} else {
			r.Target = expression
		}
	}

	if rule.Action != "redirect" || !targetOK {
		return r
	}

	expression := strings.TrimSpace(rule.Expression)
	if !strings.HasPrefix(expression, "(") || !strings.HasSuffix(expression, ")") {
		return r
	}
	inner := expression[1 : len(expression)-1]

	r.PathMode = PathAll
	if match := exactPath.FindStringSubmatch(inner); match != nil {
		inner, r.PathMode, r.Path = match[1], PathExact, match[2]
	} else if match := prefixPath.FindStringSubmatch(inner); match != nil {
		inner, r.PathMode, r.Path = match[1], PathPrefix, match[2]
	}

	if match := oneHost.FindStringSubmatch(inner); match != nil {
		r.Hosts = []string{match[1]}
	} else if match := manyHosts.FindStringSubmatch(inner); match != nil {
		for _, host := range quotedHost.FindAllStringSubmatch(match[1], -1) {
			r.Hosts = append(r.Hosts, host[1])
		}
	} else {
		r.PathMode, r.Path = "", ""
		return r
	}

	r.Managed = true

	return r
}

// ParseRedirects is a ruleset's rules as redirects, in Cloudflare's order.
func ParseRedirects(set Ruleset) []Redirect {
	out := make([]Redirect, 0, len(set.Rules))
	for _, rule := range set.Rules {
		out = append(out, ParseRedirect(rule))
	}
	return out
}

// ------------------------------------------------------------ DNS for it

// NotProxiedError is a hostname that resolves without going through
// Cloudflare, so Cloudflare never sees the request it is meant to redirect.
// The panel does not proxy it unasked: whatever it points at may be something
// that breaks behind a proxy.
type NotProxiedError struct {
	Host    string
	Content string
	Mail    bool // mail for the zone is delivered to this hostname
}

func (e *NotProxiedError) Error() string {
	if e.Mail {
		return fmt.Sprintf("%s receives this domain's mail, and its record points straight at %s. Cloudflare can only redirect it by proxying it, and a proxied hostname cannot receive mail — redirect a different hostname, or move the mail first.", e.Host, e.Content)
	}
	return fmt.Sprintf("%s points straight at %s without going through Cloudflare, so Cloudflare never sees its visitors. Tick “Proxy it through Cloudflare” to switch the proxy on — the redirect then answers instead of whatever is at that address.", e.Host, e.Content)
}

// EnsureProxied makes every hostname resolve through Cloudflare. A hostname
// with no address record gets a proxied placeholder; one whose records are
// not proxied is switched over only when proxy is true, and never when the
// zone's mail goes to it. Answers one line for each change made.
func (c *Client) EnsureProxied(zoneID, zoneName string, hosts []string, proxy bool) ([]string, error) {
	records, err := c.Records(zoneID, zoneName)
	if err != nil {
		return nil, err
	}

	done := []string{}

	for _, host := range hosts {
		addresses := []Record{}
		mail := false

		for _, record := range records {
			name := strings.ToLower(strings.TrimRight(record.Name, "."))
			switch {
			case record.Type == "MX" && strings.EqualFold(strings.TrimRight(record.Content, "."), host):
				mail = true
			case name == host && (record.Type == "A" || record.Type == "AAAA" || record.Type == "CNAME"):
				addresses = append(addresses, record)
			}
		}

		if len(addresses) == 0 {
			_, err := c.CreateRecord(zoneID, zoneName, Record{
				Type: "A", Name: host, Content: PlaceholderAddress, Priority: -1, Proxied: true,
			})
			if err != nil {
				return done, fmt.Errorf("could not add a DNS record for %s: %w", host, err)
			}
			done = append(done, "Added a proxied DNS record for "+host+" so Cloudflare answers for it.")
			continue
		}

		for _, record := range addresses {
			if record.Proxied {
				continue
			}
			if mail || !proxy {
				return done, &NotProxiedError{Host: host, Content: record.Content, Mail: mail}
			}

			record.Proxied = true
			if err := c.UpdateRecord(zoneID, zoneName, record.ID, record); err != nil {
				return done, fmt.Errorf("could not proxy %s through Cloudflare: %w", host, err)
			}
			done = append(done, "Switched the Cloudflare proxy on for "+host+".")
		}
	}

	return done, nil
}
