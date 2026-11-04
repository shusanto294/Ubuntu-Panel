package web

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// Trusted addresses are the ones the panel never treats as an attacker:
//
//   - the never-ban list, which the owner wrote;
//   - every address that signed in to the panel in the last 30 days, which is
//     the owner's phone, office and home whether or not they thought to list
//     them;
//   - the server's own addresses — the panel and the sites talk to it;
//   - the private networks the server is on: Docker's bridge, where an app
//     the panel runs reaches the host's mail server and sites, and the LAN
//     behind a NAT or load balancer, which every visitor would appear to come
//     from. Never a public network: on a VPS that is a neighbour's address.
//
// Nothing flags them, nothing offers to ban them, and none of the panel's
// jails or rate limits ever applies to them: fail2ban is given them as its
// ignore list and nginx as its exemptions. A ban by hand is refused too. The
// owner locking themselves out of their own server by one click, or by
// mistyping an SSH password three times, is the failure this exists to make
// impossible.

const trustedSignInDays = 30

const (
	trustYou         = "your address"
	trustNeverBan    = "on the never-ban list"
	trustSignedIn    = "signed in to this panel"
	trustThisServer  = "this server"
	trustCloudflare  = "Cloudflare's proxy, which real visitors arrive through"
	trustPrivate     = "a private network this server is on"
	trustedSetting   = "security_trusted_applied"
	trustedRecheckAt = 10 * time.Minute
)

// trusted is every trusted address or range, with why. Ranges come only from
// the never-ban list.
func (a *App) trusted(ctx context.Context) map[string]string {
	reasons := map[string]string{
		"127.0.0.1": trustThisServer,
		"::1":       trustThisServer,
	}

	if public := system.PublicIP(); net.ParseIP(public) != nil {
		reasons[public] = trustThisServer
	}

	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok && !network.IP.IsLinkLocalUnicast() {
				reasons[network.IP.String()] = trustThisServer

				if network.IP.IsPrivate() {
					masked := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
					reasons[masked.String()] = trustPrivate
				}
			}
		}
	}

	since := time.Now().AddDate(0, 0, -trustedSignInDays).Unix()
	for _, address := range a.Store.SignInAddresses(ctx, since) {
		if net.ParseIP(address) != nil {
			reasons[address] = trustSignedIn
		}
	}

	for _, address := range a.jailConfig().IgnoreIPs {
		if security.ValidAddress(address) {
			reasons[address] = trustNeverBan
		}
	}

	return reasons
}

// trustedList is the same as a sorted list, for fail2ban and nginx.
func (a *App) trustedList(ctx context.Context) []string {
	list := []string{}
	for address := range a.trusted(ctx) {
		list = append(list, address)
	}
	sort.Strings(list)

	return list
}

// trustReason is why an address is trusted, or "" when it is not: the
// viewer's own first, then an exact entry, then a never-ban range holding it.
func trustReason(reasons map[string]string, address, viewer string) string {
	if address == "" {
		return ""
	}

	if viewer != "" && address == viewer {
		return trustYou
	}

	if reason, ok := reasons[address]; ok {
		return reason
	}

	// Without real visitor addresses, every visitor to a site behind
	// Cloudflare arrives from one of these: banning one refuses them all.
	if security.FromCloudflare(address) {
		return trustCloudflare
	}

	ip := net.ParseIP(address)
	if ip == nil {
		// A range being banned: refused when it holds a trusted address.
		_, banned, err := net.ParseCIDR(address)
		if err != nil {
			return ""
		}
		for candidate, reason := range reasons {
			if exact := net.ParseIP(candidate); exact != nil && banned.Contains(exact) {
				return "it contains " + candidate + ", " + reason
			}
		}
		return ""
	}

	for candidate, reason := range reasons {
		if _, network, err := net.ParseCIDR(candidate); err == nil && network.Contains(ip) {
			return reason
		}
	}

	return ""
}

