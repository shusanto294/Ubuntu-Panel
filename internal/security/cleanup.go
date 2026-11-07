package security

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The panel no longer bans addresses (2.21.0). fail2ban followed every log on
// the machine in Python, the panel asked it about its jails, re-read the
// sites' traffic to ban more, and rewrote and reloaded nginx to refuse what it
// banned — and on a small VPS that was the CPU graph's spikes. The machine is
// kept safe by what costs nothing while it runs instead: the firewall closes
// every port that is not meant to be public, and SSH, the panel and the
// databases are hardened from the Security page.
//
// RemoveIPBlocking takes what earlier versions put on the machine off it
// again, so every address they banned can connect, and nothing is left
// reading logs. Safe to run more than once, and on a machine that never had
// any of it.

// The files earlier versions wrote, by what reads them.
var (
	retiredFail2banFiles = []string{
		"/etc/fail2ban/jail.d/ubuntu-panel.local",
		"/etc/fail2ban/fail2ban.d/ubuntu-panel.local",
		"/etc/fail2ban/jail.d/ubuntu-panel-protect.local",
		"/etc/fail2ban/jail.d/ubuntu-panel-trap.local",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-login.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-scan.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-secret.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-flood.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-tools.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-web-waf.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-port-scan.conf",
		"/etc/fail2ban/filter.d/ubuntu-panel-ssh-trap.conf",
		"/etc/fail2ban/action.d/ubuntu-panel-nginx.conf",
	}

	// retiredNginxPaths are files and directories nginx loaded: the ban
	// lists, the rate limits, and the per-site guard (countries, the browser
	// check, the WAF). The sites' vhosts include the guard directory by a
	// glob, which matches nothing once it is empty — no vhost is touched.
	retiredNginxPaths = []string{
		"/etc/nginx/conf.d/panel-banned.conf",
		"/etc/nginx/conf.d/panel-banned-instant.conf",
		"/etc/nginx/conf.d/panel-protect.conf",
		"/etc/nginx/panel-guard/guard.conf",
		"/etc/nginx/panel-guard-data",
		"/etc/nginx/panel-waf",
	}
)

// cloudflareOnlyComment marks the rules that let only Cloudflare reach the
// web ports.
const cloudflareOnlyComment = "Cloudflare only"

// sshTrapPrefix marks the iptables rule the SSH trap logged with.
const sshTrapPrefix = "UP-SSH-TRAP "

