package store

import (
	"context"
	"time"
)

// EdgeCache is one thing the panel read from Cloudflare, as JSON, and when.
func (s *Store) EdgeCache(ctx context.Context, key string) (body string, fetchedAt time.Time, ok bool) {
	var at int64

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT body, fetched_at FROM edge_cache WHERE key = ?`, key).Scan(&body, &at)
	if err != nil {
		return "", time.Time{}, false
	}

	return body, time.Unix(at, 0), true
}

// PutEdgeCache keeps it.
func (s *Store) PutEdgeCache(ctx context.Context, key, body string, fetchedAt time.Time) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO edge_cache (key, body, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET body = excluded.body, fetched_at = excluded.fetched_at`,
		key, body, fetchedAt.Unix())

	return err
}

// ForgetEdgeCache drops one, so the next look reads Cloudflare.
func (s *Store) ForgetEdgeCache(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx), `DELETE FROM edge_cache WHERE key = ?`, key)

	return err
}
