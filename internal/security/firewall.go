package security

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Rule is one line of `ufw status numbered`.
type Rule struct {
	Number    int    `json:"number"`
	To        string `json:"to"`
	Action    string `json:"action"`
	Direction string `json:"direction"`
	From      string `json:"from"`
	Comment   string `json:"comment"`
	IPv6      bool   `json:"ipv6"`
}

// Firewall is ufw's state.
type Firewall struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Incoming  string `json:"incoming"`
	Outgoing  string `json:"outgoing"`
	Rules     []Rule `json:"rules"`
}

// ReadFirewall asks ufw.
func ReadFirewall() Firewall {
	firewall := Firewall{Installed: proc.Which("ufw"), Rules: []Rule{}}

	if !firewall.Installed {
		return firewall
	}

	verbose := proc.Run("sudo ufw status verbose 2>&1", 30*time.Second).Output
	parseFirewallVerbose(verbose, &firewall)

	if firewall.Active {
		numbered := proc.Run("sudo ufw status numbered 2>&1", 30*time.Second).Output
		firewall.Rules = parseRules(numbered)
	}

	return firewall
}

var defaultLine = regexp.MustCompile(`(?m)^Default:\s*(\w+) \(incoming\),\s*(\w+) \(outgoing\)`)

func parseFirewallVerbose(output string, firewall *Firewall) {
	firewall.Active = strings.Contains(output, "Status: active")

	if match := defaultLine.FindStringSubmatch(output); match != nil {
		firewall.Incoming, firewall.Outgoing = match[1], match[2]
	}
}

var ruleLine = regexp.MustCompile(
	`^\[\s*(\d+)\]\s+(.*?)\s+(ALLOW|DENY|REJECT|LIMIT)(?:\s+(IN|OUT|FWD))?\s+(.*?)(?:\s+#\s*(.*))?$`)

func parseRules(output string) []Rule {
	rules := []Rule{}

	for _, line := range strings.Split(output, "\n") {
		match := ruleLine.FindStringSubmatch(strings.TrimRight(line, " "))
		if match == nil {
			continue
		}

		number, _ := strconv.Atoi(match[1])

		rule := Rule{
			Number:    number,
			To:        strings.TrimSpace(match[2]),
			Action:    match[3],
			Direction: match[4],
			From:      strings.TrimSpace(match[5]),
			Comment:   strings.TrimSpace(match[6]),
		}

		rule.IPv6 = strings.Contains(rule.To, "(v6)") || strings.Contains(rule.From, "(v6)")

		if rule.Direction == "" {
			rule.Direction = "IN"
		}

		rules = append(rules, rule)
	}

	return rules
}

