package security

import (
	"net"
	"strings"
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
