// Package dns writes records at the DNS hosts the panel can drive.
//
// Every provider models the same three things — zones, records in a zone, and
// a credential — and disagrees about all the details: whether a record's name
// is relative or absolute, whether its value is called `content`, `data`,
// `target` or `value`, whether MX priority is a field or the first word of the
// value. This package absorbs that; callers work in Record values and fully
// qualified names.
//
// Zones are passed around as id *and* name because providers key on one or the
// other and there is no cheap way to look the other up mid-operation.
package dns

import "strings"

// Provider is what the credential form renders from.
type Provider struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	TokenLabel  string `json:"token_label"`
	SecretLabel string `json:"secret_label"` // empty when the provider needs only one
	Proxy       bool   `json:"proxy"`
	Help        string `json:"help"`
	URL         string `json:"url"`
}

// Providers is every DNS host the panel can write to.
var Providers = []Provider{
	{
		Key: "cloudflare", Label: "Cloudflare", TokenLabel: "API token", Proxy: true,
		Help: "Create a token with Zone · DNS · Edit on the zones you want the panel to manage.",
		URL:  "https://dash.cloudflare.com/profile/api-tokens",
	},
	{
		Key: "digitalocean", Label: "DigitalOcean", TokenLabel: "Personal access token",
		Help: "A token with write scope. The domain has to already exist under Networking · Domains.",
		URL:  "https://cloud.digitalocean.com/account/api/tokens",
	},
	{
		Key: "linode", Label: "Linode", TokenLabel: "Personal access token",
		Help: "A token with read/write access to Domains.",
		URL:  "https://cloud.linode.com/profile/tokens",
	},
	{
		Key: "vultr", Label: "Vultr", TokenLabel: "API key",
		Help: "Enable the API and allow this server's address in the access-control list, or every call is rejected.",
		URL:  "https://my.vultr.com/settings/#settingsapi",
	},
	{
		Key: "hetzner", Label: "Hetzner DNS", TokenLabel: "API token",
		Help: "From the Hetzner DNS console, not the Cloud console — they are separate products with separate tokens.",
		URL:  "https://dns.hetzner.com/settings/api-token",
	},
	{
		Key: "porkbun", Label: "Porkbun", TokenLabel: "API key", SecretLabel: "Secret key",
		Help: "Two keys, and API access has to be switched on per domain in the domain's own settings.",
		URL:  "https://porkbun.com/account/api",
	},
}

// FindProvider returns the entry for a key, or nil.
func FindProvider(key string) *Provider {
	for i := range Providers {
		if Providers[i].Key == key {
			return &Providers[i]
		}
	}

	return nil
}

// ProviderLabel is the provider's name, falling back to the bare key.
func ProviderLabel(key string) string {
	if provider := FindProvider(key); provider != nil {
		return provider.Label
	}

	return key
}

// ProviderExists, NeedsSecret and SupportsProxy are what the handlers validate
// against.
func ProviderExists(key string) bool { return FindProvider(key) != nil }

func NeedsSecret(key string) bool {
	provider := FindProvider(key)

	return provider != nil && provider.SecretLabel != ""
}

func SupportsProxy(key string) bool {
	provider := FindProvider(key)

	return provider != nil && provider.Proxy
}

// Record is one DNS record in the panel's own shape: the name is always fully
// qualified and without a trailing dot, the type is upper case, a priority of
// -1 means "not applicable", and a TTL of 0 means "the provider's automatic".
type Record struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	Priority int    `json:"priority"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	ID       string `json:"id"`
}

// Zone is a zone the credential can see.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Qualify makes a name absolute against its zone.
//
// Providers spell the apex as `@`, as the empty string, or as the zone name
// itself. Callers get one shape.
func Qualify(name, zone string) string {
	name = strings.TrimRight(name, ".")
	zone = strings.TrimRight(zone, ".")

	if name == "" || name == "@" || name == zone {
		return zone
	}

	if strings.HasSuffix(name, "."+zone) {
		return name
	}

	return name + "." + zone
}

// relativeName is the name relative to its zone: `www` for www.example.com,
// and apex for the zone itself — which each provider spells differently.
func relativeName(name, zone, apex string) string {
	name = strings.TrimRight(name, ".")
	zone = strings.TrimRight(zone, ".")

	if name == zone {
		return apex
	}

	if strings.HasSuffix(name, "."+zone) {
		return strings.TrimSuffix(name, "."+zone)
	}

	return name
}
