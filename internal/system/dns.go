package system

import (
	"context"
	"net"
	"time"
)

// Resolution is what a hostname currently answers with, and whether that is
// this machine.
type Resolution struct {
	Addresses []string
	Matches   bool
}

// WaitForHostname waits until a hostname resolves, and reports what it resolved
// to.
//
// Certbot is the reason this exists. A record published seconds ago has not
// reached the resolver Let's Encrypt will ask, and issuing against it fails
// with an error about the challenge rather than about DNS — so the operator is
// told the certificate could not be issued for a hostname that was correct and
// simply young. Waiting here turns "no certificate, no idea why" into a
// certificate a minute later.
//
// A hostname behind Cloudflare's proxy resolves to Cloudflare rather than to
// this machine, and HTTP-01 still works because the proxy passes port 80
// through. So a mismatch is reported, not refused.
func WaitForHostname(ctx context.Context, hostname, expected string, timeout time.Duration) Resolution {
	// Ask a public resolver rather than whatever the machine is configured
	// with: a VPS whose resolver caches NXDOMAIN for five minutes would
	// otherwise report nothing long after the record is live everywhere else.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}

			return dialer.DialContext(ctx, network, "1.1.1.1:53")
		},
	}

	deadline := time.Now().Add(timeout)

	for {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		addresses, err := resolver.LookupHost(lookup, hostname)
		cancel()

		if err == nil && len(addresses) > 0 {
			for _, address := range addresses {
				if address == expected {
					return Resolution{Addresses: addresses, Matches: true}
				}
			}

			return Resolution{Addresses: addresses}
		}

		if time.Now().After(deadline) || ctx.Err() != nil {
			return Resolution{}
		}

		select {
		case <-ctx.Done():
			return Resolution{}
		case <-time.After(5 * time.Second):
		}
	}
}
