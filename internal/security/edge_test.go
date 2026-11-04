package security

import (
	"strings"
	"testing"
)

func TestNginxBansFile(t *testing.T) {
	if NginxBansFile(nil) != "" || NginxBansFile([]string{"not an ip", ""}) != "" {
		t.Error("no valid address, no file")
	}

	file := NginxBansFile([]string{"203.0.113.9", "2001:db8::1", "203.0.113.9", "x; allow all", "198.51.100.0/24"})

	for _, want := range []string{"deny 203.0.113.9;", "deny 2001:db8::1;", "deny 198.51.100.0/24;"} {
		if !strings.Contains(file, want) {
			t.Errorf("missing %q:\n%s", want, file)
		}
	}
	if strings.Count(file, "deny 203.0.113.9;") != 1 || strings.Contains(file, "allow") {
		t.Errorf("duplicates or injected text:\n%s", file)
	}
}

func TestFromCloudflare(t *testing.T) {
	for _, ip := range []string{"104.21.80.186", "172.67.153.31", "2606:4700::1"} {
		if !FromCloudflare(ip) {
			t.Errorf("%s is Cloudflare's", ip)
		}
	}
	for _, ip := range []string{"15.235.244.52", "8.8.8.8", "nonsense"} {
		if FromCloudflare(ip) {
			t.Errorf("%s is not Cloudflare's", ip)
		}
	}
}

func TestOpenWebRule(t *testing.T) {
	open := []Rule{
		{Action: "ALLOW", To: "80/tcp", From: "Anywhere"},
		{Action: "ALLOW", To: "443/tcp (v6)", From: "Anywhere (v6)"},
		{Action: "ALLOW", To: "Nginx Full", From: "Anywhere"},
		{Action: "ALLOW", To: "80,443/tcp", From: "Anywhere"},
	}
	for _, rule := range open {
		if !openWebRule(rule) {
			t.Errorf("%+v opens the web ports to everyone", rule)
		}
	}

	closed := []Rule{
		{Action: "ALLOW", To: "80,443/tcp", From: "173.245.48.0/20"},
		{Action: "ALLOW", To: "22/tcp", From: "Anywhere"},
		{Action: "ALLOW", To: "8443/tcp", From: "Anywhere"},
		{Action: "DENY", To: "80/tcp", From: "Anywhere"},
	}
	for _, rule := range closed {
		if openWebRule(rule) {
			t.Errorf("%+v does not open the web ports to everyone", rule)
		}
	}
}
