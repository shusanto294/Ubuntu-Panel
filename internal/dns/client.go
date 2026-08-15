package dns

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// Client talks to one credential's provider.
type Client struct {
	Account *store.DNSAccount

	http *http.Client
}

// New prepares a client. The timeout is per request and deliberately short:
// a deployment waiting on a DNS API that is not answering should fail the step
// and say so, not hold the queue worker for minutes.
func New(account *store.DNSAccount) *Client {
	return &Client{
		Account: account,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// request makes one call with the provider's authentication applied.
//
// Hetzner wants its token in `Auth-API-Token`, the token-bearer providers want
// `Authorization: Bearer`, and Porkbun puts both credentials in the JSON body
// instead of in a header — which is why body is passed through rather than
// built here.
func (c *Client) request(method, url string, body any) (map[string]any, error) {
	var payload io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Ubuntu-Panel")

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	switch c.Account.Provider {
	case "hetzner":
		request.Header.Set("Auth-API-Token", c.Account.APIToken)
	case "porkbun":
		// Credentials travel in the body; see porkbunBody.
	default:
		request.Header.Set("Authorization", "Bearer "+c.Account.APIToken)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", c.label(), err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", c.label(), err)
	}

	var parsed map[string]any

	// A body that is not an object (Linode returns one, Vultr sometimes
	// returns nothing at all on a delete) is not an error by itself; the
	// status code decides.
	json.Unmarshal(raw, &parsed)

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", c.label(), errorMessage(parsed, raw, response.StatusCode))
	}

	// Cloudflare and Porkbun both report failure inside a 200 envelope, so the
	// status code alone is not enough to know the call worked.
	if c.Account.Provider == "cloudflare" && parsed != nil {
		if success, ok := parsed["success"].(bool); ok && !success {
			return nil, fmt.Errorf("Cloudflare: %s", errorMessage(parsed, raw, 200))
		}
	}

	if c.Account.Provider == "porkbun" && parsed != nil {
		if status, ok := parsed["status"].(string); ok && !strings.EqualFold(status, "SUCCESS") {
			return nil, fmt.Errorf("Porkbun: %s", errorMessage(parsed, raw, 200))
		}
	}

	return parsed, nil
}

func (c *Client) label() string { return ProviderLabel(c.Account.Provider) }

// porkbunBody puts both keys into every request body, which is how Porkbun
// authenticates.
func (c *Client) porkbunBody(extra map[string]any) map[string]any {
	body := map[string]any{
		"apikey":       c.Account.APIToken,
		"secretapikey": c.Account.APISecret,
	}

	for key, value := range extra {
		body[key] = value
	}

	return body
}

// errorMessage digs the provider's own words out of a failure, so the operator
// reads "The token is not valid for this zone" rather than "HTTP 403".
func errorMessage(parsed map[string]any, raw []byte, status int) string {
	if parsed != nil {
		// Cloudflare and Porkbun: {"errors":[{"message":"..."}]}
		if errors, ok := parsed["errors"].([]any); ok && len(errors) > 0 {
			var messages []string

			for _, entry := range errors {
				switch value := entry.(type) {
				case map[string]any:
					if message, ok := value["message"].(string); ok {
						messages = append(messages, message)
					} else if reason, ok := value["reason"].(string); ok {
						messages = append(messages, reason)
					}
				case string:
					messages = append(messages, value)
				}
			}

			if len(messages) > 0 {
				return strings.Join(messages, "; ")
			}
		}

		for _, key := range []string{"message", "error", "detail"} {
			if message, ok := parsed[key].(string); ok && message != "" {
				return message
			}
		}
	}

	body := strings.TrimSpace(string(raw))
	if len(body) > 300 {
		body = body[:300]
	}

	if body == "" {
		return fmt.Sprintf("HTTP %d", status)
	}

	return fmt.Sprintf("HTTP %d — %s", status, body)
}

// ------------------------------------------------------------ JSON helpers

func object(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}

	return nil
}

func list(value any) []any {
	if listed, ok := value.([]any); ok {
		return listed
	}

	return nil
}

