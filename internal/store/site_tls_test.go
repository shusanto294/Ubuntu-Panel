package store

import "testing"

// A site whose TLS ends at its DNS provider has no certificate on this machine
// and is still an https address. The URL is what goes into a link, a WordPress
// option and an OAuth callback, so it has to be the visitor's scheme rather
// than the origin's.
func TestASiteBehindAProxysCertificateIsStillHTTPS(t *testing.T) {
	site := &Site{Domain: "example.com", TLSMode: "edge"}

	if !site.EdgeTLS() {
		t.Fatal("the site does not report edge TLS")
	}

	if site.URL() != "https://example.com" {
		t.Errorf("the URL is %s", site.URL())
	}

	// And `ssl` keeps its meaning: a certificate on this machine.
	if site.SSL {
		t.Error("edge TLS set the origin's own SSL flag")
	}
}

func TestASiteWithNothingInFrontIsHTTPUntilItsCertificateIsIssued(t *testing.T) {
	site := &Site{Domain: "example.com"}

	if site.URL() != "http://example.com" {
		t.Errorf("the URL is %s", site.URL())
	}

	site.SSL = true

	if site.URL() != "https://example.com" {
		t.Errorf("with a certificate the URL is %s", site.URL())
	}

	if site.TLSModeOrDefault() != "letsencrypt" {
		t.Errorf("the default TLS mode is %q", site.TLSModeOrDefault())
	}
}
