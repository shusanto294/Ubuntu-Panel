package web

import (
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
)

func TestDuplicateRedirect(t *testing.T) {
	have := dns.RedirectRule(dns.Redirect{Hosts: []string{"a.com", "www.a.com"}, PathMode: dns.PathAll,
		Target: "https://b.com", StatusCode: 301, Enabled: true})
	have.ID = "abc"
	have.Raw = map[string]any{"action_parameters": have.ActionParameters}
	known := &dns.Ruleset{Rules: []dns.EdgeRule{have}}

	same := dns.Redirect{Hosts: []string{"www.a.com"}, PathMode: dns.PathAll}
	if duplicateRedirect(known, same, "") == "" {
		t.Error("a second redirect for www.a.com was let through")
	}
	if duplicateRedirect(known, same, "abc") != "" {
		t.Error("editing a redirect clashed with itself")
	}
	if duplicateRedirect(known, dns.Redirect{Hosts: []string{"a.com"}, PathMode: dns.PathExact, Path: "/x"}, "") != "" {
		t.Error("one page on a.com clashed with the whole-domain redirect")
	}
	if duplicateRedirect(nil, same, "") != "" {
		t.Error("no copy should mean no clash")
	}
}
