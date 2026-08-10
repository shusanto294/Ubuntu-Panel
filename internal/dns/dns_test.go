package dns

import (
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// Providers spell the apex three different ways, and a record written under the
// wrong one is a record for a hostname nobody asked for.
func TestQualify(t *testing.T) {
	cases := []struct{ name, zone, want string }{
		{"@", "example.com", "example.com"},
		{"", "example.com", "example.com"},
		{"example.com", "example.com", "example.com"},
		{"example.com.", "example.com", "example.com"},
		{"www", "example.com", "www.example.com"},
		{"www.example.com", "example.com", "www.example.com"},
		{"mail._domainkey", "example.com", "mail._domainkey.example.com"},
	}

	for _, test := range cases {
		if got := Qualify(test.name, test.zone); got != test.want {
			t.Errorf("Qualify(%q, %q) = %q, want %q", test.name, test.zone, got, test.want)
		}
	}
}

func TestRelativeName(t *testing.T) {
	cases := []struct{ name, zone, apex, want string }{
		{"example.com", "example.com", "@", "@"},
		{"example.com", "example.com", "", ""},
		{"www.example.com", "example.com", "@", "www"},
		{"a.b.example.com", "example.com", "@", "a.b"},
	}

	for _, test := range cases {
		if got := relativeName(test.name, test.zone, test.apex); got != test.want {
			t.Errorf("relativeName(%q, %q, %q) = %q, want %q",
				test.name, test.zone, test.apex, got, test.want)
		}
	}
}

// Hetzner keeps MX priority in the value rather than in a field, and a record
// whose content is "10 mail.example.com" points mail at a host called "10".
func TestSplitPriorityPullsHetznerMXApart(t *testing.T) {
	record := Record{Type: "MX", Content: "10 mail.example.com", Priority: -1}

	splitPriority(&record)

	if record.Priority != 10 || record.Content != "mail.example.com" {
		t.Fatalf("got priority %d content %q", record.Priority, record.Content)
	}
}

func TestSplitPriorityLeavesAFieldedPriorityAlone(t *testing.T) {
	record := Record{Type: "MX", Content: "mail.example.com", Priority: 20}

	splitPriority(&record)

	if record.Priority != 20 || record.Content != "mail.example.com" {
		t.Fatalf("got priority %d content %q", record.Priority, record.Content)
	}
}

// The orange cloud is Cloudflare's alone; offering it elsewhere would be
// offering something that does nothing.
func TestOnlyCloudflareSupportsProxying(t *testing.T) {
	if !SupportsProxy("cloudflare") {
		t.Error("Cloudflare should support proxying")
	}

	for _, provider := range []string{"digitalocean", "linode", "vultr", "hetzner", "porkbun"} {
		if SupportsProxy(provider) {
			t.Errorf("%s should not claim to support proxying", provider)
		}
	}
}

func TestPorkbunIsTheOnlyProviderNeedingASecret(t *testing.T) {
	if !NeedsSecret("porkbun") {
		t.Error("Porkbun needs both a key and a secret")
	}

	for _, provider := range []string{"cloudflare", "digitalocean", "linode", "vultr", "hetzner"} {
		if NeedsSecret(provider) {
			t.Errorf("%s should not ask for a secret", provider)
		}
	}
}

// A proxied Cloudflare record has to use their "automatic" TTL of 1; any other
// value is rejected outright.
func TestCloudflarePayloadForcesAutomaticTTLWhenProxied(t *testing.T) {
	client := &Client{Account: account("cloudflare")}

	body := client.payload("example.com", Record{
		Type: "A", Name: "example.com", Content: "203.0.113.10", Priority: -1,
		TTL: 3600, Proxied: true,
	})

	if body["ttl"] != 1 {
		t.Fatalf("proxied record kept ttl %v", body["ttl"])
	}

	if body["proxied"] != true {
		t.Fatal("the proxied flag did not survive")
	}
}

func TestHetznerPayloadPutsMXPriorityInTheValue(t *testing.T) {
	client := &Client{Account: account("hetzner")}

	body := client.payload("example.com", Record{
		Type: "MX", Name: "example.com", Content: "mail.example.com", Priority: 10,
	})

	if body["value"] != "10 mail.example.com" {
		t.Fatalf("value is %q", body["value"])
	}

	if _, ok := body["priority"]; ok {
		t.Fatal("Hetzner does not take a priority field")
	}
}

// Porkbun authenticates by putting both keys into every request body, so a
// payload without them is a request that will be refused.
func TestPorkbunPayloadCarriesTheCredentials(t *testing.T) {
	client := &Client{Account: account("porkbun")}

	body := client.payload("example.com", Record{
		Type: "A", Name: "www.example.com", Content: "203.0.113.10", Priority: -1,
	})

	if body["apikey"] != "token" || body["secretapikey"] != "secret" {
		t.Fatalf("credentials missing from the body: %v", body)
	}

	if body["name"] != "www" {
		t.Fatalf("name is %q, want the zone-relative form", body["name"])
	}
}

// account is a credential for a driver test; nothing here reaches the network.
func account(provider string) *store.DNSAccount {
	return &store.DNSAccount{
		ID:        1,
		Provider:  provider,
		Label:     provider,
		APIToken:  "token",
		APISecret: "secret",
	}
}
