package dns

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/nameserver"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// This file is the `panel` provider: the zone is held in the panel's own tables
// and served by BIND on this machine, so "write a record at the provider" is a
// row and a zone file rather than an HTTP request.
//
// Each of these mirrors the method of the same name in client.go, and the
// methods there call into these when the account is a local one. Everything
// built on top — FindZone, matchingRecords, Upsert, ClaimHost, the mail and
// site sync — is written against those four operations and does not know the
// difference.

// local reports whether this client is talking to this machine.
func (c *Client) local() bool { return IsLocal(c.Account.Provider) }

// localStore is the store, or an error saying it was not handed over.
//
// A Client built without one cannot serve a local account. Nothing in the panel
// does that — every call site has the store to hand — but a nil dereference
// here would be a panic in a queue worker rather than a failed step with a
// sentence attached.
func (c *Client) localStore() (*store.Store, error) {
	if c.Store == nil {
		return nil, errors.New("the panel's own nameserver was asked for a zone " +
			"without a database to read it from")
	}

	return c.Store, nil
}

// localVerify is the local provider's answer to "does this credential work".
//
// There is no credential, so the question becomes whether this machine can
// actually serve a zone: the package installed, the service up, and the
// nameserver hostnames chosen. All three have to be true before a domain
// pointed here resolves, and all three are things the operator can get wrong.
func (c *Client) localVerify() (string, error) {
	st, err := c.localStore()
	if err != nil {
		return "", err
	}

	if !nameserver.Installed() {
		return "", errors.New("the DNS server is not installed on this machine yet — " +
			"install it from the Apps page.")
	}

	if !nameserver.Running() {
		return "", errors.New("the DNS server is installed but not running.")
	}

	if !nameserver.CurrentSettings(st).Configured() {
		return "", errors.New("the nameserver hostnames have not been set, so a domain " +
			"has nothing to be pointed at.")
	}

	count := st.DNSZoneCount(context.Background())

	return fmt.Sprintf("this server is answering for %d zone(s)", count), nil
}

// localZones is every zone this machine holds for the credential's owner.
func (c *Client) localZones() ([]Zone, error) {
	st, err := c.localStore()
	if err != nil {
		return nil, err
	}

	rows, err := st.DNSZones(context.Background(), c.Account.UserID)
	if err != nil {
		return nil, err
	}

	zones := make([]Zone, 0, len(rows))

	for _, row := range rows {
		zones = append(zones, Zone{ID: strconv.FormatInt(row.ID, 10), Name: row.Name})
	}

	return zones, nil
}

// localRecords is a zone's records in the panel's own shape, which they are
// already stored in.
func (c *Client) localRecords(zoneID string) ([]Record, error) {
	st, id, err := c.localZone(zoneID)
	if err != nil {
		return nil, err
	}

	rows, err := st.DNSZoneRecords(context.Background(), id)
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0, len(rows))

	for _, row := range rows {
		records = append(records, Record{
			Type:     strings.ToUpper(row.Type),
			Name:     row.Name,
			Content:  row.Content,
			Priority: row.Priority,
			TTL:      row.TTL,
			ID:       strconv.FormatInt(row.ID, 10),
		})
	}

	return records, nil
}

// localCreate adds a record, then has the nameserver take it.
//
// The row is removed again when the zone will not load with it. Leaving it
// would be a record the panel lists and this machine does not answer with —
// two different answers to "what does this name resolve to", one of them from
// the page the operator is looking at.
func (c *Client) localCreate(zoneID string, record Record) (string, error) {
	st, id, err := c.localZone(zoneID)
	if err != nil {
		return "", err
	}

	row := &store.DNSZoneRecord{
		ZoneID:   id,
		Type:     strings.ToUpper(record.Type),
		Name:     record.Name,
		Content:  record.Content,
		Priority: record.Priority,
		TTL:      record.TTL,
	}

	recordID, err := st.CreateDNSZoneRecord(context.Background(), row)
	if err != nil {
		return "", err
	}

	if err := nameserver.ApplyZone(context.Background(), st, id); err != nil {
		if !errors.Is(err, nameserver.ErrNotInstalled) {
			st.DeleteDNSZoneRecord(context.Background(), id, recordID)
			rewrite(st, id)

			return "", err
		}
	}

	return strconv.FormatInt(recordID, 10), nil
}