func str(from map[string]any, key, fallback string) string {
	if from == nil {
		return fallback
	}

	switch value := from[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	}

	return fallback
}

func number(from map[string]any, key string, fallback int) int {
	if from == nil {
		return fallback
	}

	switch value := from[key].(type) {
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}

	return fallback
}

func boolean(from map[string]any, key string, fallback bool) bool {
	if from == nil {
		return fallback
	}

	if value, ok := from[key].(bool); ok {
		return value
	}

	return fallback
}

// identifier reads an id that some providers send as a number and others as a
// string.
func identifier(from map[string]any, key, fallback string) string {
	return str(from, key, fallback)
}

// ---------------------------------------------------------------- verifying

// Verify checks the credential and returns something worth showing the user.
func (c *Client) Verify() (string, error) {
	switch c.Account.Provider {
	case "cloudflare":
		body, err := c.request("GET", "https://api.cloudflare.com/client/v4/user/tokens/verify", nil)
		if err != nil {
			return "", err
		}

		status := str(object(body["result"]), "status", "unknown")
		if status != "active" {
			return "", fmt.Errorf("The token is %s, not active.", status)
		}

		return "token is active", nil

	case "digitalocean":
		body, err := c.request("GET", "https://api.digitalocean.com/v2/account", nil)
		if err != nil {
			return "", err
		}

		if email := str(object(body["account"]), "email", ""); email != "" {
			return "token belongs to " + email, nil
		}

		return "token accepted", nil

	case "linode":
		body, err := c.request("GET", "https://api.linode.com/v4/profile", nil)
		if err != nil {
			return "", err
		}

		if username := str(body, "username", ""); username != "" {
			return "token belongs to " + username, nil
		}

		return "token accepted", nil

	case "vultr":
		if _, err := c.request("GET", "https://api.vultr.com/v2/account", nil); err != nil {
			return "", err
		}

		return "API key accepted", nil

	case "hetzner":
		body, err := c.request("GET", "https://dns.hetzner.com/api/v1/zones", nil)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("token can see %d zone(s)", len(list(body["zones"]))), nil

	case "porkbun":
		if _, err := c.request("POST", "https://api.porkbun.com/api/json/v3/ping",
			c.porkbunBody(nil)); err != nil {
			return "", err
		}

		return "key and secret accepted", nil
	}

	return "", fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
}

// ----------------------------------------------------------------- edge TLS

// EdgeTLS is what a provider's proxy does about certificates for one zone.
//
// A proxy that terminates TLS presents its own certificate to the visitor, and
// then decides for itself how to reach the origin. That decision is the whole
// question: on Cloudflare, `flexible` means the origin is spoken to over plain
// HTTP, so a certificate here would be issued, renewed and never presented to
// anybody; `full` and `strict` mean the origin is dialled over HTTPS, so it
// needs one whatever the edge is doing.
type EdgeTLS struct {
	// Terminates is whether the visitor's TLS ends at the provider.
	Terminates bool

	// OriginNeedsCertificate is whether this machine still needs one of its
	// own for the proxy to reach it.
	OriginNeedsCertificate bool

	// Mode is the provider's own word for it, for the sentence that explains
	// the decision to whoever is reading the deployment.
	Mode string
}

// EdgeTLSFor reports what the provider is doing about TLS for this zone.
//
// Only Cloudflare answers today, because it is the only provider the panel
// talks to whose records can be proxied at all — SupportsProxy is the same
// question asked in the same table. A provider that cannot proxy is not
// terminating anything, and its sites need their own certificate.
func (c *Client) EdgeTLSFor(zoneID string) (EdgeTLS, error) {
	if !SupportsProxy(c.Account.Provider) {
		return EdgeTLS{OriginNeedsCertificate: true}, nil
	}

	body, err := c.request("GET",
		"https://api.cloudflare.com/client/v4/zones/"+zoneID+"/settings/ssl", nil)
	if err != nil {
		return EdgeTLS{}, err
	}

	return edgeTLSFromMode(strings.ToLower(str(object(body["result"]), "value", ""))), nil
}

