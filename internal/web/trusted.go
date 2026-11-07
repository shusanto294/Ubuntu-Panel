package web

import (
	"context"
	"net"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// Trusted addresses are the ones the Logs page never flags as an attacker:
//
//   - every address that signed in to the panel in the last 30 days, which is
//     the owner's phone, office and home;
//   - the server's own addresses — the panel and the sites talk to it;
//   - the private networks the server is on: Docker's bridge, where an app
//     the panel runs reaches the host's mail server and sites, and the LAN
//     behind a NAT or load balancer, which every visitor would appear to come
//     from. Never a public network: on a VPS that is a neighbour's address.
//
// Nothing is banned either way: this only keeps the owner's own traffic out
// of "What stands out".

const trustedSignInDays = 30

const (
	trustYou        = "your address"
	trustSignedIn   = "signed in to this panel"
	trustThisServer = "this server"
	trustCloudflare = "Cloudflare's proxy, which real visitors arrive through"
	trustPrivate    = "a private network this server is on"
)

// trusted is every trusted address or range, with why.
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

	return reasons
}

// trustReason is why an address is trusted, or "" when it is not: the
// viewer's own first, then an exact entry, then a private range holding it.
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

	// Every visitor to a site behind Cloudflare arrives from one of these
	// unless nginx restores the visitor's own address.
	if security.FromCloudflare(address) {
		return trustCloudflare
	}

	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}

	for candidate, reason := range reasons {
		if _, network, err := net.ParseCIDR(candidate); err == nil && network.Contains(ip) {
			return reason
		}
	}

	return ""
}

// markTrusted is a copy of a traffic report with every trusted address said
// to be so and never flagged, and the addresses' trust reasons.
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
