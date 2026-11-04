package web

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// Several things on this machine refuse an address, each kept by the program
// that enforces it rather than copied into the panel: fail2ban's SSH jail
// (the SSH port only), the optional jails on the Protection tab (the web or
// mail ports, or every port for repeat offenders), ufw's deny rules (every
// port) and the panel's own sign-in lockout (the panel only). They stay
// separate because they block different things, but the Banned IPs tab reads
// them all into one list, one row per address, so nobody has to look in
// several places to find out why an address cannot get in.

// Ban sources, as the page names them.
const (
	banSSH      = "ssh"
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

// banCounts is the tab's tiles. SSHTotal is fail2ban's "since it started"
// count, which a restart resets.
type banCounts struct {
	Total    int `json:"total"`
	SSH      int `json:"ssh"`
	SSHTotal int `json:"ssh_total"`
	Protect  int `json:"protect"`
	Firewall int `json:"firewall"`
	Panel    int `json:"panel"`
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

// mergeBans puts the three lists together, one row per address, sorted by the
// number of things refusing it and then by address.
func mergeBans(ssh []string, protect map[string][]string, blocks []security.Rule, locked map[string]int64) []bannedAddress {
	rows := map[string]*bannedAddress{}

	add := func(ip string, source banSource) {
		row, ok := rows[ip]
		if !ok {
			row = &bannedAddress{IP: ip, Sources: []banSource{}}
			rows[ip] = row
		}

		row.Sources = append(row.Sources, source)
	}

	for kind, ips := range protect {
		for _, ip := range ips {
			add(ip, banSource{Kind: kind})
		}
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

	counts := banCounts{
		SSH:      snap.Jail.CurrentlyBanned,
		SSHTotal: snap.Jail.TotalBanned,
		Firewall: len(blocks),
		Panel:    len(locked),
	}

	// The optional jails, by the kind the tab shows; the two mail jails are
	// one kind, so an address in both is listed once for it.
	protect := map[string][]string{}
	seen := map[string]bool{}
	for jail, status := range snap.Protect {
		kind := protectJailKinds[jail]
		for _, ip := range status.Banned {
			if !seen[kind+" "+ip] {
				seen[kind+" "+ip] = true
				protect[kind] = append(protect[kind], ip)
				counts.Protect++
			}
		}
	}

	list := mergeBans(snap.Jail.Banned, protect, blocks, locked)
	counts.Total = len(list)

	return map[string]any{
		"list":            list,
		"counts":          counts,
		"firewall_active": snap.Firewall.Active,
	}
}

// securityRelease lets an address back in from everything that refuses it:
// every fail2ban jail the panel runs, every ufw rule that blocks it, and the
// panel's lockout.
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

		for jail, status := range security.ProtectStatus(a.protection()) {
			if containsString(status.Banned, address) {
				if err := security.UnbanFrom(jail, address); err != nil {
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
