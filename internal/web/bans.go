package web

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// Four things on this machine refuse an address, each kept by the program
// that enforces it rather than copied into the panel: fail2ban's SSH jail
// (the SSH port only), fail2ban's trap jail (every port), ufw's deny rules
// (every port) and the panel's own sign-in lockout (the panel only). They stay
// separate because they block different things, but the Banned IPs tab reads
// all four into one list, one row per address, so nobody has to look in four
// places to find out why an address cannot get in.

// Ban sources, as the page names them.
const (
	banSSH      = "ssh"
	banTrap     = "trap"
	banFirewall = "firewall"
	banPanel    = "panel"
)

// banSource is one reason an address is refused.
type banSource struct {
	Kind string `json:"kind"`

	// Rule is the ufw rule number, for a firewall block.
	Rule int `json:"rule,omitempty"`

	// Comment is the ufw rule's note, for a firewall block.
	Comment string `json:"comment,omitempty"`

	// Until is when a panel lockout ends, in Unix seconds.
	Until int64 `json:"until,omitempty"`
}

// bannedAddress is one row of the Banned IPs tab.
type bannedAddress struct {
	IP      string      `json:"ip"`
	Sources []banSource `json:"sources"`
}

// banCounts is the tab's tiles. SSHTotal and TrapTotal are fail2ban's "since it
// started" counts, which a restart resets.
type banCounts struct {
	Total     int  `json:"total"`
	SSH       int  `json:"ssh"`
	SSHTotal  int  `json:"ssh_total"`
	Trap      int  `json:"trap"`
	TrapTotal int  `json:"trap_total"`
	TrapOn    bool `json:"trap_on"`
	Firewall  int  `json:"firewall"`
	Panel     int  `json:"panel"`
}

// firewallBlocks are the ufw rules that refuse an address on every port: a
// deny or reject from a specific address to anywhere. A deny of one port from
// anywhere is a closed port, not a ban, and is left on the Firewall tab.
func firewallBlocks(rules []security.Rule) []security.Rule {
	blocks := []security.Rule{}

	for _, rule := range rules {
		if rule.Action != "DENY" && rule.Action != "REJECT" {
			continue
		}

		if !strings.HasPrefix(strings.TrimSpace(rule.To), "Anywhere") {
			continue
		}

		from := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rule.From), "(v6)"))
		if !security.ValidAddress(from) {
			continue
		}

		rule.From = from
		blocks = append(blocks, rule)
	}

	return blocks
}

// mergeBans puts the four lists together, one row per address, sorted by the
// number of things refusing it and then by address.
func mergeBans(ssh, trap []string, blocks []security.Rule, locked map[string]int64) []bannedAddress {
	rows := map[string]*bannedAddress{}

	add := func(ip string, source banSource) {
		row, ok := rows[ip]
		if !ok {
			row = &bannedAddress{IP: ip, Sources: []banSource{}}
			rows[ip] = row
		}

		row.Sources = append(row.Sources, source)
	}

	for _, ip := range trap {
		add(ip, banSource{Kind: banTrap})
	}
	for _, rule := range blocks {
		add(rule.From, banSource{Kind: banFirewall, Rule: rule.Number, Comment: rule.Comment})
	}
	for _, ip := range ssh {
		add(ip, banSource{Kind: banSSH})
	}
	for ip, until := range locked {
		add(ip, banSource{Kind: banPanel, Until: until})
	}

	list := make([]bannedAddress, 0, len(rows))
	for _, row := range rows {
		list = append(list, *row)
	}

	sort.Slice(list, func(i, j int) bool {
		if len(list[i].Sources) != len(list[j].Sources) {
			return len(list[i].Sources) > len(list[j].Sources)
		}

		return list[i].IP < list[j].IP
	})

	return list
}

// bans is the Banned IPs tab's props.
func (a *App) bans(ctx context.Context, snap *securitySnapshot) map[string]any {
	limit, window := a.loginLimits()
	locked := a.Store.LockedAddresses(ctx, limit, window)
	blocks := firewallBlocks(snap.Firewall.Rules)

	trap := []string{}
	counts := banCounts{
		SSH:      snap.Jail.CurrentlyBanned,
		SSHTotal: snap.Jail.TotalBanned,
		TrapOn:   a.sshTrapEnabled(),
		Firewall: len(blocks),
		Panel:    len(locked),
	}

	if snap.Trap != nil {
		trap = snap.Trap.Banned
		counts.Trap = snap.Trap.CurrentlyBanned
		counts.TrapTotal = snap.Trap.TotalBanned
	}

	list := mergeBans(snap.Jail.Banned, trap, blocks, locked)
	counts.Total = len(list)

	return map[string]any{
		"list":            list,
		"counts":          counts,
		"firewall_active": snap.Firewall.Active,
	}
}

// securityRelease lets an address back in from everything that refuses it:
// both jails, every ufw rule that blocks it, and the panel's lockout.
func (a *App) securityRelease(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := strings.TrimSpace(input["ip"])

	if !security.ValidAddress(address) {
		a.securityBack(w, r, "That is not an IP address or a range.", true)
		return
	}

	problems := []string{}

	if security.ValidIP(address) {
		if status := security.Status(); containsString(status.Banned, address) {
			if err := security.Unban(address); err != nil {
				problems = append(problems, err.Error())
			}
		}

		if a.sshTrapEnabled() {
			if status := security.TrapStatus(); containsString(status.Banned, address) {
				if err := security.UnbanTrap(address); err != nil {
					problems = append(problems, err.Error())
				}
			}
		}

		a.Store.ClearLoginFailures(r.Context(), address)
	}

	// Highest number first: deleting a ufw rule renumbers the ones after it.
	numbers := []int{}
	for _, rule := range firewallBlocks(security.ReadFirewall().Rules) {
		if rule.From == address {
			numbers = append(numbers, rule.Number)
		}
	}

	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))

	for _, number := range numbers {
		if err := security.DeleteRule(number); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) > 0 {
		a.securityBack(w, r, "Not everything let "+address+" go: "+strings.Join(problems, "; "), true)
		return
	}

	a.Log.Info("unbanned everywhere", "ip", address, "by", session.ClientIP(r))
	a.securityBack(w, r, address+" is no longer banned anywhere.", false)
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}
