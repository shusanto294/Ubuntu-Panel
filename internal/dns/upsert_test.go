package dns

import (
	"strings"
	"testing"
)

// Writing a site's A record when the zone already has one is an update, not a
// second record. Two A records at one name answer alternately, so half the
// visitors reach the server the site has just moved off — which looks like a
// site that is up and down at the same time.
func TestAnAddressRecordAtTheSameNameIsTheSameRecord(t *testing.T) {
	wanted := Record{Type: "A", Name: "test.example.com", Content: "203.0.113.10"}
	old := Record{Type: "A", Name: "test.example.com.", Content: "198.51.100.1"}

	if !replaces(old, wanted) {
		t.Error("an existing A record at the name was not recognised")
	}

	// A different name, and a different type at the same name, are other
	// records. The panel is not the only thing writing in this zone.
	if replaces(Record{Type: "A", Name: "www.example.com"}, wanted) {
		t.Error("a record at another name was treated as this one")
	}

	if replaces(Record{Type: "MX", Name: "test.example.com"}, wanted) {
		t.Error("a record of another type was treated as this one")
	}
}

// The apex carries the SPF record, the DMARC record and every verification
// string Google, Microsoft and Atlassian ever asked for — all of them TXT at
// the same name. Matching on type and name alone put the panel's SPF over
// whichever of those the provider happened to list first, and the domain
// stopped verifying for a reason nothing in the panel mentioned.
func TestATXTRecordIsOnlyReplacedByOneOfItsOwnKind(t *testing.T) {
	spf := Record{Type: "TXT", Name: "example.com", Content: "v=spf1 a:mail.example.com ~all"}

	cases := []struct {
		what    string
		content string
		same    bool
	}{
		{"an older SPF record", "v=spf1 include:_spf.google.com ~all", true},
		{"a quoted SPF record", "\"V=SPF1 -all\"", true},
		{"a Google verification string", "google-site-verification=abc123", false},
		{"a DMARC policy", "v=DMARC1; p=none", false},
		{"a DKIM key", "v=DKIM1; k=rsa; p=MIGf", false},
	}

	for _, test := range cases {
		got := replaces(Record{Type: "TXT", Name: "example.com", Content: test.content}, spf)

		if got != test.same {
			t.Errorf("%s: replaces = %v, want %v", test.what, got, test.same)
		}
	}
}

// DKIM and DMARC live at names of their own, so they are found the same way
// whatever else is at the apex.
func TestTheMailPoliciesAreEachTheirOwnKind(t *testing.T) {
	cases := []struct{ content, kind string }{
		{"v=spf1 -all", "spf"},
		{"v=DMARC1; p=quarantine", "dmarc"},
		{"v=DKIM1; k=rsa; p=AAAA", "dkim"},
		{"MS=ms12345", "txt"},
		{"", "txt"},
	}

	for _, test := range cases {
		if got := txtKind(test.content); got != test.kind {
			t.Errorf("txtKind(%q) = %q, want %q", test.content, got, test.kind)
		}
	}
}

// Replacing a record is the panel changing something somebody else put there.
// The old value in the deployment log is the only copy of it left, so the line
// has to carry it.
func TestTheConsoleSaysWhatWasReplaced(t *testing.T) {
	note := changed(Upsert{Previous: "198.51.100.1", Removed: 2}, 1)

	for _, want := range []string{"198.51.100.1", "2 duplicates", "1 conflicting record"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q: %s", want, note)
		}
	}

	// A record that was simply added has nothing to confess.
	if note := changed(Upsert{Created: true}, 0); note != "" {
		t.Errorf("a new record was annotated with %q", note)
	}
}