// edgeTLSFromMode reads Cloudflare's own word for what it is doing.
func edgeTLSFromMode(mode string) EdgeTLS {
	switch mode {
	case "flexible":
		// The one case where this machine needs nothing: Cloudflare answers the
		// visitor over TLS and fetches from here over plain HTTP.
		return EdgeTLS{Terminates: true, Mode: mode}

	case "full", "strict":
		// TLS both ways, so the origin needs its own certificate however the
		// visitor is served.
		return EdgeTLS{Terminates: true, OriginNeedsCertificate: true, Mode: mode}
	}

	// "off", and anything nobody here recognises. Neither is a reason to skip
	// a certificate: being wrong this way costs a file nobody looks at, and
	// being wrong the other way is a site with no TLS at all.
	return EdgeTLS{OriginNeedsCertificate: true, Mode: mode}
}

// -------------------------------------------------------------------- zones

// Zones lists every zone the credential can see.
func (c *Client) Zones() ([]Zone, error) {
	zones := []Zone{}

	for page := 1; page <= 20; page++ {
		var url string
		var body map[string]any
		var err error

		switch c.Account.Provider {
		case "cloudflare":
			url = fmt.Sprintf("https://api.cloudflare.com/client/v4/zones?per_page=50&page=%d", page)
		case "digitalocean":
			url = fmt.Sprintf("https://api.digitalocean.com/v2/domains?per_page=200&page=%d", page)
		case "linode":
			url = fmt.Sprintf("https://api.linode.com/v4/domains?page=%d&page_size=100", page)
		case "vultr":
			url = "https://api.vultr.com/v2/domains?per_page=500"
		case "hetzner":
			url = fmt.Sprintf("https://dns.hetzner.com/api/v1/zones?per_page=100&page=%d", page)
		case "porkbun":
			url = "https://api.porkbun.com/api/json/v3/domain/listAll"
		default:
			return nil, fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
		}

		if c.Account.Provider == "porkbun" {
			body, err = c.request("POST", url, c.porkbunBody(nil))
		} else {
			body, err = c.request("GET", url, nil)
		}

		if err != nil {
			return nil, err
		}

		var entries []any
		idKey, nameKey := "id", "name"

		switch c.Account.Provider {
		case "cloudflare":
			entries = list(body["result"])
		case "digitalocean":
			entries = list(body["domains"])
			idKey = "name"
		case "linode":
			entries = list(body["data"])
			nameKey = "domain"
		case "vultr":
			entries = list(body["domains"])
			idKey, nameKey = "domain", "domain"
		case "hetzner":
			entries = list(body["zones"])
		case "porkbun":
			entries = list(body["domains"])
			idKey, nameKey = "domain", "domain"
		}

		for _, entry := range entries {
			zone := object(entry)

			name := str(zone, nameKey, "")
			if name == "" {
				continue
			}

			zones = append(zones, Zone{ID: identifier(zone, idKey, name), Name: name})
		}

		// Paging. Only the providers that actually page get another round.
		more := false

		switch c.Account.Provider {
		case "cloudflare":
			more = page < number(object(body["result_info"]), "total_pages", 1)
		case "linode":
			more = page < number(body, "pages", 1)
		case "hetzner", "digitalocean":
			more = len(entries) >= 100
		}

		if !more {
			break
		}
	}

	return zones, nil
}

// ErrNoZone is the provider answering plainly: it has no zone covering this
// name. It is wrapped into FindZone's error so a caller can tell it from a
// provider that could not be reached at all.
//
// The two want opposite handling. "This account does not hold the domain" is a
// fact about the domain and will still be true in a minute, so the panel says
// so and leaves the records to whoever does hold it. "The API did not answer"
// is a moment, so the panel keeps management on and tries again at deploy time.
// Treating the first as the second is what created a site whose deployment
// stopped on its second step, before nginx or WordPress, leaving nothing but
// NXDOMAIN.
var ErrNoZone = errors.New("no zone covers this name")

