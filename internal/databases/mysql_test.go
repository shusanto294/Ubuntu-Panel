package databases

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseAllow(t *testing.T) {
	entries, err := ParseAllow("203.0.113.10, 10.0.0.0/24\nApp.Example.com 2001:db8::1 203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"203.0.113.10", "10.0.0.0/24", "app.example.com", "2001:db8::1"}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %v, want %v", entries, want)
	}

	for _, bad := range []string{
		"0.0.0.0", "0.0.0.0/0", "2001:db8::/32", "localhost", "evil'@'%", "a b;c", "999.1.1.1", "-x.com",
	} {
		if bad == "a b;c" {
			// Separators split it; "a", "b" and "c" are not domains.
			if _, err := ParseAllow(bad); err == nil {
				t.Errorf("%q should be refused", bad)
			}
			continue
		}
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestTargets(t *testing.T) {
	if got := (Access{Mode: AccessLocal, Allow: []string{"203.0.113.10"}}).targets(); len(got) != 0 {
		t.Errorf("this server only should allow nothing else, got %v", got)
	}

	if got := (Access{Mode: AccessAnywhere}).targets(); !reflect.DeepEqual(got, []target{{host: "%", source: "any"}}) {
		t.Errorf("anywhere = %v", got)
	}

	access := Access{
		Mode:     AccessList,
		Allow:    []string{"203.0.113.10", "10.1.2.0/24", "app.example.com", "gone.example.com"},
		Resolved: map[string][]string{"app.example.com": {"198.51.100.7", "203.0.113.10"}},
	}

	want := []target{
		{host: "203.0.113.10", source: "203.0.113.10"},
		{host: "10.1.2.0/255.255.255.0", source: "10.1.2.0/24"},
		{host: "198.51.100.7", source: "198.51.100.7"},
	}
	if got := access.targets(); !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
}

func TestResolveKeepsLastAddresses(t *testing.T) {
	saved := lookup
	defer func() { lookup = saved }()

	lookup = func(string) ([]string, error) { return nil, errors.New("timeout") }

	access := Access{Mode: AccessList, Allow: []string{"app.example.com"},
		Resolved: map[string][]string{"app.example.com": {"198.51.100.7"}}}

	if err := access.resolve(false); err != nil || len(access.Resolved["app.example.com"]) != 1 {
		t.Errorf("a failed lookup should keep the last addresses: %v %v", err, access.Resolved)
	}

	if err := access.resolve(true); err == nil {
		t.Error("saving a domain that does not resolve should be refused")
	}
}

func TestAccountScriptHosts(t *testing.T) {
	script := mysqlAccountScript("wp_site", "wp_user", "secret", "", []string{"localhost", "127.0.0.1"})

	if strings.Contains(script, "'%'") {
		t.Error("an account for this server only should not exist at '%'")
	}
	if !strings.Contains(script, "'wp_user'@'localhost'") || !strings.Contains(script, "'wp_user'@'127.0.0.1'") {
		t.Errorf("the local accounts are missing:\n%s", script)
	}
}

func TestNativeHash(t *testing.T) {
	if !nativeHash("*" + strings.Repeat("A1", 20)) {
		t.Error("a native password hash was refused")
	}
	for _, bad := range []string{"", "*abc", "invalid", "*" + strings.Repeat("Z", 40)} {
		if nativeHash(bad) {
			t.Errorf("%q is not a native password hash", bad)
		}
	}
}
