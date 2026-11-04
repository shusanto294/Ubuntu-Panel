package web

import "testing"

func TestParseFail2banEvent(t *testing.T) {
	for line, want := range map[string]banEvent{
		"2026-11-05 10:00:00,123 fail2ban.actions        [811]: NOTICE  [panel-web-secret] Ban 203.0.113.9": {"panel-web-secret", true, "203.0.113.9"},
		"NOTICE  [panel-web-scan] Unban 2001:db8::1":                                                        {"panel-web-scan", false, "2001:db8::1"},
		"2026-11-05 10:00:00,123 fail2ban.actions [811]: NOTICE  [recidive] Restore Ban 198.51.100.7":       {"recidive", true, "198.51.100.7"},
		"NOTICE  [panel-web-login] Increase Ban 198.51.100.8 (2 # 2:00:00 -> 2026-11-05 12:00:00)":          {},
	} {
		got, ok := parseFail2banEvent(line)
		if want == (banEvent{}) {
			continue
		}
		if !ok || got != want {
			t.Errorf("%q: got %+v %v, want %+v", line, got, ok, want)
		}
	}

	for _, line := range []string{
		"NOTICE  [panel-web-secret] Found 203.0.113.9 - 2026-11-05 10:00:00",
		"NOTICE  [sshd] Ban not-an-address",
		"INFO    [panel-web-secret] Jail started",
	} {
		if _, ok := parseFail2banEvent(line); ok {
			t.Errorf("%q is not a ban or unban", line)
		}
	}
}
