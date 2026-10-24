package store

import (
	"context"
	"strings"
	"time"
)

// DNSZone is one zone this machine answers for authoritatively.
//
// The rows here are the record of truth, and the zone files under /etc/bind are
// generated from them — the other way round from every provider in the dns
// package, where the provider is the truth and the panel stores nothing. A
// nameserver has to be told what to say by somebody, and here that somebody is
// this table.
type DNSZone struct {
	ID      int64
	UserID  int64
	Name    string
	Serial  int64
	TTL     int
	Refresh int
	Retry   int
	Expire  int
	Minimum int

	// Records is how many rows the zone holds, filled in by the listings.
	Records   int
	UpdatedAt string
}

// Zone defaults. A one-hour TTL is short enough that a record changed in the
// panel is live within the hour and long enough that a busy name is not
// re-asked every few seconds; the rest are the values a secondary would use if
// one were ever added, and are what every registry's zone checker expects to
// see.
const (
	DefaultZoneTTL     = 3600
	DefaultZoneRefresh = 3600
	DefaultZoneRetry   = 600
	DefaultZoneExpire  = 1209600
	DefaultZoneMinimum = 3600
)

// DNSZoneRecord is one record in a hosted zone, in the same shape the dns
// package's Record uses: the name is fully qualified without a trailing dot, a
// priority of -1 means "not applicable", and a TTL of 0 means "use the zone's".
type DNSZoneRecord struct {
	ID       int64
	ZoneID   int64
	Type     string
	Name     string
	Content  string
	Priority int
	TTL      int
}

const dnsZoneColumns = `id, user_id, name, serial, ttl, refresh, retry, expire, minimum,
	updated_at`

func (s *Store) scanDNSZone(row siteScanner) (*DNSZone, error) {
	zone := &DNSZone{}

	var userID nullInt
	var updatedAt text

	err := row.Scan(&zone.ID, &userID, &zone.Name, &zone.Serial, &zone.TTL, &zone.Refresh,
		&zone.Retry, &zone.Expire, &zone.Minimum, &updatedAt)
	if err != nil {
		return nil, notFound(err)
	}

	zone.UserID = userID.Int64
	zone.UpdatedAt = updatedAt.String()

	return zone, nil
}

// FindDNSZone loads a zone by id. A user id of zero skips the ownership check,
// which is what the nameserver's own configuration writer needs — it has to see
// every zone on the machine, not one person's.
func (s *Store) FindDNSZone(ctx context.Context, id, userID int64) (*DNSZone, error) {
	if userID > 0 {
		return s.scanDNSZone(s.db.QueryRowContext(ctxOrBackground(ctx),
			`SELECT `+dnsZoneColumns+` FROM dns_zones WHERE id = ? AND user_id = ?`, id, userID))
	}

	return s.scanDNSZone(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+dnsZoneColumns+` FROM dns_zones WHERE id = ?`, id))
}

// FindDNSZoneByName loads a zone by its name, which is unique across the
// machine: two people cannot both host example.com here, because there is one
// nameserver and it can only answer one way.
func (s *Store) FindDNSZoneByName(ctx context.Context, name string) (*DNSZone, error) {
	return s.scanDNSZone(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+dnsZoneColumns+` FROM dns_zones WHERE name = ?`, NormaliseZoneName(name)))
}

