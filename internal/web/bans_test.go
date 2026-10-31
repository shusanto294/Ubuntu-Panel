package web

import (
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
		[]string{"1.1.1.1", "2.2.2.2"},
		[]string{"2.2.2.2", "3.3.3.3"},
		[]security.Rule{{Number: 7, From: "2.2.2.2"}},
		map[string]int64{"4.4.4.4": 99},
	)

	if len(list) != 4 {
		t.Fatalf("want 4 addresses, got %d", len(list))
	}

	if list[0].IP != "2.2.2.2" || len(list[0].Sources) != 3 {
		t.Fatalf("the address refused three ways should come first, got %+v", list[0])
	}

	if list[1].IP != "1.1.1.1" || list[3].IP != "4.4.4.4" || list[3].Sources[0].Until != 99 {
		t.Fatalf("unexpected order or sources: %+v", list)
	}
}
