package security

import "testing"

func TestParseListening(t *testing.T) {
	output := `tcp   LISTEN 0      511          0.0.0.0:80        0.0.0.0:*    users:(("nginx",pid=10,fd=6),("nginx",pid=11,fd=6))
tcp   LISTEN 0      128             [::]:22           [::]:*    users:(("sshd",pid=5,fd=4))
tcp   LISTEN 0      80         127.0.0.1:3306      0.0.0.0:*    users:(("mariadbd",pid=7,fd=20))
udp   UNCONN 0      0      127.0.0.53%lo:53        0.0.0.0:*    users:(("systemd-resolve",pid=3,fd=13))
tcp   LISTEN 0      511                *:8443            *:*
`
	found := parseListening(output)
	if len(found) != 5 {
		t.Fatalf("want 5 listeners, got %d: %+v", len(found), found)
	}

	want := []listener{
		{"80/tcp", true, "nginx"},
		{"22/tcp", true, "sshd"},
		{"3306/tcp", false, "mariadbd"},
		{"53/udp", false, "systemd-resolve"},
		{"8443/tcp", true, ""},
	}
	for i, w := range want {
		if found[i] != w {
			t.Errorf("listener %d: want %+v, got %+v", i, w, found[i])
		}
	}
}

func TestRuleTargets(t *testing.T) {
	cases := map[string][]string{
		"22/tcp":        {"22/tcp"},
		"80":            {"80/tcp", "80/udp"},
		"80,443/tcp":    {"80/tcp", "443/tcp"},
		"OpenSSH (v6)":  {"22/tcp"},
		"Nginx Full":    {"80/tcp", "443/tcp"},
		"Anywhere":      {},
		"6000:6100/tcp": {},
		"53 (v6)":       {"53/tcp", "53/udp"},
	}

	for to, want := range cases {
		keys, _ := ruleTargets(Rule{To: to})
		if len(keys) != len(want) {
			t.Errorf("%q: want %v, got %v", to, want, keys)
			continue
		}
		for i := range want {
			if keys[i] != want[i] {
				t.Errorf("%q: want %v, got %v", to, want, keys)
			}
		}
	}

	_, ranges := ruleTargets(Rule{To: "6000:6100/tcp"})
	if len(ranges) != 1 || ranges[0] != (portRange{6000, 6100, "tcp"}) {
		t.Errorf("range not read: %+v", ranges)
	}
}

func TestBuildPorts(t *testing.T) {
	listening := []listener{
		{"22/tcp", true, "sshd"},
		{"80/tcp", true, "nginx"},
		{"3306/tcp", false, "mariadbd"},
		{"6379/tcp", true, "redis-server"},
		{"7000/tcp", true, "myapp"},
	}
	firewall := Firewall{Active: true, Rules: []Rule{
		{Number: 1, To: "22/tcp", Action: "ALLOW", Direction: "IN", From: "Anywhere"},
		{Number: 2, To: "Nginx Full", Action: "ALLOW", Direction: "IN", From: "Anywhere"},
		{Number: 3, To: "3306/tcp", Action: "ALLOW", Direction: "IN", From: "203.0.113.4"},
		{Number: 4, To: "8443/tcp", Action: "ALLOW", Direction: "IN", From: "Anywhere"},
		{Number: 5, To: "Anywhere", Action: "DENY", Direction: "IN", From: "198.51.100.7"},
	}}

	ports := map[string]Port{}
	for _, port := range buildPorts(listening, firewall, []string{"22"}, 8443, Needed{}) {
		ports[port.Name] = port
	}

	if p := ports["SSH"]; !p.Open || p.Locked != "" {
		t.Errorf("SSH should be open and closable: %+v", p)
	}
	if p := ports["Ubuntu Panel"]; !p.Open || p.Locked == "" || p.Listening {
		t.Errorf("the panel's port should be open and locked: %+v", p)
	}
	if p := ports["HTTPS (websites)"]; !p.Open || p.Listening {
		t.Errorf("443 is opened by Nginx Full: %+v", p)
	}
	if p := ports["MariaDB / MySQL"]; p.Open || p.Public || len(p.AllowedFrom) != 1 || p.Risk != "risky" {
		t.Errorf("MariaDB is local and allowed from one address only: %+v", p)
	}
	if p := ports["Redis"]; p.Open || !p.Public || p.Risk != "risky" {
		t.Errorf("Redis listens publicly but the firewall keeps it closed: %+v", p)
	}
	if p := ports["myapp"]; p.Description != "Opened by myapp." {
		t.Errorf("an unknown port is described by its program: %+v", p)
	}
}

func TestBlocksAddress(t *testing.T) {
	cases := []struct {
		rule Rule
		want bool
	}{
		{Rule{To: "Anywhere", Action: "DENY", From: "198.51.100.7"}, true},
		{Rule{To: "Anywhere (v6)", Action: "REJECT", From: "2001:db8::1 (v6)"}, true},
		{Rule{To: "Anywhere", Action: "DENY", From: "203.0.113.0/24"}, true},
		{Rule{To: "3306/tcp", Action: "DENY", From: "Anywhere"}, false},
		{Rule{To: "22/tcp", Action: "ALLOW", From: "198.51.100.7"}, false},
	}

	for _, c := range cases {
		if got := blocksAddress(c.rule); got != c.want {
			t.Errorf("%+v: want %v, got %v", c.rule, c.want, got)
		}
	}
}

func TestClosable(t *testing.T) {
	needed := Needed{
		Open: map[string]string{"22/tcp": "SSH", "80/tcp": "Websites", "443/tcp": "Websites", "25/tcp": "Mail"},
		Keep: map[string]string{"3306/tcp": "Databases"},
	}

	cases := []struct {
		rule Rule
		want bool
	}{
		{Rule{To: "22/tcp", Action: "ALLOW", From: "Anywhere"}, false},
		{Rule{To: "Nginx Full", Action: "ALLOW", From: "Anywhere"}, false},
		{Rule{To: "3306/tcp", Action: "ALLOW", From: "Anywhere"}, false},
		{Rule{To: "6379/tcp", Action: "ALLOW", From: "Anywhere"}, true},
		{Rule{To: "80", Action: "ALLOW", From: "Anywhere"}, true}, // 80/udp is not needed
		{Rule{To: "25,465/tcp", Action: "ALLOW", From: "Anywhere"}, true},
		{Rule{To: "6000:6100/tcp", Action: "ALLOW", From: "Anywhere"}, true},
		{Rule{To: "Anywhere", Action: "ALLOW", From: "Anywhere"}, true},
		{Rule{To: "6379/tcp", Action: "ALLOW", From: "203.0.113.4"}, false},
		{Rule{To: "6379/tcp", Action: "ALLOW", Direction: "OUT", From: "Anywhere"}, false},
		{Rule{To: "6379/tcp", Action: "DENY", From: "Anywhere"}, false},
	}

	for _, c := range cases {
		if got := closable(c.rule, needed); got != c.want {
			t.Errorf("%+v: want %v, got %v", c.rule, c.want, got)
		}
	}
}
