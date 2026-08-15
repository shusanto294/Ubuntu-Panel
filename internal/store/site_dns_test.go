package store

import "testing"

// The record a site needs is quoted in three places — the site page, the
// deployment console and create_site's answer — and a panel that suggests a
// record other than the one it would have published itself is a panel arguing
// with itself in front of the operator. So there is one answer, here.
func TestASitesRecordIsTheOneThePanelWouldPublish(t *testing.T) {
	site := &Site{Domain: "test.example.com", DNSType: "cname", DNSContent: " host.example.net "}

	if site.RecordType() != "CNAME" {
		t.Errorf("the record type is %q", site.RecordType())
	}

	if site.RecordContent() != "host.example.net" {
		t.Errorf("the record content is %q", site.RecordContent())
	}
}

// A site created before the panel stored either, or with the fields left
// empty: an A record, and content the caller fills in from this machine's
// public address.
func TestASiteWithNoRecordStoredStillAsksForAnARecord(t *testing.T) {
	site := &Site{Domain: "test.example.com"}

	if site.RecordType() != "A" {
		t.Errorf("the default record type is %q", site.RecordType())
	}

	if site.RecordContent() != "" {
		t.Errorf("the record content is %q, not empty", site.RecordContent())
	}
}
