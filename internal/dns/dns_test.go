package dns

import (
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
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

// Deleting a mail domain has to take its records with it, including the ones
// published before the panel recorded their ids. Matching is exact on purpose:
// a verification TXT somebody else put at the apex is not the panel's to
// remove.
func TestOnlyThePanelsOwnMailRecordsAreMatched(t *testing.T) {
	domain := &store.EmailDomain{Domain: "example.com", DKIMSelector: "mail"}

	mine := []Record{
		{Type: "A", Name: "mail.example.com", Content: "203.0.113.10"},
		{Type: "MX", Name: "example.com", Content: "mail.example.com", Priority: 10},
		{Type: "MX", Name: "example.com", Content: "mail.example.com.", Priority: 10},
		{Type: "TXT", Name: "example.com", Content: "v=spf1 a:mail.example.com ~all"},
		{Type: "TXT", Name: "_dmarc.example.com", Content: "v=DMARC1; p=none"},
		{Type: "TXT", Name: "mail._domainkey.example.com", Content: "v=DKIM1; k=rsa; p=MIIB"},
	}

	for _, record := range mine {
		if !ours(record, domain) {
			t.Errorf("%s %s %q was not recognised as the panel's", record.Type, record.Name, record.Content)
		}
	}

	theirs := []Record{
		{Type: "A", Name: "example.com", Content: "203.0.113.10"},
		{Type: "A", Name: "www.example.com", Content: "203.0.113.10"},
		{Type: "MX", Name: "example.com", Content: "aspmx.l.google.com", Priority: 1},
		{Type: "TXT", Name: "example.com", Content: "google-site-verification=abc"},
		{Type: "TXT", Name: "example.com", Content: "v=spf1 include:_spf.google.com ~all"},
		{Type: "TXT", Name: "_dmarc.example.com", Content: "some other note"},
		{Type: "TXT", Name: "selector2._domainkey.example.com", Content: "v=DKIM1; k=rsa; p=ZZZ"},
		{Type: "CNAME", Name: "mail.example.com", Content: "elsewhere.example"},
	}

	for _, record := range theirs {
		if ours(record, domain) {
			t.Errorf("%s %s %q would have been deleted and is not the panel's",
				record.Type, record.Name, record.Content)
		}
	}
}

// The record a mail client looks up to configure itself is published with the
// rest.
//
// Thunderbird asks `autoconfig.<domain>` for one XML file the moment somebody
// types an address on the domain, and takes the hostnames and ports out of it.
// Without the record that lookup fails, the client falls back to guessing, and
// the person is left typing four settings they were never told — which is the
// mailbox setup this record exists to remove.
func TestTheAutoconfigRecordIsPublishedWithTheMailRecords(t *testing.T) {
	domain := &store.EmailDomain{Domain: "example.com"}

	records := MailRecords(domain, "203.0.113.10")

	var found *Record

	for i, record := range records {
		if record.Name == "autoconfig.example.com" {
			found = &records[i]
		}
	}

	if found == nil {
		t.Fatalf("no record for autoconfig.example.com was published:\n%+v", records)
	}

	if found.Type != "A" || found.Content != "203.0.113.10" {
		t.Errorf("autoconfig.example.com is %s %q, wanted an A at this server",
			found.Type, found.Content)
	}

	// And it is the panel's to remove again, or deleting the domain leaves a
	// hostname pointing at a server that no longer serves the file.
	if !ours(Record{Type: "A", Name: "autoconfig.example.com", Content: "203.0.113.10"}, domain) {
		t.Error("the autoconfig record would be left behind when the domain is deleted")
	}
}

// A selector other than the default still has its key removed.
func TestADKIMRecordIsMatchedOnTheDomainsOwnSelector(t *testing.T) {
	domain := &store.EmailDomain{Domain: "example.com", DKIMSelector: "panel2026"}

	record := Record{Type: "TXT", Name: "panel2026._domainkey.example.com", Content: "v=DKIM1; p=x"}

	if !ours(record, domain) {
		t.Fatal("the domain's own DKIM record was not recognised")
	}
}

// Which Cloudflare settings mean this machine can skip Let's Encrypt.
//
// Only Flexible does. Full and Strict have Cloudflare dialling the origin over
// HTTPS, so a machine without a certificate answers those requests with
// nothing — the 525 that looks exactly like a broken site.
func TestOnlyFlexibleLetsTheOriginSkipItsCertificate(t *testing.T) {
	cases := []struct {
		mode       string
		terminates bool
		needsCert  bool
	}{
		{"flexible", true, false},
		{"full", true, true},
		{"strict", true, true},
		{"off", false, true},
		{"", false, true},
		{"something-new", false, true},
	}

	for _, test := range cases {
		edge := edgeTLSFromMode(test.mode)

		if edge.Terminates != test.terminates {
			t.Errorf("%q: terminates = %v, want %v", test.mode, edge.Terminates, test.terminates)
		}

		if edge.OriginNeedsCertificate != test.needsCert {
			t.Errorf("%q: the origin needs a certificate = %v, want %v",
				test.mode, edge.OriginNeedsCertificate, test.needsCert)
		}
	}
}
