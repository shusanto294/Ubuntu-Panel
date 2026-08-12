package system

import (
	"context"
	"net"
	"sync"
	"time"
)

// Resolution is what a hostname currently answers with, and whether that is
// this machine.
type Resolution struct {
	Addresses []string
	Matches   bool
}

// Resolves reports whether the hostname answers with anything at all. A name
// that answers with nothing is the NXDOMAIN a browser shows as "this site
// can't be reached", whatever nginx on this machine is doing.
func (r Resolution) Resolves() bool {
	return len(r.Addresses) > 0
}

// publicResolver asks 1.1.1.1 rather than whatever the machine is configured
// with: a VPS whose resolver caches NXDOMAIN for five minutes would otherwise
// report nothing long after the record is live everywhere else.
func publicResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}

			return dialer.DialContext(ctx, network, "1.1.1.1:53")
		},
	}
}

// resolve is one query, with no waiting.
//
// A hostname behind Cloudflare's proxy resolves to Cloudflare rather than to
// this machine, so a mismatch is reported rather than treated as a failure.
func resolve(ctx context.Context, hostname, expected string) Resolution {
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	addresses, err := publicResolver().LookupHost(lookup, hostname)
	if err != nil || len(addresses) == 0 {
		return Resolution{}
	}

	for _, address := range addresses {
		if address == expected {
			return Resolution{Addresses: addresses, Matches: true}
		}
	}

	return Resolution{Addresses: addresses}
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
func WaitForHostname(ctx context.Context, hostname, expected string, timeout time.Duration) Resolution {
	deadline := time.Now().Add(timeout)

	for {
		if resolution := resolve(ctx, hostname, expected); resolution.Resolves() {
			return resolution
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

type cachedResolution struct {
	resolution Resolution
	expected   string
	asked      time.Time
}

var resolutions = struct {
	sync.Mutex
	entries map[string]cachedResolution
}{entries: map[string]cachedResolution{}}

// Lookup is the same question asked once, with the answer kept for a minute.
//
// The site page renders this on every load and reloads itself while anything
// is running, and a resolver query per render is both slow to draw and rude to
// 1.1.1.1. A minute is short enough that a record just published shows up
// while the operator is still looking at the page.
func Lookup(ctx context.Context, hostname, expected string) Resolution {
	resolutions.Lock()

	if entry, ok := resolutions.entries[hostname]; ok &&
		entry.expected == expected && time.Since(entry.asked) < time.Minute {
		resolutions.Unlock()

		return entry.resolution
	}

	resolutions.Unlock()

	resolution := resolve(ctx, hostname, expected)

	resolutions.Lock()
	resolutions.entries[hostname] = cachedResolution{
		resolution: resolution,
		expected:   expected,
		asked:      time.Now(),
	}
	resolutions.Unlock()

	return resolution
}

// ForgetResolution drops a hostname's cached answer, for when the panel has
// just published the record itself and the operator is about to ask.
func ForgetResolution(hostname string) {
	resolutions.Lock()
	delete(resolutions.entries, hostname)
	resolutions.Unlock()
}
