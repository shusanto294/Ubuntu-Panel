package nameserver

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func zone() *store.DNSZone {
	return &store.DNSZone{
		ID: 1, Name: "example.com", Serial: 2026102401,
		TTL: 3600, Refresh: 3600, Retry: 600, Expire: 1209600, Minimum: 3600,
	}
}

func render(records ...*store.DNSZoneRecord) string {
	return Render(zone(), records, []string{"ns1.example.com", "ns2.example.com"},
		"hostmaster@example.com")
}

// A zone with no SOA or no NS record at its apex is one BIND refuses to load —
// not warns about, refuses — so neither is the operator's to forget. The NS
// records come from the panel's own settings rather than from the record table,
// because they have to be the names the registrar's delegation points at.
func TestEveryZoneGetsAnSOAAndItsApexNameservers(t *testing.T) {
	out := render()

	for _, wanted := range []string{
		"example.com.",
		"IN SOA ns1.example.com. hostmaster.example.com.",
		"2026102401 ; serial",
		"IN NS ns1.example.com.",
		"IN NS ns2.example.com.",
		"$TTL 3600",
	} {
		if !strings.Contains(out, wanted) {
			t.Errorf("the zone file is missing %q:\n%s", wanted, out)
		}
	}
}

// The apex NS records are written once, from the settings. A row in the record
// table saying the same thing is a duplicate BIND complains about and nothing
// needs — but an NS record for a delegated subdomain is a real delegation and
// has to survive.
func TestAnApexNameserverRowIsDroppedAndADelegationIsKept(t *testing.T) {
	out := render(
		&store.DNSZoneRecord{Type: "NS", Name: "example.com", Content: "ns1.example.com",
			Priority: -1},
		&store.DNSZoneRecord{Type: "NS", Name: "sub.example.com", Content: "ns1.elsewhere.net",
			Priority: -1},
	)

	if strings.Count(out, "IN NS ns1.example.com.") != 1 {
		t.Errorf("the apex NS record was written twice:\n%s", out)
	}

	if !strings.Contains(out, "sub.example.com.") ||
		!strings.Contains(out, "IN NS ns1.elsewhere.net.") {
		t.Errorf("a delegated subdomain's NS record was dropped:\n%s", out)
	}
}

// A DKIM public key is around 400 characters and a single character-string may
// be 255. One long string is a zone BIND will not load at all, so the value is
// split and a resolver joins it back up — which is what every DKIM verifier
// does with a multi-string TXT record.
func TestALongTXTRecordIsSplitIntoChunksBINDWillAccept(t *testing.T) {
	key := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)

	out := render(&store.DNSZoneRecord{
		Type: "TXT", Name: "mail._domainkey.example.com", Content: key, Priority: -1,
	})

	line := lineContaining(t, out, "mail._domainkey")

	if strings.Count(line, `"`) != 4 {
		t.Errorf("a 418-character TXT value was not split into two strings:\n%s", line)
	}

	for _, chunk := range strings.Split(txtValue(line), `" "`) {
		if len(chunk) > maxStringLength {
			t.Errorf("a chunk is longer than %d characters:\n%s", maxStringLength, line)
		}
	}

	// Split or not, the value a verifier reconstructs has to be the one that
	// was stored.
	if rebuilt := strings.ReplaceAll(txtValue(line), `" "`, ""); rebuilt != key {
		t.Errorf("the value did not survive being split:\n%s", rebuilt)
	}
}

// A DMARC policy is full of semicolons, and a semicolon starts a comment in a
// zone file. Quoting is what keeps the rest of the policy from being thrown
// away.
func TestATXTValueIsQuotedSoSemicolonsSurvive(t *testing.T) {
	out := render(&store.DNSZoneRecord{
		Type: "TXT", Name: "_dmarc.example.com", Priority: -1,
		Content: "v=DMARC1; p=none; rua=mailto:postmaster@example.com",
	})

	line := lineContaining(t, out, "_dmarc")

	if !strings.Contains(line, `"v=DMARC1; p=none; rua=mailto:postmaster@example.com"`) {
		t.Errorf("the DMARC policy was not quoted whole:\n%s", line)
	}
}

// A value copied out of another provider's dashboard arrives with the quotes
// still on it. Quoting it again publishes a record whose value starts and ends
// with a quotation mark, which no verifier recognises.
func TestAnAlreadyQuotedTXTValueIsNotQuotedTwice(t *testing.T) {
	out := render(&store.DNSZoneRecord{
		Type: "TXT", Name: "example.com", Content: `"v=spf1 -all"`, Priority: -1,
	})

	if !strings.Contains(out, `IN TXT "v=spf1 -all"`) {
		t.Errorf("an already-quoted value was requoted:\n%s", out)
	}
}

// MX and SRV keep the priority in its own column and the rest in the value,
// which is the split every provider's API uses. The zone file wants them in
// one line in a particular order.
func TestPriorityIsWrittenWhereTheRecordTypeWantsIt(t *testing.T) {
	out := render(
		&store.DNSZoneRecord{Type: "MX", Name: "example.com", Content: "mail.example.com",
			Priority: 10},
		&store.DNSZoneRecord{Type: "SRV", Name: "_sip._tcp.example.com",
			Content: "5 5060 sip.example.com", Priority: 20},
	)

	if !strings.Contains(out, "IN MX 10 mail.example.com.") {
		t.Errorf("the MX priority is not where BIND expects it:\n%s", out)
	}

	if !strings.Contains(out, "IN SRV 20 5 5060 sip.example.com.") {
		t.Errorf("the SRV fields are not in the right order:\n%s", out)
	}
}

