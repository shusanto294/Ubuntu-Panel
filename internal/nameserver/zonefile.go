package nameserver

import (
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// maxStringLength is the longest a single character-string may be in a DNS
// record — 255 bytes, because the length is one byte on the wire.
//
// It is the reason TXT records are written as several quoted strings rather
// than one: a DKIM public key is around 400 characters, and a zone file with
// it on one line is a zone BIND refuses to load at all. Split into chunks it
// loads, and a resolver concatenates them back into the one value every DKIM
// verifier reads.
const maxStringLength = 255

// Render is the zone file for one zone.
//
// Every name is written fully qualified with a trailing dot rather than
// relative to $ORIGIN. Relative names in a generated file are the classic way
// to publish `www.example.com.example.com`: one missing dot and BIND silently
// appends the origin to a name that was already complete.
func Render(zone *store.DNSZone, records []*store.DNSZoneRecord,
	nameservers []string, hostmaster string) string {
	var out strings.Builder

	apex := store.NormaliseZoneName(zone.Name)

	primary := apex
	if len(nameservers) > 0 {
		primary = store.NormaliseZoneName(nameservers[0])
	}

	fmt.Fprintf(&out, "; %s — written by Ubuntu Panel.\n", apex)
	out.WriteString(";\n")
	out.WriteString("; Generated from the panel's own records. Anything added here by hand is\n")
	out.WriteString("; lost the next time a record in this zone changes.\n")
	fmt.Fprintf(&out, "$TTL %d\n", orDefault(zone.TTL, store.DefaultZoneTTL))

	fmt.Fprintf(&out, "%-30s IN SOA %s %s (\n", dotted(apex), dotted(primary),
		soaContact(hostmaster, apex))
	fmt.Fprintf(&out, "%38d ; serial\n", zone.Serial)
	fmt.Fprintf(&out, "%38d ; refresh\n", orDefault(zone.Refresh, store.DefaultZoneRefresh))
	fmt.Fprintf(&out, "%38d ; retry\n", orDefault(zone.Retry, store.DefaultZoneRetry))
	fmt.Fprintf(&out, "%38d ; expire\n", orDefault(zone.Expire, store.DefaultZoneExpire))
	fmt.Fprintf(&out, "%38d ) ; minimum\n", orDefault(zone.Minimum, store.DefaultZoneMinimum))
	out.WriteString("\n")

	// The apex NS records, always and from the panel's own configuration.
	//
	// A zone with no NS record at its apex is one BIND refuses to load, so
	// these are not the operator's to forget. They are also what a resolver
	// coming in from the TLD is told to come back to, which has to be the
	// names the registrar's delegation points at and not whatever somebody
	// typed into the record table.
	for _, name := range nameservers {
		if name = store.NormaliseZoneName(name); name != "" {
			fmt.Fprintf(&out, "%-30s IN NS %s\n", dotted(apex), dotted(name))
		}
	}

	out.WriteString("\n")

	for _, record := range records {
		// An apex NS is the delegation above, written once already. A second
		// copy is a duplicate record BIND warns about and nothing needs.
		if strings.EqualFold(record.Type, "NS") &&
			store.NormaliseZoneName(record.Name) == apex {
			continue
		}

		if line := renderRecord(record); line != "" {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}

	return out.String()
}

// renderRecord is one line of the zone file, or empty for a record that cannot
// safely be written.
func renderRecord(record *store.DNSZoneRecord) string {
	name := store.NormaliseZoneName(record.Name)
	kind := strings.ToUpper(strings.TrimSpace(record.Type))
	content := strings.TrimSpace(record.Content)

	// A name or value carrying a newline would end the record and start
	// another one — somebody else's record, in somebody else's zone, written
	// by the panel. Nothing legitimate contains one, so the row is skipped
	// rather than escaped: a dropped record is noticed, a smuggled one is not.
	if name == "" || kind == "" || content == "" ||
		hasControlCharacter(name) || hasControlCharacter(content) {
		return ""
	}

	ttl := ""
	if record.TTL > 0 {
		ttl = fmt.Sprintf(" %d", record.TTL)
	}

	prefix := fmt.Sprintf("%-30s%s IN %s ", dotted(name), ttl, kind)

	switch kind {
	case "A", "AAAA":
		// Addresses, written as they are. named-checkzone is what rejects one
		// that is not an address.
		return prefix + content

	case "CNAME", "NS", "PTR", "DNAME":
		return prefix + dotted(content)

	case "MX":
		priority := record.Priority
		if priority < 0 {
			priority = 10
		}

		return fmt.Sprintf("%s%d %s", prefix, priority, dotted(content))

	case "SRV":
		// weight, port and target in the value; priority in its own column,
		// the same split every provider's API uses.
		fields := strings.Fields(content)
		if len(fields) != 3 {
			return ""
		}

		priority := record.Priority
		if priority < 0 {
			priority = 0
		}

		return fmt.Sprintf("%s%d %s %s %s", prefix, priority, fields[0], fields[1],
			dotted(fields[2]))

	case "TXT":
		return prefix + quoteStrings(content)

	case "CAA":
		// `0 issue "letsencrypt.org"` — flags, tag and an already-quoted
		// value, which is the form everybody copies from the CA's own
		// instructions.
		return prefix + content

	case "SPF":
		return prefix + quoteStrings(content)
	}

	return ""
}

// quoteStrings writes a TXT value as one or more quoted character-strings,
// split at the 255-byte limit.
func quoteStrings(content string) string {
	// Already-quoted input, which is what a value copied out of another
	// provider's dashboard looks like. The quotes are the zone file's, not the
	// value's, so they come off before it is measured and put back on.
	content = strings.TrimSpace(content)

	if len(content) >= 2 && strings.HasPrefix(content, `"`) && strings.HasSuffix(content, `"`) {
		content = content[1 : len(content)-1]
	}

	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(content)

	var parts []string

	for len(escaped) > maxStringLength {
		cut := maxStringLength

		// Never split on a backslash: half an escape sequence is not a
		// character-string, and BIND would read the next chunk's first
		// character as escaped.
		for cut > 1 && escaped[cut-1] == '\\' {
			cut--
		}

		parts = append(parts, `"`+escaped[:cut]+`"`)
		escaped = escaped[cut:]
	}

	parts = append(parts, `"`+escaped+`"`)

	return strings.Join(parts, " ")
}

// soaContact is the zone's responsible mailbox in the form SOA wants it: an
// address with its `@` written as a dot, fully qualified.
func soaContact(hostmaster, apex string) string {
	hostmaster = strings.TrimSpace(hostmaster)

	if hostmaster == "" {
		hostmaster = "hostmaster@" + apex
	}

	if local, domain, found := strings.Cut(hostmaster, "@"); found {
		// A dot in the local part has to be escaped, or the reader cannot tell
		// which dot was the `@`.
		return dotted(strings.ReplaceAll(local, ".", `\.`) + "." + domain)
	}

	return dotted(hostmaster)
}

// dotted makes a name absolute. A zone file's names are relative to the origin
// unless they end in a dot, and every name the panel writes is already
// complete.
func dotted(name string) string {
	name = strings.TrimSpace(name)

	if name == "" {
		return "."
	}

	if strings.HasSuffix(name, ".") {
		return name
	}

	return name + "."
}

// hasControlCharacter reports whether text holds a byte that would end the
// record it is written into.
func hasControlCharacter(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] == 0x7f {
			return true
		}
	}

	return false
}

func orDefault(value, fallback int) int {
	if value > 0 {
		return value
	}

	return fallback
}
