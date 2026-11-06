package security

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Two things for sites behind Cloudflare, where a visitor's connection
// arrives from one of Cloudflare's addresses rather than their own.
//
// A firewall ban cannot stop such a visitor: the firewall sees Cloudflare.
// nginx can — with real visitor addresses on, $remote_addr is the visitor —
// so every banned address is also refused by nginx itself.
//
// And the server can refuse the web ports to everyone but Cloudflare, so the
// sites cannot be reached around it, and a sweep of every IPv4 address gets
// nothing from this one.

// ------------------------------------------------------------ nginx bans

// NginxBansPath is the file of addresses nginx refuses. In conf.d, so inside
// the http block: every site and the panel inherit it.
const NginxBansPath = "/etc/nginx/conf.d/panel-banned.conf"

// NginxBansFile renders the addresses as deny rules, or "" for none.
func NginxBansFile(ips []string) string {
	seen := map[string]bool{}
	list := []string{}

	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		// Cloudflare's own addresses never: a site behind it would refuse
		// every visitor.
		if ValidAddress(ip) && !seen[ip] && !FromCloudflare(ip) {
			seen[ip] = true
			list = append(list, ip)
		}
	}

	if len(list) == 0 {
		return ""
	}

	sort.Strings(list)

	var file strings.Builder
	file.WriteString("# Managed by Ubuntu Panel — every address banned on the Security page.\n")
	file.WriteString("# nginx refuses them itself, which is what stops a banned visitor whose\n")
	file.WriteString("# requests arrive through Cloudflare: the firewall only sees Cloudflare.\n")
	for _, ip := range list {
		fmt.Fprintf(&file, "deny %s;\n", ip)
	}

	return file.String()
}

// ApplyNginxBans writes the file when it changed and reloads nginx, putting the
// previous one back if nginx refuses it.
//
// instant is the instant file's contents (InstantBansPath). An address it
// already denies does not need nginx tested and reloaded to be added here
// too: when the only difference is such addresses, nothing is written. A
// file naming an address no longer banned is always rewritten.
func ApplyNginxBans(ips []string, instant string) error {
	if !proc.Which("nginx") && !proc.Ok("test -x /usr/sbin/nginx") {
		return nil
	}

	want := NginxBansFile(ips)
	previous, readErr := proc.ReadFile(NginxBansPath, true)
	existed := readErr == nil

	if want == "" {
		if !existed {
			return nil
		}
		proc.Run("sudo rm -f "+NginxBansPath, time.Minute)
	} else {
		if existed && (previous == want || nginxBansCovered(previous, want, instant)) {
			return nil
		}
		if err := proc.PutFile(NginxBansPath, want, true); err != nil {
			return err
		}
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		if existed {
			proc.PutFile(NginxBansPath, previous, true)
		} else {
			proc.Run("sudo rm -f "+NginxBansPath, time.Minute)
		}

		return fmt.Errorf("nginx refused the banned addresses: %s", lastLines(check.Output, 3))
	}

	if result := proc.Run("sudo systemctl reload nginx 2>&1", time.Minute); !result.OK() {
		return fmt.Errorf("nginx would not reload: %s", lastLines(result.Output, 3))
	}

	return nil
}

// nginxBansCovered is whether nginx, with the full list as it is and the
// instant file beside it, already refuses exactly what want says: nothing in
// previous that want does not name, and everything want adds already denied
// by instant. A ban the worker gave nginx a moment ago is then not worth a
// second nginx -t and reload just to be written down twice.
func nginxBansCovered(previous, want, instant string) bool {
	have, wanted, already := parseInstantBans(previous), parseInstantBans(want), parseInstantBans(instant)

	for address := range have {
		if _, ok := wanted[address]; !ok {
			return false
		}
	}

	for address := range wanted {
		if _, ok := have[address]; ok {
			continue
		}
		if _, ok := already[address]; !ok {
			return false
		}
	}

	return true
}

// ------------------------------------------------------- Cloudflare only

const edgeOnlyComment = "Cloudflare only"

// webRuleNames are the ways a rule opening the web ports to everyone has been
// written: by the panel, by ufw's nginx profiles, by hand.
var webRuleNames = []string{"80/tcp", "443/tcp", "80", "443", "80,443/tcp", "http", "https",
	"'Nginx Full'", "'Nginx HTTP'", "'Nginx HTTPS'"}

