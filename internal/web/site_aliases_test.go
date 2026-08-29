package web

import (
	"strings"
	"testing"
)

// A root domain is what gets a www; a host inside one is not.
func TestIsApexDomain(t *testing.T) {
	apex := []string{
		"example.com",
		"tulibuilder.com",
		"example.co.uk",
		"example.com.au",
		"example.co.in",
	}

	inside := []string{
		"www.example.com",
		"app.example.com",
		"test.bdwebguy.com",
		"api.example.co.uk",
		"localhost",
		"",
	}

	for _, domain := range apex {
		if !isApexDomain(domain) {
			t.Errorf("%q is a root domain and should be given a www", domain)
		}
	}

	for _, domain := range inside {
		if isApexDomain(domain) {
			t.Errorf("%q is not a root domain and should not be given a www", domain)
		}
	}
}

// The whole of the bug this closes: a site created for a root domain has to
// come out of prepareSite already answering on its www.
func TestSiteAliasesAddsWWWForARootDomain(t *testing.T) {
	aliases := siteAliases("example.com", nil, true)

	if len(aliases) != 1 || aliases[0] != "www.example.com" {
		t.Fatalf("a root domain should be given its www, got %v", aliases)
	}
}

func TestSiteAliasesLeavesASubdomainAlone(t *testing.T) {
	if aliases := siteAliases("app.example.com", nil, true); len(aliases) != 0 {
		t.Fatalf("a host inside a domain has no www, got %v", aliases)
	}
}

func TestSiteAliasesRespectsTheAnswerNo(t *testing.T) {
	if aliases := siteAliases("example.com", nil, false); len(aliases) != 0 {
		t.Fatalf("www switched off should add nothing, got %v", aliases)
	}
}

// Asked for twice, in two spellings, is one name: a server_name that repeats a
// host is a vhost nginx refuses.
func TestSiteAliasesDeduplicates(t *testing.T) {
	aliases := siteAliases("example.com",
		[]string{"WWW.example.com", "www.example.com", "example.com", "old.example.net"}, true)

	if len(aliases) != 2 {
		t.Fatalf("expected the www and the old domain, got %v", aliases)
	}

	if aliases[0] != "www.example.com" || aliases[1] != "old.example.net" {
		t.Fatalf("unexpected hostnames: %v", aliases)
	}
}

// A name that is not a hostname would go into a server_name, and nginx refuses
// the whole configuration — every site on the machine — over one bad file.
func TestSiteAliasesDropsWhatIsNotAHostname(t *testing.T) {
	aliases := siteAliases("example.com", []string{"not a host", "-bad.example.com", ""}, false)

	if len(aliases) != 0 {
		t.Fatalf("expected nothing written, got %v", aliases)
	}
}

// The www is added after what was asked for, so the operator's own hostnames
// keep the order they were typed in.
func TestSiteAliasesKeepsTheRequestedOrder(t *testing.T) {
	aliases := siteAliases("example.com", []string{"shop.example.net", "example.org"}, true)

	if strings.Join(aliases, " ") != "shop.example.net example.org www.example.com" {
		t.Fatalf("unexpected order: %v", aliases)
	}
}
