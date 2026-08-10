package store

import (
	"context"
	"fmt"
)

// schema is applied in order on every start. Every statement is written to be
// safe to run against a database that already has it, so there is no migration
// bookkeeping to get out of step with the file.
//
// Column names and types match the C panel's schema exactly: the same
// panel.db can be served by either binary, which is what makes porting one
// page at a time possible.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		name              TEXT NOT NULL,
		email             TEXT NOT NULL UNIQUE,
		email_verified_at TEXT,
		password          TEXT NOT NULL,
		remember_token    TEXT,
		created_at        TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id            TEXT PRIMARY KEY,
		user_id       INTEGER,
		ip_address    TEXT,
		user_agent    TEXT,
		payload       TEXT NOT NULL DEFAULT '{}',
		last_activity INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_last_activity ON sessions(last_activity)`,
	`CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id)`,

	`CREATE TABLE IF NOT EXISTS settings (
		key        TEXT PRIMARY KEY,
		value      TEXT,
		secret     INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,

	`CREATE TABLE IF NOT EXISTS activity_logs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER,
		action     TEXT NOT NULL,
		subject    TEXT,
		detail     TEXT,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS activity_logs_created_at ON activity_logs(created_at)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for _, statement := range schema {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("applying schema: %w\n%s", err, statement)
		}
	}

	return nil
}
