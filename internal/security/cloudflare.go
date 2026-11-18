package security

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// CloudflareRanges are the addresses Cloudflare's proxies connect from,
// as published at cloudflare.com/ips. A site whose DNS is proxied through
// Cloudflare is reached from these, never from the visitor: the Logs page
// names them as Cloudflare rather than as a visitor of their own.
var CloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// CloudflareWorker is the address Cloudflare gives as the visitor's own — in
// CF-Connecting-IP — for a request made by a Cloudflare Worker: somebody's
// script running on Cloudflare, which anyone can deploy to scan other people's
// sites from behind Cloudflare's name. It is inside Cloudflare's ranges, but no
// visitor's connection ever arrives from it: Cloudflare's servers connect from
// their own addresses and name this one only in the header. With real visitor
// addresses on, it is what nginx sees for those scripts and nothing else, so
// it is judged like any visitor rather than trusted like Cloudflare.
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

// RealIPPath is the nginx file that makes a site behind Cloudflare log, and
// limit, its visitors rather than Cloudflare's proxies.
const RealIPPath = "/etc/nginx/conf.d/panel-realip.conf"

// RealIPConfig is that file: for a connection from one of Cloudflare's
// addresses, and only those, nginx takes the visitor's own from the
// CF-Connecting-IP header Cloudflare sets. Anybody else sending the header is
// ignored, so it cannot be used to pose as another address.
func RealIPConfig() string {
	var b strings.Builder

	b.WriteString("# Managed by the server panel — rewritten when Cloudflare's ranges change.\n")
	b.WriteString("# Requests proxied by Cloudflare are logged with the visitor's address,\n")
	b.WriteString("# not the Cloudflare server that passed them on.\n")

	for _, cidr := range CloudflareRanges {
		b.WriteString("set_real_ip_from " + cidr + ";\n")
	}

	b.WriteString("real_ip_header CF-Connecting-IP;\n")

	return b.String()
}

// ------------------------------------------------------------ Cloudflare only

// "Accept web traffic from Cloudflare only": every site answers a connection
// that comes from one of Cloudflare's addresses and closes any other without a
// response (nginx's 444). Somebody who finds the server's IP — in an old DNS
// record, a mail header, a scan — cannot reach a site around Cloudflare's
// firewall, cache and rate limits by sending its hostname straight here.
//
// It is done in nginx rather than ufw on purpose. A firewall rule for ports 80
// and 443 cannot tell a site from a mail hostname, and mail.example.com is
// never proxied by Cloudflare — so its webmail would go dark, and certbot's
// renewal of the certificate Postfix and Dovecot use would fail ninety days
// later. In nginx the rule sits only in the sites' own server blocks (through
// the panel-guard include every site vhost has), so the mail hostnames, the
// panel's hostname and its own port, SSH and the mail ports are untouched, and
// the ACME challenge stays reachable for every hostname.
//
// The connecting address is $realip_remote_addr: the real peer, before the
// real-IP module has replaced $remote_addr with the visitor's own from
// CF-Connecting-IP. Judging $remote_addr would refuse every visitor.

// CloudflareOnlyMapPath declares, for the whole http block, whether the
// connection came from Cloudflare.
const CloudflareOnlyMapPath = "/etc/nginx/conf.d/panel-cloudflare-only.conf"

// CloudflareOnlyGuardPath is the line in each site's server blocks that acts
// on it.
const CloudflareOnlyGuardPath = "/etc/nginx/panel-guard/cloudflare-only.conf"

// CloudflareOnlyMap is the conf.d file. Loopback is let in too, so the
// machine's own requests to its sites — a health check, WordPress calling
// itself through /etc/hosts — keep working.
func CloudflareOnlyMap() string {
	var b strings.Builder

	b.WriteString("# Managed by the server panel — Security → Hardening → Cloudflare only.\n")
	b.WriteString("# 1 when the connection itself comes from Cloudflare (or this machine).\n")
	b.WriteString("geo $realip_remote_addr $panel_cloudflare_peer {\n")
	b.WriteString("    default 0;\n")
	b.WriteString("    127.0.0.0/8 1;\n")
	b.WriteString("    ::1 1;\n")

	for _, cidr := range CloudflareRanges {
		b.WriteString("    " + cidr + " 1;\n")
	}

	b.WriteString("}\n\n")
	b.WriteString("# Refuse a direct connection, except to the ACME challenge, which Let's\n")
	b.WriteString("# Encrypt reaches without Cloudflare for a hostname that is not proxied.\n")
	b.WriteString("map \"$panel_cloudflare_peer:$uri\" $panel_refuse_direct {\n")
	b.WriteString("    default 1;\n")
	b.WriteString("    \"~^1:\" 0;\n")
	b.WriteString("    \"~^0:/\\.well-known/acme-challenge/\" 0;\n")
	b.WriteString("}\n")

	return b.String()
}

// CloudflareOnlyGuard is the server-block file.
func CloudflareOnlyGuard() string {
	return "# Managed by the server panel — Security → Hardening → Cloudflare only.\n" +
		"if ($panel_refuse_direct) { return 444; }\n"
}

// CloudflareOnlyApplied is whether both files are on the machine as this build
// writes them.
func CloudflareOnlyApplied() bool {
	mapFile, err := proc.ReadFile(CloudflareOnlyMapPath, true)
	if err != nil || mapFile != CloudflareOnlyMap() {
		return false
	}

	guard, err := proc.ReadFile(CloudflareOnlyGuardPath, true)

	return err == nil && guard == CloudflareOnlyGuard()
}

// ApplyCloudflareOnly writes the two files, or removes them, and reloads
// nginx. The map goes in before the guard that uses its variable and comes out
// after it, so no moment has a configuration nginx would refuse; and if nginx
// refuses the result anyway, the files are taken out again and nothing changes.
func ApplyCloudflareOnly(on bool) error {
	if !proc.Which("nginx") {
		return errors.New("nginx is not installed")
	}

	remove := func() {
		proc.Run("sudo rm -f "+proc.Quote(CloudflareOnlyGuardPath)+" "+proc.Quote(CloudflareOnlyMapPath), time.Minute)
	}

	if on {
		if err := proc.PutFile(CloudflareOnlyMapPath, CloudflareOnlyMap(), true); err != nil {
			return err
		}

		if err := proc.PutFile(CloudflareOnlyGuardPath, CloudflareOnlyGuard(), true); err != nil {
			remove()
			return err
		}
	} else {
		remove()
	}

	if test := proc.Run("sudo nginx -t 2>&1", time.Minute); !test.OK() {
		if on {
			remove()
		}

		return fmt.Errorf("nginx refused the configuration, so nothing was changed: %s", lastLines(test.Output, 2))
	}

	if result := proc.Run("sudo systemctl reload nginx 2>&1", time.Minute); !result.OK() {
		return fmt.Errorf("nginx did not reload: %s", lastLines(result.Output, 2))
	}

	return nil
}
