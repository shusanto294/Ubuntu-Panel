package security

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Port forwarding: a port on this server that leads somewhere else — another
// port here (expose an app on a port of its own without opening the app's
// real port), or a port on another machine (reach a database on a private
// network through this one). IPv4 only.
//
// Written into ufw's own rules file between two marker lines, so ufw loads
// the forwards with everything else and `ufw reload` keeps them. The traffic
// they let in is let in only because it was forwarded (conntrack's DNAT
// state): the target port is not opened to the world by a forward.

// Forward is one forwarded port.
type Forward struct {
	Protocol string `json:"protocol"` // tcp | udp
	Port     int    `json:"port"`     // on this server
	ToIP     string `json:"to_ip"`    // 127.0.0.1 for this server
	ToPort   int    `json:"to_port"`
	Comment  string `json:"comment"`
}

// Local is whether the target is this server.
func (f Forward) Local() bool { return f.ToIP == "" || f.ToIP == "127.0.0.1" }

// Validate is what the form checks.
func (f Forward) Validate() map[string]string {
	problems := map[string]string{}

	if f.Protocol != "tcp" && f.Protocol != "udp" {
		problems["protocol"] = "Choose TCP or UDP."
	}
	if f.Port < 1 || f.Port > 65535 {
		problems["port"] = "A port is between 1 and 65535."
	}
	if f.ToPort < 1 || f.ToPort > 65535 {
		problems["to_port"] = "A port is between 1 and 65535."
	}
	if !f.Local() {
		if ip := net.ParseIP(f.ToIP); ip == nil || ip.To4() == nil {
			problems["to_ip"] = "Forward to an IPv4 address, or leave it empty for this server."
		}
	}
	if f.Local() && f.Port == f.ToPort {
		problems["to_port"] = "That would forward the port to itself."
	}
	for _, r := range f.Comment {
		if r < ' ' || r == '"' || r == '\'' || r == '\\' {
			problems["comment"] = "A note is plain text without quotes."
			break
		}
	}
	if len(f.Comment) > 60 {
		problems["comment"] = "Keep the note under 60 characters."
	}

	return problems
}

const (
	ufwBeforeRules = "/etc/ufw/before.rules"
	forwardStart   = "# BEGIN Ubuntu Panel port forwards — managed from Security → Firewall"
	forwardEnd     = "# END Ubuntu Panel port forwards"
	forwardSysctl  = "/etc/sysctl.d/62-ubuntu-panel-forward.conf"
)

// ForwardRules renders the nat table and the filter lines for the forwards,
// "" for none.
func ForwardRules(forwards []Forward) (nat, filter string) {
	if len(forwards) == 0 {
		return "", ""
	}

	var n, f strings.Builder

	n.WriteString("*nat\n:PREROUTING ACCEPT [0:0]\n:POSTROUTING ACCEPT [0:0]\n")
	for _, fw := range forwards {
		comment := ""
		if fw.Comment != "" {
			comment = fmt.Sprintf(" -m comment --comment \"%s\"", fw.Comment)
		}
		if fw.Local() {
			fmt.Fprintf(&n, "-A PREROUTING -p %s --dport %d -j REDIRECT --to-ports %d%s\n", fw.Protocol, fw.Port, fw.ToPort, comment)
			fmt.Fprintf(&f, "-A ufw-before-input -p %s --dport %d -m conntrack --ctstate DNAT -j ACCEPT\n", fw.Protocol, fw.ToPort)
		} else {
			fmt.Fprintf(&n, "-A PREROUTING -p %s --dport %d -j DNAT --to-destination %s:%d%s\n", fw.Protocol, fw.Port, fw.ToIP, fw.ToPort, comment)
			fmt.Fprintf(&n, "-A POSTROUTING -p %s -d %s --dport %d -j MASQUERADE\n", fw.Protocol, fw.ToIP, fw.ToPort)
			fmt.Fprintf(&f, "-A ufw-before-forward -p %s -d %s --dport %d -m conntrack --ctstate DNAT -j ACCEPT\n", fw.Protocol, fw.ToIP, fw.ToPort)
			fmt.Fprintf(&f, "-A ufw-before-forward -p %s -s %s --sport %d -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT\n", fw.Protocol, fw.ToIP, fw.ToPort)
		}
	}
	n.WriteString("COMMIT\n")

	return n.String(), f.String()
}

