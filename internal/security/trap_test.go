package security

import (
	"regexp"
	"strings"
	"testing"
)

func TestTrapJailBansEverythingOnTheFirstTry(t *testing.T) {
	file := TrapJailFile([]string{"203.0.113.10"}, false)

	for _, want := range []string{
		"[ssh-trap]",
		"maxretry = 1",
		"bantime  = -1",
		"action   = iptables-allports[name=ssh-trap, protocol=all]",
		"ignoreip = 127.0.0.1/8 ::1 203.0.113.10",
		"backend  = systemd",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("jail is missing %q:\n%s", want, file)
		}
	}

	if kern := TrapJailFile(nil, true); !strings.Contains(kern, "logpath  = /var/log/kern.log") {
		t.Errorf("with kern.log the jail should read it:\n%s", kern)
	}
}

// The filter, as fail2ban would run it, against a line the rule logs.
func TestTrapFilterFindsTheAddress(t *testing.T) {
	var failregex string
	for _, line := range strings.Split(trapFilter, "\n") {
		if value, ok := strings.CutPrefix(line, "failregex = "); ok {
			failregex = strings.TrimSpace(value)
		}
	}

	pattern := regexp.MustCompile(strings.Replace(failregex, "<ADDR>", `(?P<addr>[0-9a-f.:]+)`, 1))

	for line, want := range map[string]string{
		"kernel: [12.3] UP-SSH-TRAP IN=eth0 OUT= MAC=aa:bb SRC=198.51.100.7 DST=10.0.0.2 LEN=60 PROTO=TCP SPT=51515 DPT=22": "198.51.100.7",
		"UP-SSH-TRAP IN=eth0 OUT= MAC=aa:bb SRC=2001:db8::7 DST=2001:db8::1 LEN=80 PROTO=TCP SPT=40000 DPT=22":              "2001:db8::7",
	} {
		match := pattern.FindStringSubmatch(line)
		if match == nil || match[1] != want {
			t.Errorf("from %q got %v, want %s", line, match, want)
		}
	}
}

func TestTrapRuleWatchesOnlyRealPorts(t *testing.T) {
	rule := trapRule([]string{"22", "2222", "22; rm -rf /"})

	if !strings.Contains(rule, "--dports 22,2222 ") {
		t.Errorf("rule should watch 22 and 2222 and nothing else: %s", rule)
	}

	if !strings.Contains(rule, "--ctstate NEW") || !strings.Contains(rule, "--log-prefix '"+trapPrefix+"'") {
		t.Errorf("rule should log new connections with the prefix: %s", rule)
	}

	if fallback := trapRule(nil); !strings.Contains(fallback, "--dports ssh ") {
		t.Errorf("with no ports known the rule should watch ssh: %s", fallback)
	}
}