// FindZone is the zone that owns a hostname — longest matching suffix, so
// a.b.example.com prefers b.example.com over example.com, and a delegated
// subdomain beats the parent it was carved out of.
func (c *Client) FindZone(hostname string) (string, string, error) {
	hostname = strings.TrimRight(strings.ToLower(hostname), ".")

	zones, err := c.Zones()
	if err != nil {
		return "", "", err
	}

	bestID, bestName := "", ""

	for _, zone := range zones {
		name := strings.TrimRight(strings.ToLower(zone.Name), ".")

		if hostname != name && !strings.HasSuffix(hostname, "."+name) {
			continue
		}

		if len(name) > len(bestName) {
			bestID, bestName = zone.ID, name
		}
	}

	if bestName == "" {
		return "", "", fmt.Errorf("No zone on this %s account covers %s: %w",
			c.label(), hostname, ErrNoZone)
	}

	return bestID, bestName, nil
}

// ------------------------------------------------------------------ records

func (c *Client) recordsURL(zoneID, zoneName string, page int) string {
	switch c.Account.Provider {
	case "cloudflare":
		return fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?per_page=100&page=%d", zoneID, page)
	case "digitalocean":
		return fmt.Sprintf("https://api.digitalocean.com/v2/domains/%s/records?per_page=200&page=%d", zoneName, page)
	case "linode":
		return fmt.Sprintf("https://api.linode.com/v4/domains/%s/records?page=%d&page_size=100", zoneID, page)
	case "vultr":
		return fmt.Sprintf("https://api.vultr.com/v2/domains/%s/records?per_page=500", zoneName)
	case "hetzner":
		return fmt.Sprintf("https://dns.hetzner.com/api/v1/records?zone_id=%s&per_page=100&page=%d", zoneID, page)
	case "porkbun":
		return fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/retrieve/%s", zoneName)
	}

	return ""
}

// normalise turns one provider record into the panel's own shape.
func (c *Client) normalise(raw map[string]any, zoneName string) Record {
	record := Record{Priority: -1}

	switch c.Account.Provider {
	case "cloudflare":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "content", "")
		record.Priority = number(raw, "priority", -1)
		// 1 is Cloudflare's "automatic", which is not a TTL.
		record.TTL = number(raw, "ttl", 1)
		if record.TTL <= 1 {
			record.TTL = 0
		}
		record.Proxied = boolean(raw, "proxied", false)

	case "digitalocean":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "data", "")
		record.Priority = number(raw, "priority", -1)
		record.TTL = number(raw, "ttl", 0)

	case "linode":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "target", "")
		record.Priority = number(raw, "priority", -1)
		record.TTL = number(raw, "ttl_sec", 0)

	case "vultr":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "data", "")
		record.Priority = number(raw, "priority", -1)
		record.TTL = number(raw, "ttl", 0)

	case "hetzner":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "value", "")
		record.TTL = number(raw, "ttl", 0)

	case "porkbun":
		record.Type = str(raw, "type", "")
		record.Name = str(raw, "name", "")
		record.Content = str(raw, "content", "")
		if priority := number(raw, "prio", -1); priority > 0 {
			record.Priority = priority
		}
		record.TTL = number(raw, "ttl", 0)
	}

	record.Type = strings.ToUpper(record.Type)
	record.Name = Qualify(record.Name, zoneName)
	record.ID = identifier(raw, "id", "")

	splitPriority(&record)

	return record
}

// splitPriority pulls an MX priority out of the value, which is where Hetzner
// keeps it.
func splitPriority(record *Record) {
	if record.Type != "MX" || record.Priority >= 0 {
		return
	}

	first, rest, found := strings.Cut(strings.TrimSpace(record.Content), " ")
	if !found {
		return
	}

	priority, err := strconv.Atoi(first)
	if err != nil {
		return
	}

	record.Priority = priority
	record.Content = strings.TrimSpace(rest)
}

