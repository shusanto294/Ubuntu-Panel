package security

import (
	"strings"
	"testing"
	"time"
)

func TestInstantBansRoundTrip(t *testing.T) {
	bans := map[string]int64{"203.0.113.9": 1700000000, "2001:db8::1": 1700000500}
	file := renderInstantBans(bans)

	for _, want := range []string{"deny 203.0.113.9; # 1700000000", "deny 2001:db8::1; # 1700000500"} {
		if !strings.Contains(file, want) {
			t.Errorf("missing %q:\n%s", want, file)
		}
	}

	back := parseInstantBans(file + "deny x; allow all;\nallow 1.2.3.4;\n")
	if len(back) != 2 || back["203.0.113.9"] != 1700000000 || back["2001:db8::1"] != 1700000500 {
		t.Errorf("read back %v", back)
	}
}

func TestStaleInstantBans(t *testing.T) {
	now := time.Unix(1700010000, 0)
	file := renderInstantBans(map[string]int64{
		"203.0.113.1": now.Add(-time.Hour).Unix(),   // old, still banned: kept
		"203.0.113.2": now.Add(-time.Hour).Unix(),   // old, no longer banned: stale
		"203.0.113.3": now.Add(-time.Minute).Unix(), // new, not yet in the list: kept
	})

	stale := StaleInstantBans(file, []string{"203.0.113.1"}, now, 20*time.Minute)
	if len(stale) != 1 || stale[0] != "203.0.113.2" {
		t.Errorf("stale = %v", stale)
	}
}

func TestWebJailsLeaveNginxToTheWorker(t *testing.T) {
	p := ProtectionDefaults
	p.WebScan.Enabled = true

	if file := ProtectJailFile(p, JailEnv{BotCheck: true}); strings.Contains(file, "action ") {
		t.Errorf("the jails keep fail2ban's own action; the panel's worker tells nginx:\n%s", file)
	}
}