// An MX with no priority is a record BIND rejects. A provider that keeps the
// priority somewhere the panel did not look sends -1, and 10 is what every
// single-mail-host domain uses.
func TestAnMXWithNoPriorityGetsADefaultRatherThanNone(t *testing.T) {
	out := render(&store.DNSZoneRecord{
		Type: "MX", Name: "example.com", Content: "mail.example.com", Priority: -1,
	})

	if !strings.Contains(out, "IN MX 10 mail.example.com.") {
		t.Errorf("an MX record without a priority was written unusable:\n%s", out)
	}
}

// Names and targets are written absolute. A relative name in a generated file
// is how a zone comes to publish www.example.com.example.com: BIND appends the
// origin to anything without a trailing dot, including a name that was already
// complete.
func TestEveryNameIsWrittenAbsolute(t *testing.T) {
	out := render(
		&store.DNSZoneRecord{Type: "A", Name: "www.example.com", Content: "203.0.113.10",
			Priority: -1},
		&store.DNSZoneRecord{Type: "CNAME", Name: "shop.example.com",
			Content: "shops.myshopify.com", Priority: -1},
	)

	if !strings.Contains(out, "www.example.com.") {
		t.Errorf("a record name was written relative:\n%s", out)
	}

	if !strings.Contains(out, "IN CNAME shops.myshopify.com.") {
		t.Errorf("a CNAME target was written relative:\n%s", out)
	}

	// An address is not a name and must not collect a dot.
	if strings.Contains(out, "203.0.113.10.") {
		t.Errorf("an IP address was given a trailing dot:\n%s", out)
	}
}

// A newline in a record value would end the record and begin another one —
// somebody else's record, written by the panel into a zone it is authoritative
// for. Nothing legitimate contains one, so the row is dropped rather than
// escaped: a missing record gets noticed and a smuggled one does not.
func TestARecordCarryingANewlineIsNotWritten(t *testing.T) {
	out := render(
		&store.DNSZoneRecord{Type: "A", Name: "ok.example.com", Content: "203.0.113.10",
			Priority: -1},
		&store.DNSZoneRecord{Type: "A", Name: "bad.example.com",
			Content: "203.0.113.10\nevil.example.com. IN A 198.51.100.1", Priority: -1},
		&store.DNSZoneRecord{Type: "A", Name: "worse.example.com\nmore", Content: "203.0.113.10",
			Priority: -1},
	)

	if !strings.Contains(out, "ok.example.com.") {
		t.Errorf("a good record was dropped:\n%s", out)
	}

	if strings.Contains(out, "evil.example.com") || strings.Contains(out, "198.51.100.1") {
		t.Errorf("a record with a newline in it was written into the zone:\n%s", out)
	}

	if strings.Contains(out, "worse.example.com") {
		t.Errorf("a name with a newline in it was written into the zone:\n%s", out)
	}
}

// A record type the renderer has no form for is left out rather than guessed
// at. Half a record is a zone BIND will not load, which takes every other name
// in the zone down with it.
func TestAnUnknownRecordTypeIsLeftOut(t *testing.T) {
	out := render(&store.DNSZoneRecord{
		Type: "HTTPS", Name: "example.com", Content: "1 . alpn=h3", Priority: -1,
	})

	if strings.Contains(out, "alpn=h3") {
		t.Errorf("a record type the renderer does not know was written anyway:\n%s", out)
	}
}

// The SOA's contact is an email address written with its @ as a dot, and a dot
// in the local part has to be escaped or nobody can tell which dot was the @.
func TestTheSOAContactIsWrittenTheWayBINDReadsIt(t *testing.T) {
	cases := map[string]string{
		"hostmaster@example.com":     "hostmaster.example.com.",
		"first.last@example.com":     `first\.last.example.com.`,
		"":                           "hostmaster.example.com.",
		"hostmaster.already.example": "hostmaster.already.example.",
	}

	for given, wanted := range cases {
		if got := soaContact(given, "example.com"); got != wanted {
			t.Errorf("soaContact(%q) = %q, wanted %q", given, got, wanted)
		}
	}
}

// A zone edited twice in a day gets two serials, and a zone edited a hundred
// times keeps counting rather than going backwards — a serial that went down
// stops a secondary transferring until somebody notices.
func TestTheSerialOnlyEverGoesUp(t *testing.T) {
	first := store.NextSerialFor(0)
	second := store.NextSerialFor(first)

	if second <= first {
		t.Errorf("the serial did not advance: %d then %d", first, second)
	}

	// Already past today's date, which is a zone that has been edited a lot.
	if next := store.NextSerialFor(2099010199); next != 2099010200 {
		t.Errorf("a serial ahead of today went backwards: got %d", next)
	}
}

// ---------------------------------------------------------------- helpers

func lineContaining(t *testing.T, out, needle string) string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}

	t.Fatalf("no line containing %q:\n%s", needle, out)

	return ""
}

// txtValue is a TXT line's value with the outermost quotes off and the chunk
// separators left in, so a test can see where the splits landed.
func txtValue(line string) string {
	_, value, _ := strings.Cut(line, "IN TXT ")

	value = strings.TrimSpace(value)

	return strings.TrimSuffix(strings.TrimPrefix(value, `"`), `"`)
}
