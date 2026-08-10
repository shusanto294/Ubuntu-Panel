package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// SyncForSite publishes the records a site needs: its own hostname and every
// alias, all pointing where the site form said.
//
// The provider's record ids are stored on the site, so deleting it can take
// the records back down again.
func SyncForSite(ctx context.Context, st *store.Store, site *store.Site) (string, error) {
	if !site.ManageDNS || site.DNSAccountID <= 0 {
		return "", errors.New("DNS management is not enabled for this site.")
	}

	account, err := st.FindDNSAccount(ctx, site.DNSAccountID, 0)
	if err != nil {
		return "", errors.New("The DNS credential for this site no longer exists.")
	}

	client := New(account)

	zoneID, zoneName, err := client.FindZone(site.Domain)
	if err != nil {
		return "", err
	}

	recordType := site.DNSType
	if recordType == "" {
		recordType = "A"
	}

	var log strings.Builder
	var failure error

	ids := site.RecordIDs()

	for _, hostname := range site.Hostnames() {
		record := Record{
			Type:     recordType,
			Name:     Qualify(hostname, zoneName),
			Content:  site.DNSContent,
			Priority: -1,
			Proxied:  site.DNSProxied && SupportsProxy(account.Provider),
		}

		id, err := client.UpsertRecord(zoneID, zoneName, record)
		if err != nil {
			fmt.Fprintf(&log, "%s: %v\n", hostname, err)

			if failure == nil {
				failure = err
			}

			continue
		}

		if id != "" {
			ids[record.Name] = id
		}

		fmt.Fprintf(&log, "%s %s -> %s\n", record.Type, record.Name, record.Content)
	}

	if encoded, err := json.Marshal(ids); err == nil {
		st.SetSiteDNS(site.ID, zoneID, string(encoded))
	}

	return log.String(), failure
}

// RemoveForSite takes a site's records back down. Every failure is reported
// and none of them stops the rest: a site is being deleted, and a record the
// provider has already lost is not a reason to keep the site row.
func RemoveForSite(ctx context.Context, st *store.Store, site *store.Site) (string, error) {
	if site.DNSAccountID <= 0 || site.DNSZoneID == "" {
		return "No DNS records were published for this site.", nil
	}

	account, err := st.FindDNSAccount(ctx, site.DNSAccountID, 0)
	if err != nil {
		return "The DNS credential for this site no longer exists; its records were left in place.", nil
	}

	client := New(account)

	zoneID, zoneName, err := client.FindZone(site.Domain)
	if err != nil {
		return "", err
	}

	var log strings.Builder

	for name, id := range site.RecordIDs() {
		if err := client.DeleteRecord(zoneID, zoneName, id); err != nil {
			fmt.Fprintf(&log, "%s: %v\n", name, err)
			continue
		}

		fmt.Fprintf(&log, "%s removed\n", name)
	}

	return log.String(), nil
}

// SyncForMail publishes the records a mail domain needs.
//
// The A record for mail.<domain> is what the MX points at, what the
// certificate is issued for, and what a client connects to — so it has to
// exist before anything else is useful.
func SyncForMail(ctx context.Context, st *store.Store, domain *store.EmailDomain, publicIP string) (string, error) {
	if !domain.ManageDNS || domain.DNSAccountID <= 0 {
		return "", errors.New("DNS management is not enabled for this mail domain.")
	}

	account, err := st.FindDNSAccount(ctx, domain.DNSAccountID, 0)
	if err != nil {
		return "", errors.New("The DNS credential for this mail domain no longer exists.")
	}

	client := New(account)

	zoneID, zoneName, err := client.FindZone(domain.Domain)
	if err != nil {
		return "", err
	}

	if publicIP == "" {
		publicIP = "127.0.0.1"
	}

	wanted := []Record{
		{Type: "A", Name: "mail." + domain.Domain, Content: publicIP, Priority: -1},
		{Type: "MX", Name: domain.Domain, Content: "mail." + domain.Domain, Priority: 10},
		{Type: "TXT", Name: domain.Domain, Content: "v=spf1 a:mail." + domain.Domain + " ~all", Priority: -1},
		{Type: "TXT", Name: "_dmarc." + domain.Domain,
			Content: "v=DMARC1; p=none; rua=mailto:postmaster@" + domain.Domain, Priority: -1},
	}

	// DKIM, once the key exists. Before the mail server has generated it there
	// is nothing to publish, and publishing an empty selector is worse than
	// publishing nothing: receivers treat it as a hard signing failure.
	if domain.DKIMPublicKey != "" {
		selector := domain.DKIMSelector
		if selector == "" {
			selector = "mail"
		}

		wanted = append(wanted, Record{
			Type:     "TXT",
			Name:     selector + "._domainkey." + domain.Domain,
			Content:  "v=DKIM1; k=rsa; p=" + domain.DKIMPublicKey,
			Priority: -1,
		})
	}

	var log strings.Builder
	var failure error

	ids := domain.RecordIDs()

	for _, record := range wanted {
		record.Name = Qualify(record.Name, zoneName)
		record.Proxied = false // mail must never be proxied

		id, err := client.UpsertRecord(zoneID, zoneName, record)
		if err != nil {
			fmt.Fprintf(&log, "%s %s: %v\n", record.Type, record.Name, err)

			if failure == nil {
				failure = err
			}

			continue
		}

		if id != "" {
			ids[record.Type+" "+record.Name] = id
		}

		fmt.Fprintf(&log, "%s %s written\n", record.Type, record.Name)
	}

	if domain.DKIMPublicKey == "" {
		log.WriteString("DKIM key not generated yet; its record was skipped.\n")
	}

	if encoded, err := json.Marshal(ids); err == nil {
		st.SetEmailDomainDNS(domain.ID, string(encoded))
	}

	return log.String(), failure
}

