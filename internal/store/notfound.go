package store

import (
	"context"
	"time"
)

// NotFoundBlock is one address the panel challenged or blocked at Cloudflare
// for asking for pages that do not exist.
type NotFoundBlock struct {
	ID        int64  `json:"id"`
	IP        string `json:"ip"`
	Host      string `json:"host"`
	Path      string `json:"path"`
	AccountID int64  `json:"account_id"`
	ZoneID    string `json:"zone_id"`
	ZoneName  string `json:"zone_name"`
	RuleID    string `json:"rule_id"`
	Mode      string `json:"mode"`
	Hits      int    `json:"hits"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`

	// Zones is how many domains the address has a rule in, on the grouped
	// list the Security page shows. Not a column.
	Zones int `json:"zones"`
}

const notFoundColumns = `id, ip, host, path, account_id, zone_id, zone_name, rule_id, mode, hits,
	created_at, expires_at`

func (s *Store) notFoundBlocks(ctx context.Context, query string, args ...any) ([]*NotFoundBlock, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx), `SELECT `+notFoundColumns+` FROM notfound_blocks `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	blocks := []*NotFoundBlock{}

	for rows.Next() {
		b := &NotFoundBlock{}
		if err := rows.Scan(&b.ID, &b.IP, &b.Host, &b.Path, &b.AccountID, &b.ZoneID, &b.ZoneName,
			&b.RuleID, &b.Mode, &b.Hits, &b.CreatedAt, &b.ExpiresAt); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	return blocks, rows.Err()
}

// AddNotFoundBlock records a rule the panel made.
func (s *Store) AddNotFoundBlock(ctx context.Context, b *NotFoundBlock) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO notfound_blocks (ip, host, path, account_id, zone_id, zone_name, rule_id, mode, hits,
		                              created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.IP, b.Host, b.Path, b.AccountID, b.ZoneID, b.ZoneName, b.RuleID, b.Mode, b.Hits,
		b.CreatedAt, b.ExpiresAt)

	return err
}

// NotFoundBlocked is whether an address already has a live rule in a zone.
func (s *Store) NotFoundBlocked(ctx context.Context, ip, zoneID string) bool {
	var n int
	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM notfound_blocks WHERE ip = ? AND zone_id = ?`, ip, zoneID).Scan(&n)

	return n > 0
}

// ActiveNotFoundBlocks are the live rules, newest first.
func (s *Store) ActiveNotFoundBlocks(ctx context.Context, limit int) ([]*NotFoundBlock, error) {
	return s.notFoundBlocks(ctx, `ORDER BY created_at DESC LIMIT ?`, limit)
}

// NotFoundAddresses are the live rules one row per address, newest first,
// with how many domains each is in. An address is blocked on every domain at
// once, so a row per zone would list the same visitor twenty times.
func (s *Store) NotFoundAddresses(ctx context.Context, limit int) ([]*NotFoundBlock, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT min(id), ip, host, path, mode, hits, max(created_at), max(expires_at), count(*)
		   FROM notfound_blocks GROUP BY ip ORDER BY max(created_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	blocks := []*NotFoundBlock{}

	for rows.Next() {
		b := &NotFoundBlock{}
		if err := rows.Scan(&b.ID, &b.IP, &b.Host, &b.Path, &b.Mode, &b.Hits, &b.CreatedAt, &b.ExpiresAt,
			&b.Zones); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	return blocks, rows.Err()
}

// NotFoundBlocksFor is every rule for one address, in whichever zones.
func (s *Store) NotFoundBlocksFor(ctx context.Context, ip string) ([]*NotFoundBlock, error) {
	return s.notFoundBlocks(ctx, `WHERE ip = ? ORDER BY id`, ip)
}

// CountNotFoundBlocks is how many addresses have live rules.
func (s *Store) CountNotFoundBlocks(ctx context.Context) int {
	var n int
	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT count(DISTINCT ip) FROM notfound_blocks`).Scan(&n)

	return n
}

// ExpiredNotFoundBlocks are the rules due to be taken away. A rule with no
// expiry (0) is kept until somebody removes it.
func (s *Store) ExpiredNotFoundBlocks(ctx context.Context, now time.Time) ([]*NotFoundBlock, error) {
	return s.notFoundBlocks(ctx, `WHERE expires_at > 0 AND expires_at <= ? ORDER BY expires_at LIMIT 100`, now.Unix())
}

// AllNotFoundBlocks is every live rule, for taking them all away at once.
func (s *Store) AllNotFoundBlocks(ctx context.Context) ([]*NotFoundBlock, error) {
	return s.notFoundBlocks(ctx, `ORDER BY id`)
}

// FindNotFoundBlock loads one.
func (s *Store) FindNotFoundBlock(ctx context.Context, id int64) (*NotFoundBlock, error) {
	blocks, err := s.notFoundBlocks(ctx, `WHERE id = ?`, id)
	if err != nil || len(blocks) == 0 {
		return nil, ErrNotFound
	}

	return blocks[0], nil
}

// NextNotFoundExpiry is when the soonest rule is due, or zero with none.
func (s *Store) NextNotFoundExpiry(ctx context.Context) time.Time {
	var next nullInt
	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT min(expires_at) FROM notfound_blocks WHERE expires_at > 0`).Scan(&next)

	if next.Int64 == 0 {
		return time.Time{}
	}

	return time.Unix(next.Int64, 0)
}

// DeleteNotFoundBlock forgets one.
func (s *Store) DeleteNotFoundBlock(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx), `DELETE FROM notfound_blocks WHERE id = ?`, id)

	return err
}

// EdgeDNSAccounts are every stored credential, whoever's, for work the panel
// does on its own rather than for one user.
func (s *Store) EdgeDNSAccounts(ctx context.Context) ([]*DNSAccount, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx), `SELECT id FROM dns_accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}

	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	accounts := []*DNSAccount{}
	for _, id := range ids {
		if account, err := s.FindDNSAccount(ctx, id, 0); err == nil {
			accounts = append(accounts, account)
		}
	}

	return accounts, nil
}