// RemoveIPBlocking undoes every kind of address blocking an earlier version
// set up, and says what it did. nginx is tested and reloaded once, and only
// when one of its files was there to remove.
func RemoveIPBlocking() ([]string, error) {
	done := []string{}
	var problems []string

	// fail2ban: stopped and disabled, which also takes its firewall rules
	// and ipsets — every ban it held — off the machine. The package stays:
	// it costs nothing installed and not running.
	if proc.Which("fail2ban-client") || proc.Ok("systemctl cat fail2ban >/dev/null 2>&1") {
		if proc.Ok("systemctl is-active --quiet fail2ban") || proc.Ok("systemctl is-enabled --quiet fail2ban") {
			if result := proc.Run("sudo systemctl disable --now fail2ban 2>&1", 2*time.Minute); result.OK() {
				done = append(done, "stopped fail2ban and every ban it held")
			} else {
				problems = append(problems, "fail2ban would not stop: "+lastLines(result.Output, 2))
			}
		}
	}

	present := []string{}
	for _, path := range retiredFail2banFiles {
		if proc.Exists(path, true) {
			present = append(present, proc.Quote(path))
		}
	}
	if len(present) > 0 {
		proc.Run("sudo rm -f "+strings.Join(present, " "), time.Minute)
		done = append(done, "removed the panel's fail2ban jails")
	}

	// The SSH trap's logging rule, if a machine still has one.
	for _, tool := range []string{"iptables", "ip6tables"} {
		proc.Run(fmt.Sprintf(
			"sudo %[1]s -S INPUT 2>/dev/null | grep -F -- '--log-prefix \"%[2]s\"' | sed 's/^-A /-D /' | "+
				"while read -r rule; do eval \"sudo %[1]s $rule\"; done; true",
			tool, sshTrapPrefix), time.Minute)
	}

	// nginx: the ban lists and the protections it carried out.
	present = present[:0]
	for _, path := range retiredNginxPaths {
		if proc.Exists(path, true) {
			present = append(present, proc.Quote(path))
		}
	}
	if len(present) > 0 {
		proc.Run("sudo rm -rf "+strings.Join(present, " "), time.Minute)

		if proc.Which("nginx") || proc.Ok("test -x /usr/sbin/nginx") {
			check := proc.Run("sudo nginx -t 2>&1", time.Minute)
			if !check.OK() {
				problems = append(problems, "nginx's configuration does not test clean, so it was not reloaded: "+
					lastLines(check.Output, 3))
			} else if result := proc.Run("sudo systemctl reload nginx 2>&1", time.Minute); !result.OK() {
				problems = append(problems, "nginx would not reload: "+lastLines(result.Output, 2))
			}
		}
		done = append(done, "removed nginx's ban lists, rate limits, country rules and browser check")
	}

	// The firewall: addresses blocked by hand, and the web ports kept to
	// Cloudflare.
	if proc.Which("ufw") {
		if removed, err := removeFirewallBlocks(); err != nil {
			problems = append(problems, err.Error())
		} else if removed != "" {
			done = append(done, removed)
		}
	}

	if len(problems) > 0 {
		return done, fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	return done, nil
}

// removeFirewallBlocks deletes the ufw rules that refuse an address, and puts
// the web ports back for everyone where they had been kept to Cloudflare. The
// open rules go in before Cloudflare's come out, so the sites are never
// unreachable in between.
func removeFirewallBlocks() (string, error) {
	firewall := ReadFirewall()
	if !firewall.Active {
		return "", nil
	}

	blocks, cloudflare := 0, 0
	for _, rule := range firewall.Rules {
		if blocksAddress(rule) {
			blocks++
		}
		if cloudflareOnly(rule) {
			cloudflare++
		}
	}

	if blocks == 0 && cloudflare == 0 {
		return "", nil
	}

	said := []string{}

	if cloudflare > 0 {
		for _, port := range []string{"80/tcp", "443/tcp"} {
			if result := proc.Run("sudo ufw allow "+port+" comment 'Web' 2>&1", time.Minute); !result.OK() {
				return "", fmt.Errorf("the firewall would not open %s to everyone again, so the Cloudflare "+
					"rules were left: %s", port, lastLines(result.Output, 2))
			}
		}
		said = append(said, "opened ports 80 and 443 to everyone again, not only Cloudflare")

		// The rules just added moved the numbers on.
		firewall = ReadFirewall()
	}

	numbers := []int{}
	for _, rule := range firewall.Rules {
		if blocksAddress(rule) || cloudflareOnly(rule) {
			numbers = append(numbers, rule.Number)
		}
	}

	if err := deleteRules(numbers); err != nil {
		return "", err
	}

	if blocks > 0 {
		said = append(said, fmt.Sprintf("removed %d firewall %s blocking an address", blocks,
			map[bool]string{true: "rule", false: "rules"}[blocks == 1]))
	}

	return strings.Join(said, ", and "), nil
}

func cloudflareOnly(rule Rule) bool {
	return rule.Action == "ALLOW" && rule.Comment == cloudflareOnlyComment
}

// blocksAddress is whether a rule refuses one address or range, on any port:
// what Ban and Block an address wrote. A rule closing a port to everyone is
// not one.
func blocksAddress(rule Rule) bool {
	if rule.Action != "DENY" && rule.Action != "REJECT" {
		return false
	}

	from := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rule.From), "(v6)"))

	return from != "" && !strings.HasPrefix(from, "Anywhere") && ValidAddress(from)
}

// deleteRules deletes rules by number, highest first, so the numbers of the
// ones still to go do not move.
func deleteRules(numbers []int) error {
	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))

	for _, number := range numbers {
		if err := DeleteRule(number); err != nil {
			return err
		}
	}

	return nil
}
