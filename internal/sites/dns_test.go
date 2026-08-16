package sites

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func siteForDNS() *store.Site {
	return &store.Site{
		ID:         3,
		Domain:     "test.example.com",
		Type:       "wordpress",
		ManageDNS:  true,
		DNSType:    "A",
		DNSContent: "203.0.113.10",
	}
}

// The DNS step runs second, before nginx and before the application. Failing it
// on a domain hosted at a provider nobody connected threw away the whole
// deployment — so the operator got NXDOMAIN and an unbuilt server, rather than
// NXDOMAIN and a site waiting for one record.
func TestAZoneTheCredentialDoesNotHoldDoesNotStopTheDeployment(t *testing.T) {
	err := fmt.Errorf("No zone on this Cloudflare account covers test.example.com: %w", dns.ErrNoZone)

	output, failure := dnsOutcome(siteForDNS(), "", err, "198.51.100.7")

	if failure != nil {
		t.Fatalf("a missing zone failed the step: %v", failure)
	}

	// And it says which record to add, in the same shape the site page and
	// create_site give — the operator has to be able to act on it.
	for _, want := range []string{"A", "test.example.com", "203.0.113.10"} {
		if !strings.Contains(output, want) {
			t.Errorf("the step's output does not name %q:\n%s", want, output)
		}
	}
}

// With no record content stored, the record still has to point somewhere: this
// machine.
func TestAMissingZoneFallsBackToThisServersAddress(t *testing.T) {
	site := siteForDNS()
	site.DNSContent = ""

	output, _ := dnsOutcome(site, "",
		fmt.Errorf("nothing here: %w", dns.ErrNoZone), "198.51.100.7")

	if !strings.Contains(output, "198.51.100.7") {
		t.Errorf("the suggested record does not point at this server:\n%s", output)
	}
}

// Everything else is a real failure. A token that expired or an API that
// refused the write is fixed by retrying, and a deployment that carried on
// would publish nothing and say nothing.
func TestARefusedWriteStillFailsTheStep(t *testing.T) {
	refused := errors.New("Cloudflare refused the record: 403")

	if _, failure := dnsOutcome(siteForDNS(), "", refused, "198.51.100.7"); failure == nil {
		t.Fatal("a refused write was reported as a published record")
	}
}