// CloudflareWorker is the address Cloudflare gives as the visitor's own — in
// CF-Connecting-IP — for a request made by a Cloudflare Worker: somebody's
// script running on Cloudflare, which anyone can deploy to scan other people's
// sites from behind Cloudflare's name. It is inside Cloudflare's ranges, but no
// visitor's connection ever arrives from it: Cloudflare's servers connect from
// their own addresses and name this one only in the header. With real visitor
// addresses on, it is what nginx sees for those scripts and nothing else, so
// it is banned like any visitor rather than trusted like Cloudflare.
const CloudflareWorker = "2a06:98c0:3600::103"

var cloudflareWorker = net.ParseIP(CloudflareWorker)

// FromCloudflare is whether an address is one of Cloudflare's own — never a
// Worker's (see CloudflareWorker).
func FromCloudflare(address string) bool {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil || ip.Equal(cloudflareWorker) {
		return false
	}

	for _, cidr := range CloudflareRanges {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return true
		}
	}

	return false
}

// openWebRule is whether a ufw rule lets everyone reach the web ports.
func openWebRule(rule Rule) bool {
	if rule.Action != "ALLOW" || !strings.HasPrefix(strings.TrimSpace(rule.From), "Anywhere") {
		return false
	}

	to := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rule.To), "(v6)"))
	if strings.HasPrefix(to, "Nginx") {
		return true
	}

	ports := strings.Split(strings.TrimSuffix(to, "/tcp"), ",")
	for _, port := range ports {
		switch strings.TrimSpace(port) {
		case "80", "443", "http", "https":
			return true
		}
	}

	return false
}

// EdgeOnlyApplied is whether the firewall is up, lets Cloudflare reach the
// web ports and lets nobody else.
func EdgeOnlyApplied() bool {
	firewall := ReadFirewall()
	if !firewall.Active {
		return false
	}

	cloudflare := 0
	for _, rule := range firewall.Rules {
		if openWebRule(rule) {
			return false
		}
		if rule.Action == "ALLOW" && FromCloudflare(strings.TrimSuffix(strings.Split(strings.TrimSpace(rule.From), "/")[0], " (v6)")) {
			cloudflare++
		}
	}

	return cloudflare >= len(CloudflareRanges)
}

func cloudflareRule(verb, cidr string) string {
	return "sudo ufw " + verb + "allow proto tcp from " + proc.Quote(cidr) + " to any port 80,443"
}

// ApplyEdgeOnly lets only Cloudflare reach ports 80 and 443, or lets everyone
// reach them again. The rules for Cloudflare go in before the open ones come
// out, so the sites are never unreachable in between.
func ApplyEdgeOnly(on bool) error {
	if !proc.Which("ufw") {
		return errors.New("this needs the firewall (ufw), which is not installed")
	}

	if on {
		if !ReadFirewall().Active {
			return errors.New("the firewall is off — turn it on (Security → Firewall) first, " +
				"or nothing would be refused")
		}

		for _, cidr := range CloudflareRanges {
			if result := proc.Run(cloudflareRule("", cidr)+" comment "+proc.Quote(edgeOnlyComment)+" 2>&1", time.Minute); !result.OK() {
				return fmt.Errorf("the firewall would not let Cloudflare in: %s", lastLines(result.Output, 2))
			}
		}

		for _, name := range webRuleNames {
			proc.Run("sudo ufw --force delete allow "+name+" >/dev/null 2>&1", time.Minute)
		}

		// Anything still open — a rule written some other way — is reported
		// rather than guessed at.
		if !EdgeOnlyApplied() {
			return errors.New("Cloudflare's rules are in, but another firewall rule still opens " +
				"80 or 443 to everyone — remove it on the Firewall tab")
		}

		return nil
	}

	for _, rule := range []string{"80/tcp", "443/tcp"} {
		if result := proc.Run("sudo ufw allow "+rule+" comment 'Web' 2>&1", time.Minute); !result.OK() {
			return fmt.Errorf("the firewall would not open %s again: %s", rule, lastLines(result.Output, 2))
		}
	}

	for _, cidr := range CloudflareRanges {
		proc.Run(cloudflareRule("--force delete ", cidr)+" >/dev/null 2>&1", time.Minute)
	}

	return nil
}

// NotBehindCloudflare are the hostnames given that resolve to an address that
// is not Cloudflare's: the ones that would stop working with the web ports
// open to Cloudflare only. A name that does not resolve is not counted.
func NotBehindCloudflare(hostnames []string) []string {
	exposed := []string{}

	for _, host := range hostnames {
		addresses, err := net.LookupHost(host)
		if err != nil || len(addresses) == 0 {
			continue
		}

		for _, address := range addresses {
			if !FromCloudflare(address) {
				exposed = append(exposed, host)
				break
			}
		}
	}

	sort.Strings(exposed)

	return exposed
}