// localUpdate replaces one, putting the old values back if the zone will not
// load with the new ones.
func (c *Client) localUpdate(zoneID, recordID string, record Record) error {
	st, id, err := c.localZone(zoneID)
	if err != nil {
		return err
	}

	numeric, err := strconv.ParseInt(recordID, 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not a record on this server", recordID)
	}

	previous, err := st.FindDNSZoneRecord(context.Background(), id, numeric)
	if err != nil {
		return errors.New("that record is no longer in this zone.")
	}

	row := &store.DNSZoneRecord{
		ID:       numeric,
		ZoneID:   id,
		Type:     strings.ToUpper(record.Type),
		Name:     record.Name,
		Content:  record.Content,
		Priority: record.Priority,
		TTL:      record.TTL,
	}

	if err := st.UpdateDNSZoneRecord(context.Background(), row); err != nil {
		return err
	}

	if err := nameserver.ApplyZone(context.Background(), st, id); err != nil {
		if !errors.Is(err, nameserver.ErrNotInstalled) {
			st.UpdateDNSZoneRecord(context.Background(), previous)
			rewrite(st, id)

			return err
		}
	}

	return nil
}

// rewrite puts the zone file back to what the rows now say, after a row has
// been rolled back.
//
// The failed write left a zone file on disk that BIND refused — which did no
// harm at the time, because a zone that fails its check is never reloaded and
// the server carries on serving what it had. It would do harm later: that file
// is what BIND reads on its next restart, and a zone it cannot parse is a zone
// it does not serve at all. Any error here is swallowed on purpose; the caller
// is already returning the one that matters, and two error messages about one
// mistyped record explain it less well than one.
func rewrite(st *store.Store, zoneID int64) {
	nameserver.ApplyZone(context.Background(), st, zoneID)
}

// localDelete removes one. No rollback: a zone that loaded with a record loads
// without it, so the only way this fails is the nameserver being unreachable —
// and then the row is already gone and the next change publishes the rest.
func (c *Client) localDelete(zoneID, recordID string) error {
	st, id, err := c.localZone(zoneID)
	if err != nil {
		return err
	}

	numeric, err := strconv.ParseInt(recordID, 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not a record on this server", recordID)
	}

	if err := st.DeleteDNSZoneRecord(context.Background(), id, numeric); err != nil {
		return err
	}

	if err := nameserver.ApplyZone(context.Background(), st, id); err != nil &&
		!errors.Is(err, nameserver.ErrNotInstalled) {
		return err
	}

	return nil
}

// localZone resolves a zone id handed in as a string, and checks it is a zone
// that exists.
func (c *Client) localZone(zoneID string) (*store.Store, int64, error) {
	st, err := c.localStore()
	if err != nil {
		return nil, 0, err
	}

	id, err := strconv.ParseInt(strings.TrimSpace(zoneID), 10, 64)
	if err != nil || id <= 0 {
		return nil, 0, fmt.Errorf("%q is not a zone hosted on this server", zoneID)
	}

	// Against the credential's owner, so a zone id typed into a request cannot
	// reach somebody else's zone. A credential with no owner — which is what a
	// queue worker loads — passes zero, and FindDNSZone reads that as "any",
	// which is what a deployment needs.
	if _, err := st.FindDNSZone(context.Background(), id, c.Account.UserID); err != nil {
		return nil, 0, errors.New("that zone is no longer hosted on this server.")
	}

	return st, id, nil
}
