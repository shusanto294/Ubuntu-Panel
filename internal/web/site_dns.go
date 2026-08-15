package web

import (
	"context"
	"fmt"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// siteResolution answers the question every other check on a site leaves open:
// can anybody actually get here?
//
// Every step of a deployment asks this machine about itself — the application
// on its port, nginx with the site's own Host header and `--resolve` — so all
// of them pass on a name that resolves to nothing. The site goes green, the
// browser says DNS_PROBE_FINISHED_NXDOMAIN, and it reads as the panel having
// built something broken.
//
// One answer, built here, for the site page and for the MCP report both. It
// carries the record that would fix it, because "does not resolve" is a
// diagnosis and `A  test.example.com  ->  1.2.3.4` is the thing to go and do —
// and because the panel suggesting a record other than the one it would have
// published itself is the panel disagreeing with itself in front of the
// operator.
func (a *App) siteResolution(ctx context.Context, site *store.Site) map[string]any {
	expected := system.PublicIP()

	content := site.RecordContent()
	if content == "" {
		content = expected
	}

	answer := map[string]any{
		"checked":  false,
		"managed":  site.ManageDNS,
		"expected": expected,
		"record": map[string]any{
			"type":    site.RecordType(),
			"name":    site.Domain,
			"content": content,
		},
	}

	// Not while it is being built: the answer is not settled until the
	// deployment's own DNS step has run, and the lookup costs a round trip on
	// every poll of a page that reloads every three seconds.
	if site.Status == "pending" || site.Status == "deploying" {
		return answer
	}

	lookup := system.Lookup(ctx, site.Domain, expected)

	addresses := lookup.Addresses
	if addresses == nil {
		addresses = []string{}
	}

	answer["checked"] = true
	answer["resolves"] = lookup.Resolves()
	answer["matches"] = lookup.Matches
	answer["addresses"] = addresses

	return answer
}

// dnsInstruction is what to tell somebody whose site the panel will not be
// publishing records for: the record, and where to put it.
//
// Said at creation rather than discovered afterwards. A site whose domain is
// hosted somewhere the panel has no credential for is an ordinary site — it
// works the moment the name points here — but nothing about the deployment
// says so, and the operator is left with a green build and a browser error.
func dnsInstruction(site *store.Site) string {
	content := site.RecordContent()
	if content == "" {
		content = system.PublicIP()
	}

	return fmt.Sprintf(
		"The panel is not publishing DNS for %s — no connected credential holds that zone. "+
			"Add this record wherever the domain's DNS is run and the site answers as soon as it "+
			"spreads; nothing needs redeploying: %s  %s  ->  %s",
		site.Domain, site.RecordType(), site.Domain, content)
}

// resolutionAdvice is the one sentence to act on for a name that resolves
// nowhere, which is a different sentence depending on whose records they are.
func resolutionAdvice(site *store.Site) string {
	if site.ManageDNS {
		return "The panel published the record itself, so either it has not spread yet — that " +
			"takes minutes — or the zone at the provider is not the one serving this name. " +
			"Check the zone, then sync_site_dns."
	}

	return dnsInstruction(site)
}
