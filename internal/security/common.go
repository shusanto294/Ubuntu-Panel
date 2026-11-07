// Package security is what the panel does to keep this machine safe, without
// ever watching or banning addresses: ufw for which ports answer, sshd's own
// configuration for how SSH may be used at all, the accounts that can log in,
// apt for the patches, and a few switches that harden the kernel and PHP.
//
// Each part is a program the machine already runs rather than one the panel
// reimplements. The panel writes their configuration, reads their state when
// the Security page asks, and runs nothing in the background.
package security

import (
	"net"
	"strings"
)

// ValidAddress is an IP address or a CIDR range, which is what ufw is handed.
// Validated rather than quoted: these end up on a command line.
func ValidAddress(text string) bool {
	if net.ParseIP(text) != nil {
		return true
	}

	_, _, err := net.ParseCIDR(text)

	return err == nil
}

// ValidIP is a single address.
func ValidIP(text string) bool { return net.ParseIP(text) != nil }

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}

	return strings.Join(lines, " ")
}