// Records lists a zone's records, normalised and sorted the way a zone file
// reads.
func (c *Client) Records(zoneID, zoneName string) ([]Record, error) {
	records := []Record{}

	for page := 1; page <= 20; page++ {
		url := c.recordsURL(zoneID, zoneName, page)
		if url == "" {
			return nil, fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
		}

		var body map[string]any
		var err error

		if c.Account.Provider == "porkbun" {
			body, err = c.request("POST", url, c.porkbunBody(nil))
		} else {
			body, err = c.request("GET", url, nil)
		}

		if err != nil {
			return nil, err
		}

		var entries []any

		switch c.Account.Provider {
		case "cloudflare":
			entries = list(body["result"])
		case "digitalocean":
			entries = list(body["domain_records"])
		case "linode":
			entries = list(body["data"])
		default:
			entries = list(body["records"])
		}

		for _, entry := range entries {
			records = append(records, c.normalise(object(entry), zoneName))
		}

		more := false

		switch c.Account.Provider {
		case "cloudflare":
			more = page < number(object(body["result_info"]), "total_pages", 1)
		case "linode":
			more = page < number(body, "pages", 1)
		}

		if !more {
			break
		}
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}

		return records[i].Type < records[j].Type
	})

	return records, nil
}

// FindRecordID is the id of an existing record the panel would replace.
func (c *Client) FindRecordID(zoneID, zoneName string, record Record) (string, error) {
	matches, err := c.matchingRecords(zoneID, zoneName, record)
	if err != nil || len(matches) == 0 {
		return "", err
	}

	return matches[0].ID, nil
}

// matchingRecords is every record in the zone that `record` replaces, in the
// order the provider lists them.
//
// More than one is the ordinary case rather than the odd one: a domain moved
// between hosts collects two A records at the same name, and both answer, so
// half the visitors land on the old server. The panel writes the first and
// deletes the rest, because "point this name here" is not "point this name
// here as well".
func (c *Client) matchingRecords(zoneID, zoneName string, record Record) ([]Record, error) {
	records, err := c.Records(zoneID, zoneName)
	if err != nil {
		return nil, err
	}

	matches := []Record{}

	for _, existing := range records {
		if replaces(existing, record) {
			matches = append(matches, existing)
		}
	}

	return matches, nil
}

// replaces reports whether an existing record is the one being written again.
//
// Type and name, except for TXT, where they are not enough. A domain's apex
// carries an SPF record, a DMARC record and however many verification strings
// Google, Microsoft and Atlassian have asked for, all of them TXT at the same
// name. Matching on type and name alone made the panel's SPF overwrite
// whichever of those the provider happened to list first — a domain that
// stopped verifying, for a reason nothing in the panel mentioned.
func replaces(existing, wanted Record) bool {
	if !strings.EqualFold(existing.Type, wanted.Type) {
		return false
	}

	if !strings.EqualFold(strings.TrimRight(existing.Name, "."),
		strings.TrimRight(wanted.Name, ".")) {
		return false
	}

	if strings.EqualFold(wanted.Type, "TXT") {
		return txtKind(existing.Content) == txtKind(wanted.Content)
	}

	return true
}

// txtKind is what a TXT record is for, which is written in its own first
// word. Anything the panel does not publish is `txt` — one bucket, so a
// verification string is never mistaken for a policy the panel owns.
func txtKind(content string) string {
	content = strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(content), "\"")))

	switch {
	case strings.HasPrefix(content, "v=spf1"):
		return "spf"
	case strings.HasPrefix(content, "v=dmarc1"):
		return "dmarc"
	case strings.HasPrefix(content, "v=dkim1"):
		return "dkim"
	}

	return "txt"
}

