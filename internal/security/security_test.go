package security

import (
	"strings"
	"testing"
	"time"
)

func TestParseEvents(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	log := strings.Join([]string{
		"2026-10-30T11:00:00+0000 host sshd[10]: Invalid user admin from 203.0.113.9 port 50000",
		"2026-10-30T11:00:01+0000 host sshd[10]: Failed password for invalid user admin from 203.0.113.9 port 50000 ssh2",
		"2026-10-30T11:00:05+0000 host sshd[11]: Failed password for root from 203.0.113.9 port 50001 ssh2",
		"2026-10-30T11:05:00+0000 host sshd-session[12]: Invalid user test from 198.51.100.4 port 40000",
		"2026-10-30T11:30:00.123456+00:00 host sshd[13]: Accepted publickey for ubuntu from 192.0.2.1 port 4444 ssh2: ED25519 SHA256:x",
		"2026-10-28T11:30:00+0000 host sshd[14]: Failed password for root from 203.0.113.50 port 1 ssh2",
		"2026-10-30T11:40:00+0000 host sshd[15]: Connection closed by 203.0.113.9 port 50002 [preauth]",
	}, "\n")

	events := parseEvents(log, now, 24*time.Hour, 0)

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}

	if events[0].Kind != EventAccepted || events[0].User != "ubuntu" || events[0].Method != "publickey" {
		t.Fatalf("the newest event should be the accepted key: %+v", events[0])
	}

	summary := Summarise(events, 5)

	if summary.Accepted != 1 || summary.Failed != 3 || summary.UniqueIPs != 2 {
		t.Fatalf("summary: %+v", summary)
	}

	if summary.TopAttackers[0].IP != "203.0.113.9" || summary.TopAttackers[0].Attempts != 2 {
		t.Fatalf("top attacker: %+v", summary.TopAttackers[0])
	}
}

func TestParseClassicTimestamp(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)

	event, ok := ParseLine("Dec 31 23:59:00 host sshd[1]: Failed password for root from 203.0.113.9 port 1 ssh2", now)
	if !ok {
		t.Fatal("the line was not parsed")
	}

	if event.Time.Year() != 2025 {
		t.Fatalf("December read in January belongs to last year, got %v", event.Time)
	}
}

func TestParseRules(t *testing.T) {
	output := "Status: active\n\n" +
		"     To                         Action      From\n" +
		"     --                         ------      ----\n" +
		"[ 1] OpenSSH                    ALLOW IN    Anywhere\n" +
		"[ 2] 80/tcp                     ALLOW IN    Anywhere                   # Web\n" +
		"[ 3] Anywhere                   DENY IN     203.0.113.9\n" +
		"[ 4] Nginx Full                 ALLOW IN    Anywhere\n" +
		"[ 5] 22/tcp (v6)                LIMIT IN    Anywhere (v6)\n"

	rules := parseRules(output)

	if len(rules) != 5 {
		t.Fatalf("expected 5 rules, got %d: %+v", len(rules), rules)
	}

	if rules[1].To != "80/tcp" || rules[1].Comment != "Web" || rules[1].From != "Anywhere" {
		t.Fatalf("rule 2: %+v", rules[1])
	}

	if rules[2].Action != "DENY" || rules[2].From != "203.0.113.9" {
		t.Fatalf("rule 3: %+v", rules[2])
	}

	if rules[3].To != "Nginx Full" {
		t.Fatalf("rule 4: %+v", rules[3])
	}

	if !rules[4].IPv6 || rules[4].Action != "LIMIT" {
		t.Fatalf("rule 5: %+v", rules[4])
	}
}

func TestNewRule(t *testing.T) {
	rule := NewRule{Action: "allow", Port: "8080", Protocol: "tcp", From: "203.0.113.0/24", Comment: "Office"}

	if problems := rule.Validate(); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if got := rule.command(); got != "sudo ufw allow proto tcp from 203.0.113.0/24 to any port 8080 comment 'Office'" {
		t.Fatalf("command: %s", got)
	}

	hostile := NewRule{Action: "allow; reboot", Port: "22 && reboot", Protocol: "tcp", From: "$(id)", Comment: "`x`"}
	problems := hostile.Validate()

	for _, field := range []string{"action", "port", "from", "comment"} {
		if problems[field] == "" {
			t.Errorf("expected %s to be refused", field)
		}
	}
}

func TestSSHConfigValidate(t *testing.T) {
	config := SSHConfig{PermitRootLogin: "no", PasswordAuthentication: false, MaxAuthTries: 3}

	if problems := config.Validate(SSHState{}); problems["password_authentication"] == "" {
		t.Fatal("turning passwords off with no keys installed must be refused")
	}

	if problems := config.Validate(SSHState{AuthorizedKeys: []string{"ubuntu"}}); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	drop := SSHDropIn(config)
	if !strings.Contains(drop, "PasswordAuthentication no") || !strings.Contains(drop, "MaxAuthTries 3") {
		t.Fatalf("drop-in:\n%s", drop)
	}
}

func TestAptPeriodic(t *testing.T) {
	contents := "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"

	if aptPeriodic(contents, "Unattended-Upgrade") != 1 {
		t.Fatal("expected the unattended upgrade to be on")
	}

	if aptPeriodic("APT::Periodic::Unattended-Upgrade \"0\";", "Unattended-Upgrade") != 0 {
		t.Fatal("expected it to be off")
	}
}