// withForwards is ufw's before.rules with the panel's block replaced: the nat
// table before the filter table, the filter lines before the filter table's
// last COMMIT.
func withForwards(rules string, forwards []Forward) (string, error) {
	rules = withoutForwards(rules)

	nat, filter := ForwardRules(forwards)
	if nat == "" {
		return rules, nil
	}

	filterAt := strings.Index(rules, "*filter")
	commitAt := strings.LastIndex(rules, "\nCOMMIT")
	if filterAt < 0 || commitAt < filterAt {
		return "", errors.New("ufw's before.rules does not look like ufw's: no *filter table to add to")
	}

	block := func(body string) string { return forwardStart + "\n" + body + forwardEnd + "\n" }

	return rules[:filterAt] + block(nat) + rules[filterAt:commitAt+1] + block(filter) + rules[commitAt+1:], nil
}

// withoutForwards takes every panel block out.
func withoutForwards(rules string) string {
	for {
		start := strings.Index(rules, forwardStart)
		if start < 0 {
			return rules
		}
		end := strings.Index(rules[start:], forwardEnd)
		if end < 0 {
			return rules[:start]
		}
		end += start + len(forwardEnd)
		if end < len(rules) && rules[end] == '\n' {
			end++
		}
		rules = rules[:start] + rules[end:]
	}
}

// ApplyForwards writes the forwards into ufw and reloads it; the old rules
// come back if ufw refuses the new ones. A forward to another machine needs
// the kernel to forward packets, which is switched on for as long as there is
// one.
func ApplyForwards(forwards []Forward) error {
	if !proc.Which("ufw") {
		return errors.New("port forwarding is part of the firewall, and ufw is not installed")
	}

	previous, err := proc.ReadFile(ufwBeforeRules, true)
	if err != nil {
		return fmt.Errorf("could not read ufw's rules: %w", err)
	}

	next, err := withForwards(previous, forwards)
	if err != nil {
		return err
	}

	remote := false
	for _, forward := range forwards {
		remote = remote || !forward.Local()
	}
	if remote {
		if err := proc.PutFile(forwardSysctl, "# Managed by Ubuntu Panel — port forwarding to other machines.\n"+
			"net.ipv4.ip_forward = 1\n", true); err != nil {
			return err
		}
		proc.Run("sudo sysctl -p "+forwardSysctl+" >/dev/null 2>&1", time.Minute)
	} else if proc.Exists(forwardSysctl, true) {
		proc.Run("sudo rm -f "+forwardSysctl, time.Minute)
	}

	if next == previous {
		return nil
	}

	if err := proc.PutFile(ufwBeforeRules, next, true); err != nil {
		return err
	}

	active := strings.Contains(proc.Run("sudo ufw status 2>&1", 30*time.Second).Output, "Status: active")
	if !active {
		return nil
	}

	if result := proc.Run("sudo ufw reload 2>&1", 2*time.Minute); !result.OK() {
		proc.PutFile(ufwBeforeRules, previous, true)
		proc.Run("sudo ufw reload 2>&1", 2*time.Minute)
		return fmt.Errorf("ufw refused the forward, so the firewall was left as it was: %s", lastLines(result.Output, 3))
	}

	return nil
}

// ParseForwardKey reads "tcp/8080" into its parts, for removing one.
func ParseForwardKey(key string) (string, int, bool) {
	protocol, port, found := strings.Cut(key, "/")
	number, err := strconv.Atoi(port)
	return protocol, number, found && err == nil
}