// EnableFirewall switches ufw on, having first let in everything the machine
// needs to stay reachable: SSH on whichever ports sshd listens on, the web
// ports, and the panel's own port. In that order and before `enable`, because
// a firewall switched on with nothing allowed is the one change this page can
// make that the page cannot then undo.
func EnableFirewall(sshPorts []string, panelPort int) error {
	if !proc.Which("ufw") {
		return errors.New("ufw is not installed")
	}

	commands := []string{}

	for _, port := range sshPorts {
		if port == "ssh" {
			commands = append(commands, "sudo ufw allow OpenSSH")
			continue
		}

		if _, err := strconv.Atoi(port); err == nil {
			commands = append(commands, "sudo ufw allow "+port+"/tcp comment 'SSH'")
		}
	}

	if len(commands) == 0 {
		commands = append(commands, "sudo ufw allow OpenSSH")
	}

	commands = append(commands,
		"sudo ufw allow 80/tcp comment 'Web'",
		"sudo ufw allow 443/tcp comment 'Web'")

	if panelPort > 0 {
		commands = append(commands, fmt.Sprintf("sudo ufw allow %d/tcp comment 'Ubuntu Panel'", panelPort))
	}

	commands = append(commands, "sudo ufw --force enable")

	result := proc.Run(strings.Join(commands, " && "), time.Minute)
	if !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// DisableFirewall switches ufw off. The rules are kept, so switching it back
// on restores them.
func DisableFirewall() error {
	if result := proc.Run("sudo ufw --force disable", time.Minute); !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// NewRule is what the "add a rule" form posts.
type NewRule struct {
	Action   string // allow | deny | limit
	Port     string // 22, 8000:8100, or empty for every port
	Protocol string // tcp | udp | any
	From     string // an address or range, or empty for anywhere
	Comment  string
}

var (
	portPattern    = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)
	commentPattern = regexp.MustCompile(`^[\w .,:/()-]{0,60}$`)
)

// Validate is what the form checks. Every field ends up on a ufw command line,
// so each is held to a pattern rather than quoted and hoped about.
func (r NewRule) Validate() map[string]string {
	problems := map[string]string{}

	switch r.Action {
	case "allow", "deny", "limit":
	default:
		problems["action"] = "Choose allow, deny or limit."
	}

	switch r.Protocol {
	case "tcp", "udp", "any":
	default:
		problems["protocol"] = "Choose TCP, UDP or any."
	}

	if r.Port != "" {
		if !portPattern.MatchString(r.Port) || !portsInRange(r.Port) {
			problems["port"] = "Enter a port like 8080 or a range like 6000:6100."
		} else if strings.Contains(r.Port, ":") && r.Protocol == "any" {
			problems["protocol"] = "A port range needs a protocol: ufw will not take one for any."
		}
	}

	if r.From != "" && !ValidAddress(r.From) {
		problems["from"] = "Enter an IP address or a range like 203.0.113.0/24, or leave it empty."
	}

	if r.Port == "" && r.From == "" {
		problems["port"] = "Give a port, a source address, or both."
	}

	if !commentPattern.MatchString(r.Comment) {
		problems["comment"] = "Keep the note to 60 letters, numbers and simple punctuation."
	}

	return problems
}

func portsInRange(text string) bool {
	for _, part := range strings.Split(text, ":") {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}

	return true
}

// command is the ufw invocation for the rule, in the long form that takes a
// source and a destination port together.
func (r NewRule) command() string {
	parts := []string{"sudo ufw", r.Action}

	if r.Protocol != "any" {
		parts = append(parts, "proto", r.Protocol)
	}

	from := "any"
	if r.From != "" {
		from = r.From
	}
	parts = append(parts, "from", from, "to", "any")

	if r.Port != "" {
		parts = append(parts, "port", r.Port)
	}

	if r.Comment != "" {
		parts = append(parts, "comment", proc.Quote(r.Comment))
	}

	return strings.Join(parts, " ")
}

// AddRule adds it.
func AddRule(rule NewRule) error {
	if result := proc.Run(rule.command(), time.Minute); !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// DeleteRule removes a rule by its number in `ufw status numbered`.
func DeleteRule(number int) error {
	if number < 1 {
		return errors.New("that is not a rule number")
	}

	if result := proc.Run(fmt.Sprintf("sudo ufw --force delete %d", number), time.Minute); !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// BlockAddress refuses an address on every port, not just SSH.
//
// Inserted at the top, because ufw stops at the first rule that matches and an
// "allow 80 from anywhere" above it would let the address straight through.
// An IPv6 rule cannot go above the IPv4 ones, which is what the fallback is
// for: ufw keeps the two lists apart and puts the plain `deny` at the head of
// its own.
func BlockAddress(address, comment string) error {
	if !ValidAddress(address) {
		return errors.New("that is not an IP address or a range")
	}

	if !commentPattern.MatchString(comment) {
		comment = ""
	}

	note := ""
	if comment != "" {
		note = " comment " + proc.Quote(comment)
	}

	if proc.Run("sudo ufw insert 1 deny from "+address+note, time.Minute).OK() {
		return nil
	}

	if result := proc.Run("sudo ufw deny from "+address+note, time.Minute); !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}