// payload is the body a create or update takes, in the provider's own
// spelling.
func (c *Client) payload(zoneName string, record Record) map[string]any {
	body := map[string]any{}

	switch c.Account.Provider {
	case "cloudflare":
		body["type"] = record.Type
		body["name"] = record.Name
		body["content"] = record.Content
		// ttl 1 is Cloudflare's "automatic"; proxied records must use it.
		ttl := 1
		if !record.Proxied && record.TTL > 0 {
			ttl = record.TTL
		}
		body["ttl"] = ttl
		if record.Priority >= 0 {
			body["priority"] = record.Priority
		}
		body["proxied"] = record.Proxied

	case "digitalocean":
		body["type"] = record.Type
		body["name"] = relativeName(record.Name, zoneName, "@")
		body["data"] = record.Content
		body["ttl"] = orDefault(record.TTL, 1800)
		if record.Priority >= 0 {
			body["priority"] = record.Priority
		}

	case "linode":
		body["type"] = record.Type
		body["name"] = relativeName(record.Name, zoneName, "")
		body["target"] = record.Content
		body["ttl_sec"] = orDefault(record.TTL, 0)
		if record.Priority >= 0 {
			body["priority"] = record.Priority
		}

	case "vultr":
		body["type"] = record.Type
		body["name"] = relativeName(record.Name, zoneName, "")
		body["data"] = record.Content
		body["ttl"] = orDefault(record.TTL, 300)
		if record.Priority >= 0 {
			body["priority"] = record.Priority
		}

	case "hetzner":
		body["type"] = record.Type
		body["name"] = relativeName(record.Name, zoneName, "@")
		// Hetzner keeps MX priority in the value, not as a field.
		if record.Type == "MX" && record.Priority >= 0 {
			body["value"] = fmt.Sprintf("%d %s", record.Priority, record.Content)
		} else {
			body["value"] = record.Content
		}
		body["ttl"] = orDefault(record.TTL, 3600)

	case "porkbun":
		body["name"] = relativeName(record.Name, zoneName, "")
		body["type"] = record.Type
		body["content"] = record.Content
		if record.TTL > 0 {
			body["ttl"] = strconv.Itoa(record.TTL)
		}
		if record.Priority >= 0 {
			body["prio"] = strconv.Itoa(record.Priority)
		}

		return c.porkbunBody(body)
	}

	return body
}

func orDefault(value, fallback int) int {
	if value > 0 {
		return value
	}

	return fallback
}

func (c *Client) recordEndpoint(zoneID, zoneName, recordID string) string {
	switch c.Account.Provider {
	case "cloudflare":
		if recordID != "" {
			return fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, recordID)
		}
		return fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", zoneID)

	case "digitalocean":
		if recordID != "" {
			return fmt.Sprintf("https://api.digitalocean.com/v2/domains/%s/records/%s", zoneName, recordID)
		}
		return fmt.Sprintf("https://api.digitalocean.com/v2/domains/%s/records", zoneName)

	case "linode":
		if recordID != "" {
			return fmt.Sprintf("https://api.linode.com/v4/domains/%s/records/%s", zoneID, recordID)
		}
		return fmt.Sprintf("https://api.linode.com/v4/domains/%s/records", zoneID)

	case "vultr":
		if recordID != "" {
			return fmt.Sprintf("https://api.vultr.com/v2/domains/%s/records/%s", zoneName, recordID)
		}
		return fmt.Sprintf("https://api.vultr.com/v2/domains/%s/records", zoneName)

	case "hetzner":
		if recordID != "" {
			return "https://dns.hetzner.com/api/v1/records/" + recordID
		}
		return "https://dns.hetzner.com/api/v1/records"

	case "porkbun":
		if recordID != "" {
			return fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/edit/%s/%s", zoneName, recordID)
		}
		return fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/create/%s", zoneName)
	}

	return ""
}

// CreateRecord writes a new record and returns the provider's id for it.
func (c *Client) CreateRecord(zoneID, zoneName string, record Record) (string, error) {
	url := c.recordEndpoint(zoneID, zoneName, "")
	if url == "" {
		return "", fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
	}

	body := c.payload(zoneName, record)

	// Hetzner's create needs the zone in the body, not in the path.
	if c.Account.Provider == "hetzner" {
		body["zone_id"] = zoneID
	}

	response, err := c.request("POST", url, body)
	if err != nil {
		return "", err
	}

	for _, key := range []string{"result", "record", "domain_record"} {
		if nested := object(response[key]); nested != nil {
			if id := identifier(nested, "id", ""); id != "" {
				return id, nil
			}
		}
	}

	return identifier(response, "id", ""), nil
}