// DNSZones lists the zones, with how many records each holds. A user id of zero
// lists every zone on the machine.
func (s *Store) DNSZones(ctx context.Context, userID int64) ([]*DNSZone, error) {
	query := `SELECT ` + dnsZoneColumns + `,
	                 (SELECT count(*) FROM dns_zone_records r WHERE r.zone_id = dns_zones.id)
	            FROM dns_zones`
	args := []any{}

	if userID > 0 {
		query += ` WHERE user_id = ?`
		args = append(args, userID)
	}

	query += ` ORDER BY name`

	rows, err := s.db.QueryContext(ctxOrBackground(ctx), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var zones []*DNSZone

	for rows.Next() {
		zone := &DNSZone{}

		var zoneUserID nullInt
		var updatedAt text

		if err := rows.Scan(&zone.ID, &zoneUserID, &zone.Name, &zone.Serial, &zone.TTL,
			&zone.Refresh, &zone.Retry, &zone.Expire, &zone.Minimum, &updatedAt,
			&zone.Records); err != nil {
			return nil, err
		}

		zone.UserID = zoneUserID.Int64
		zone.UpdatedAt = updatedAt.String()

		zones = append(zones, zone)
	}

	return zones, rows.Err()
}

// DNSZoneCount is what the pages check before offering the zone editor.
func (s *Store) DNSZoneCount(ctx context.Context) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT count(*) FROM dns_zones`).Scan(&count)

	return count
}

// CreateDNSZone adds a zone with the standard timers.
func (s *Store) CreateDNSZone(ctx context.Context, userID int64, name string) (int64, error) {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO dns_zones (user_id, name, serial, ttl, refresh, retry, expire, minimum)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, NormaliseZoneName(name), NextSerialFor(0),
		DefaultZoneTTL, DefaultZoneRefresh, DefaultZoneRetry, DefaultZoneExpire,
		DefaultZoneMinimum)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// DeleteDNSZone removes a zone and everything in it. The nameserver stops
// answering for the name as soon as the configuration is written again.
func (s *Store) DeleteDNSZone(ctx context.Context, id, userID int64) error {
	if userID > 0 {
		if _, err := s.FindDNSZone(ctx, id, userID); err != nil {
			return err
		}
	}

	s.db.ExecContext(ctxOrBackground(ctx), `DELETE FROM dns_zone_records WHERE zone_id = ?`, id)

	_, err := s.db.ExecContext(ctxOrBackground(ctx), `DELETE FROM dns_zones WHERE id = ?`, id)

	return err
}

// BumpDNSZoneSerial moves a zone's serial on, which is what tells anything
// holding a copy that the zone changed.
//
// The serial is date-based — YYYYMMDDnn, the convention every registry's zone
// checker and every secondary expects — and never allowed to go backwards: a
// zone edited more than ninety-nine times in a day carries on counting past
// the date rather than wrapping, because a serial that went down would stop
// secondaries transferring until somebody noticed.
func (s *Store) BumpDNSZoneSerial(ctx context.Context, id int64) int64 {
	var current int64

	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT serial FROM dns_zones WHERE id = ?`,
		id).Scan(&current)

	serial := NextSerialFor(current)

	s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE dns_zones SET serial = ?, updated_at = datetime('now') WHERE id = ?`, serial, id)

	return serial
}

// NextSerialFor is today's date with a counter, or one past the current value
// when that is already higher. Exported because it is the rule, and a rule
// worth a test of its own.
func NextSerialFor(current int64) int64 {
	today := time.Now().UTC()

	base := int64(today.Year())*1000000 + int64(today.Month())*10000 + int64(today.Day())*100

	if current >= base {
		return current + 1
	}

	return base + 1
}

// ---------------------------------------------------------------- records

const dnsZoneRecordColumns = `id, zone_id, type, name, content, priority, ttl`

// DNSZoneRecords lists a zone's records, sorted the way a zone file reads.
func (s *Store) DNSZoneRecords(ctx context.Context, zoneID int64) ([]*DNSZoneRecord, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+dnsZoneRecordColumns+` FROM dns_zone_records WHERE zone_id = ?
		  ORDER BY name, type, id`, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*DNSZoneRecord

	for rows.Next() {
		record := &DNSZoneRecord{}

		if err := rows.Scan(&record.ID, &record.ZoneID, &record.Type, &record.Name,
			&record.Content, &record.Priority, &record.TTL); err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	return records, rows.Err()
}

// FindDNSZoneRecord loads one record, checking it belongs to the zone named —
// so a record id from one zone cannot be used to edit another's.
func (s *Store) FindDNSZoneRecord(ctx context.Context, zoneID, id int64) (*DNSZoneRecord, error) {
	record := &DNSZoneRecord{}

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+dnsZoneRecordColumns+` FROM dns_zone_records WHERE id = ? AND zone_id = ?`,
		id, zoneID).Scan(&record.ID, &record.ZoneID, &record.Type, &record.Name,
		&record.Content, &record.Priority, &record.TTL)
	if err != nil {
		return nil, notFound(err)
	}

	return record, nil
}

// CreateDNSZoneRecord adds a record and returns its id.
func (s *Store) CreateDNSZoneRecord(ctx context.Context, record *DNSZoneRecord) (int64, error) {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO dns_zone_records (zone_id, type, name, content, priority, ttl)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		record.ZoneID, strings.ToUpper(record.Type), NormaliseZoneName(record.Name),
		record.Content, record.Priority, record.TTL)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// UpdateDNSZoneRecord replaces one in place, keeping its id.
func (s *Store) UpdateDNSZoneRecord(ctx context.Context, record *DNSZoneRecord) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE dns_zone_records SET type = ?, name = ?, content = ?, priority = ?, ttl = ?,
		        updated_at = datetime('now')
		  WHERE id = ? AND zone_id = ?`,
		strings.ToUpper(record.Type), NormaliseZoneName(record.Name), record.Content,
		record.Priority, record.TTL, record.ID, record.ZoneID)

	return err
}

// DeleteDNSZoneRecord removes one.
func (s *Store) DeleteDNSZoneRecord(ctx context.Context, zoneID, id int64) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`DELETE FROM dns_zone_records WHERE id = ? AND zone_id = ?`, id, zoneID)

	return err
}

// NormaliseZoneName is the one spelling a name is stored under: lower case, no
// trailing dot, no surrounding space.
//
// Everything compares names as strings — a zone against its records, a record
// against the one it replaces — so `WWW.Example.COM.` and `www.example.com`
// being two different strings would be two different records answering for the
// same name.
func NormaliseZoneName(name string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(name), "."))
}
