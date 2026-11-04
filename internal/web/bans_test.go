package web

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
)

func TestFirewallBlocks(t *testing.T) {
	rules := []security.Rule{
		{Number: 1, Action: "DENY", To: "Anywhere", From: "203.0.113.5"},
		{Number: 2, Action: "ALLOW", To: "22/tcp", From: "Anywhere"},
		{Number: 3, Action: "DENY", To: "3306", From: "Anywhere"},
		{Number: 4, Action: "REJECT", To: "Anywhere (v6)", From: "2001:db8::1 (v6)"},
		{Number: 5, Action: "DENY", To: "Anywhere", From: "198.51.100.0/24"},
	}

	blocks := firewallBlocks(rules)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d: %+v", len(blocks), blocks)
	}

	if blocks[1].From != "2001:db8::1" {
		t.Fatalf("the (v6) suffix should be dropped, got %q", blocks[1].From)
	}
}

func TestMergeBans(t *testing.T) {
	list := mergeBans(
		[]string{"1.1.1.1", "2.2.2.2", "3.3.3.3"},
		map[string][]string{"web_login": {"2.2.2.2"}},
		[]security.Rule{{Number: 7, From: "2.2.2.2"}},
		map[string]int64{"4.4.4.4": 99},
	)

	if len(list) != 4 {
		t.Fatalf("want 4 addresses, got %d", len(list))
	}

	if list[1].IP != "2.2.2.2" || len(list[1].Sources) != 3 {
		t.Fatalf("one row per address with every source, got %+v", list[1])
	}

	if list[0].IP != "1.1.1.1" || list[3].IP != "4.4.4.4" || list[3].Sources[0].Until != 99 {
		t.Fatalf("unexpected order or sources: %+v", list)
	}
}

func TestNewestFirst(t *testing.T) {
	list := []bannedAddress{
		{IP: "1.1.1.1", Sources: []banSource{{Kind: banSSH, At: 100}}},
		{IP: "2.2.2.2", Sources: []banSource{{Kind: banFirewall, Rule: 5}}},
		{IP: "3.3.3.3", Sources: []banSource{{Kind: banSSH, At: 300}, {Kind: banFirewall, Rule: 1, At: 50}}},
		{IP: "4.4.4.4", Sources: []banSource{{Kind: banFirewall, Rule: 4}}},
		{IP: "5.5.5.5", Sources: []banSource{{Kind: banSSH}}},
		{IP: "6.6.6.6", Sources: []banSource{{Kind: "web_scan", At: 200}}},
	}

	newestFirst(list)

	got := []string{}
	for _, row := range list {
		got = append(got, row.IP)
	}

	want := "3.3.3.3 6.6.6.6 1.1.1.1 4.4.4.4 2.2.2.2 5.5.5.5"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestAutoBanJail(t *testing.T) {
	p := security.ProtectionDefaults

	for _, attack := range []string{"login", "scan", "flood"} {
		if jail := autoBanJail(p, attack); jail != "" {
			t.Errorf("%s with every protection off went to %q", attack, jail)
		}
	}

	p.WebLogin.Enabled = true
	p.WebScan.Enabled = true

	want := map[string]string{"login": security.JailWebLogin, "scan": security.JailWebScan, "flood": security.JailWebScan}
	for attack, jail := range want {
		if got := autoBanJail(p, attack); got != jail {
			t.Errorf("%s went to %q, want %q", attack, got, jail)
		}
	}
}