// UpdateRecord replaces one.
func (c *Client) UpdateRecord(zoneID, zoneName, recordID string, record Record) error {
	url := c.recordEndpoint(zoneID, zoneName, recordID)
	if url == "" {
		return fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
	}

	method := "PUT"

	switch c.Account.Provider {
	case "digitalocean", "linode", "vultr":
		method = "PATCH"
	case "porkbun":
		method = "POST"
	}

	_, err := c.request(method, url, c.payload(zoneName, record))

	return err
}

// DeleteRecord removes one.
func (c *Client) DeleteRecord(zoneID, zoneName, recordID string) error {
	if c.Account.Provider == "porkbun" {
		url := fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/delete/%s/%s", zoneName, recordID)

		_, err := c.request("POST", url, c.porkbunBody(nil))

		return err
	}

	url := c.recordEndpoint(zoneID, zoneName, recordID)
	if url == "" {
		return fmt.Errorf("Unknown DNS provider: %s", c.Account.Provider)
	}

	_, err := c.request("DELETE", url, nil)

	return err
}

// Upsert is what writing one record did, so the console can say it: whether the
// record was already there, what it used to say, and how many copies of it the
// zone had collected.
type Upsert struct {
	ID       string
	Created  bool
	Previous string
	Removed  int
}

// UpsertRecord creates or overwrites, which is what "add an A record for www"
// means when there is one already, and what every provider's own UI does. It
// returns the record's id.
func (c *Client) UpsertRecord(zoneID, zoneName string, record Record) (string, error) {
	result, err := c.Upsert(zoneID, zoneName, record)

	return result.ID, err
}

// Upsert writes one record and leaves exactly one record behind.
//
// The zone is asked first, and every record the new one replaces is collected:
// the first is updated in place, keeping its id, and any others are deleted.
// Adding a second A record beside the one already at the name is the failure
// this exists to stop — the name then answers with both, so a browser reaches
// the old server as often as the new one and the panel looks intermittently
// broken.
func (c *Client) Upsert(zoneID, zoneName string, record Record) (Upsert, error) {
	existing, err := c.matchingRecords(zoneID, zoneName, record)
	if err != nil {
		return Upsert{}, err
	}

	if len(existing) == 0 {
		id, err := c.CreateRecord(zoneID, zoneName, record)

		return Upsert{ID: id, Created: true}, err
	}

	result := Upsert{ID: existing[0].ID, Previous: existing[0].Content}

	if err := c.UpdateRecord(zoneID, zoneName, result.ID, record); err != nil {
		return result, err
	}

	// The copies. A provider that will not delete one is not worth failing the
	// write for — the record the panel wanted is already right — so it is
	// counted and the caller says how many are left.
	for _, duplicate := range existing[1:] {
		if err := c.DeleteRecord(zoneID, zoneName, duplicate.ID); err == nil {
			result.Removed++
		}
	}

	return result, nil
}

// ClaimHost leaves one answer at a name.
//
// A, AAAA and CNAME all answer "where is this host", and a name carrying more
// than one of them answers differently depending on who asks: an old AAAA
// beside the panel's new A sends every visitor with IPv6 to the previous
// server, which is the same site being up and down at once. The type being
// written is kept; the other two go.
//
// Only ever at the exact name — nothing under it, nothing above it.
func (c *Client) ClaimHost(zoneID, zoneName, name, keep string) (int, error) {
	keep = strings.ToUpper(keep)

	if keep != "A" && keep != "AAAA" && keep != "CNAME" {
		return 0, nil
	}

	records, err := c.Records(zoneID, zoneName)
	if err != nil {
		return 0, err
	}

	removed := 0

	for _, existing := range records {
		kind := strings.ToUpper(existing.Type)

		if kind == keep || (kind != "A" && kind != "AAAA" && kind != "CNAME") {
			continue
		}

		if !strings.EqualFold(strings.TrimRight(existing.Name, "."), strings.TrimRight(name, ".")) {
			continue
		}

		if err := c.DeleteRecord(zoneID, zoneName, existing.ID); err == nil {
			removed++
		}
	}

	return removed, nil
}