// refuseBan is the message a ban of a trusted address is refused with, or "".
func (a *App) refuseBan(ctx context.Context, address, viewer string) string {
	reason := trustReason(a.trusted(ctx), strings.TrimSpace(address), viewer)
	if reason == "" {
		return ""
	}

	if reason == trustNeverBan {
		return address + " is on the never-ban list, so it is never banned. Take it off the list on the " +
			"SSH protection tab first if you really mean to."
	}

	if reason == trustCloudflare {
		return address + " is one of Cloudflare's addresses. Every visitor to a site behind Cloudflare " +
			"arrives from these, so banning it would refuse them all — turn on real visitor addresses on " +
			"the Protection tab and ban the visitor instead."
	}

	return address + " is never banned — " + reason + ". The panel will not lock its owner out."
}

// markTrusted is a copy of a traffic report with every trusted address said
// to be so, never flagged, and the addresses' trust reasons for the page's
// ban buttons.
func (a *App) markTrusted(ctx context.Context, report trafficReport, viewer string) (trafficReport, map[string]string) {
	reasons := a.trusted(ctx)
	seen := map[string]string{}

	marked := report
	marked.TopIPs = make([]weblog.IPStat, len(report.TopIPs))
	copy(marked.TopIPs, report.TopIPs)

	for i := range marked.TopIPs {
		if reason := trustReason(reasons, marked.TopIPs[i].IP, viewer); reason != "" {
			marked.TopIPs[i].Trusted = reason
			marked.TopIPs[i].Danger = false
			marked.TopIPs[i].Verdict = ""
			seen[marked.TopIPs[i].IP] = reason
		}
	}
	weblog.SortIPs(marked.TopIPs)

	for _, threat := range report.Threats {
		for _, ip := range threat.TopIPs {
			if reason := trustReason(reasons, ip.Name, viewer); reason != "" {
				seen[ip.Name] = reason
			}
		}
	}

	return marked, seen
}

// trustedSet is the exact trusted addresses, for the readers that only match
// whole addresses.
func (a *App) trustedSet(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	for address := range a.trusted(ctx) {
		if net.ParseIP(address) != nil {
			set[address] = true
		}
	}

	return set
}

// syncTrusted gives fail2ban and nginx the trusted list again when it has
// changed since they were last given it — an address signed in for the first
// time, one aged out of the thirty days, the never-ban list edited.
func (a *App) syncTrusted(ctx context.Context) {
	list := strings.Join(a.trustedList(ctx), " ")
	if list == a.Store.Setting(trustedSetting, "") {
		return
	}

	if proc.Which("fail2ban-client") && proc.Exists(security.JailPath, true) {
		if err := a.applySSHProtection(nil); err != nil {
			a.Log.Warn("could not give fail2ban the trusted addresses", "error", err)
			return
		}
	} else if p := a.protection(); p.AnyJail() || p.RateLogin.Enabled || p.RateFlood.Enabled {
		if err := a.applyProtection(ctx, p); err != nil {
			a.Log.Warn("could not give the protections the trusted addresses", "error", err)
			return
		}
	}

	a.Store.SetSetting(trustedSetting, list)
}

// trustSignIn runs after a sign-in: the address is let out of anything that
// banned it, and given to fail2ban and nginx as trusted.
func (a *App) trustSignIn(address string) {
	defer func() {
		if problem := recover(); problem != nil {
			a.Log.Error("trusting a sign-in address panicked", "panic", problem)
		}
	}()

	ctx := context.Background()

	if security.ValidIP(address) && proc.Which("fail2ban-client") {
		if containsString(security.Status().Banned, address) {
			security.Unban(address)
		}
		for jail, status := range security.ProtectStatus(a.protection()) {
			if containsString(status.Banned, address) {
				security.UnbanFrom(jail, address)
			}
		}
	}

	a.syncTrusted(ctx)
}