// RemoveForMail takes a mail domain's records back down.
//
// The ids the panel recorded when it published them are the first pass. The
// second is by name, type and content, because a domain set up before the panel
// recorded ids — or by hand, or by an earlier panel — has records that are
// plainly the panel's and no id to find them by. Deleting a mail domain and
// leaving its MX behind means mail for it keeps arriving at a server that no
// longer accepts it.
//
// Nothing is matched loosely. A TXT at the apex is only removed when it is an
// SPF record naming this server's mail host; an MX only when it points at it.
// Somebody else's verification record at the same name is not the panel's to
// delete.
func RemoveForMail(ctx context.Context, st *store.Store, domain *store.EmailDomain) (string, error) {
	if domain.DNSAccountID <= 0 {
		return "No DNS records were published for this domain.", nil
	}

	account, err := st.FindDNSAccount(ctx, domain.DNSAccountID, 0)
	if err != nil {
		return "The DNS credential for this domain no longer exists; its records were left in place.", nil
	}

	client := New(account)

	zoneID, zoneName, err := client.FindZone(domain.Domain)
	if err != nil {
		return "", err
	}

	var log strings.Builder

	removed := map[string]bool{}

	for name, id := range domain.RecordIDs() {
		if err := client.DeleteRecord(zoneID, zoneName, id); err != nil {
			fmt.Fprintf(&log, "%s: %v\n", name, err)
			continue
		}

		removed[id] = true

		fmt.Fprintf(&log, "%s removed\n", name)
	}

	records, err := client.Records(zoneID, zoneName)
	if err != nil {
		// The stored ids are gone either way; a listing the provider would not
		// give up is not a reason to fail the deletion.
		fmt.Fprintf(&log, "Could not list the zone to check for leftovers: %v\n", err)

		return log.String(), nil
	}

	for _, record := range records {
		if removed[record.ID] || !ours(record, domain) {
			continue
		}

		if err := client.DeleteRecord(zoneID, zoneName, record.ID); err != nil {
			fmt.Fprintf(&log, "%s %s: %v\n", record.Type, record.Name, err)
			continue
		}

		fmt.Fprintf(&log, "%s %s removed\n", record.Type, record.Name)
	}

	st.SetEmailDomainDNS(domain.ID, "")

	if log.Len() == 0 {
		return "No DNS records for this domain were found at the provider.", nil
	}

	return log.String(), nil
}

// ours reports whether a record is one the panel publishes for a mail domain.
func ours(record Record, domain *store.EmailDomain) bool {
	host := "mail." + domain.Domain
	apex := domain.Domain

	selector := domain.DKIMSelector
	if selector == "" {
		selector = "mail"
	}

	switch record.Type {
	case "A", "AAAA":
		return record.Name == host

	case "MX":
		return record.Name == apex && strings.TrimRight(record.Content, ".") == host

	case "TXT":
		content := strings.Trim(record.Content, "\"")

		if record.Name == apex {
			return strings.HasPrefix(content, "v=spf1") && strings.Contains(content, host)
		}

		if record.Name == "_dmarc."+apex {
			return strings.HasPrefix(content, "v=DMARC1")
		}

		if record.Name == selector+"._domainkey."+apex {
			return strings.HasPrefix(content, "v=DKIM1")
		}
	}

	return false
}
