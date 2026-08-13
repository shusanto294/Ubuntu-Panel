package store

import (
	"context"
	"fmt"
	"strings"
)

// schema is applied in order on every start. Every statement is written to be
// safe to run against a database that already has it, so there is no migration
// bookkeeping to get out of step with the file.
//
// Column names and types match the C panel's schema exactly, which in turn
// matched the Laravel migrations: the same panel.db can be served by any of
// the three, which is what makes an upgrade a binary swap rather than a data
// migration.
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

	// DNS provider credentials. api_token and api_secret are stored encrypted
	// with the application key.
	`CREATE TABLE IF NOT EXISTS dns_accounts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id     INTEGER NOT NULL,
		provider    TEXT NOT NULL DEFAULT 'cloudflare',
		label       TEXT NOT NULL,
		api_token   TEXT NOT NULL,
		api_secret  TEXT,
		email       TEXT,
		account_id  TEXT,
		verified_at TEXT,
		created_at  TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
	)`,

	// One row per catalogue entry: what the machine has, and what is being
	// installed onto it.
	`CREATE TABLE IF NOT EXISTS services (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		key          TEXT NOT NULL UNIQUE,
		status       TEXT NOT NULL DEFAULT 'not_installed',
		version      TEXT,
		last_error   TEXT,
		task_id      INTEGER,
		sort_order   INTEGER NOT NULL DEFAULT 0,
		queued_at    TEXT,
		installed_at TEXT,
		detected_at  TEXT,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS services_status ON services(status)`,

	`CREATE TABLE IF NOT EXISTS databases (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id         INTEGER,
		engine          TEXT NOT NULL,
		name            TEXT NOT NULL,
		username        TEXT,
		password        TEXT,
		charset         TEXT,
		status          TEXT NOT NULL DEFAULT 'pending',
		last_error      TEXT,
		managed_by_site INTEGER NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(engine, name)
	)`,

	`CREATE TABLE IF NOT EXISTS sites (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id           INTEGER,
		dns_account_id    INTEGER,
		database_id       INTEGER,
		domain            TEXT NOT NULL UNIQUE,
		type              TEXT NOT NULL DEFAULT 'php',
		aliases           TEXT,
		root_path         TEXT NOT NULL,
		web_directory     TEXT NOT NULL DEFAULT '/public',
		php_version       TEXT NOT NULL DEFAULT '8.3',
		node_version      TEXT,
		status            TEXT NOT NULL DEFAULT 'pending',
		ssl               INTEGER NOT NULL DEFAULT 0,
		last_error        TEXT,
		app_port          INTEGER,
		start_command     TEXT,
		build_command     TEXT,
		repository        TEXT,
		branch            TEXT NOT NULL DEFAULT 'main',
		wp_admin_user     TEXT,
		wp_admin_password TEXT,
		wp_admin_email    TEXT,
		wp_title          TEXT,
		manage_dns        INTEGER NOT NULL DEFAULT 0,
		dns_zone_id       TEXT,
		dns_record_ids    TEXT,
		dns_type          TEXT NOT NULL DEFAULT 'A',
		dns_content       TEXT,
		dns_proxied       INTEGER NOT NULL DEFAULT 1,
		deleted_at        TEXT,
		created_at        TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS sites_status ON sites(status)`,

	// A task is one deployment, install or mail operation, with the console
	// the browser polls. The table keeps the Laravel name.
	`CREATE TABLE IF NOT EXISTS activity_logs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id      INTEGER,
		site_id      INTEGER,
		type         TEXT NOT NULL,
		action       TEXT NOT NULL,
		status       TEXT NOT NULL DEFAULT 'running',
		message      TEXT,
		output       TEXT,
		steps        TEXT,
		current_step TEXT,
		progress     INTEGER NOT NULL DEFAULT 0,
		started_at   TEXT,
		finished_at  TEXT,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS activity_logs_type_status ON activity_logs(type, status)`,
	`CREATE INDEX IF NOT EXISTS activity_logs_created ON activity_logs(id DESC)`,

	`CREATE TABLE IF NOT EXISTS email_domains (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id         INTEGER,
		dns_account_id  INTEGER,
		domain          TEXT NOT NULL UNIQUE,
		status          TEXT NOT NULL DEFAULT 'pending',
		dkim_selector   TEXT NOT NULL DEFAULT 'mail',
		dkim_public_key TEXT,
		manage_dns      INTEGER NOT NULL DEFAULT 0,
		dns_record_ids  TEXT,
		last_error      TEXT,
		deleted_at      TEXT,
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
	)`,

	`CREATE TABLE IF NOT EXISTS email_accounts (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		email_domain_id INTEGER NOT NULL,
		user_id         INTEGER,
		local_part      TEXT NOT NULL,
		password        TEXT NOT NULL,
		quota_mb        INTEGER NOT NULL DEFAULT 2048,
		status          TEXT NOT NULL DEFAULT 'pending',
		last_error      TEXT,
		deleted_at      TEXT,
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(email_domain_id, local_part)
	)`,

	`CREATE TABLE IF NOT EXISTS metric_samples (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		cpu_percent    REAL,
		memory_percent REAL NOT NULL,
		disk_percent   REAL NOT NULL,
		swap_percent   REAL,
		memory_used    INTEGER,
		memory_total   INTEGER,
		disk_used      INTEGER,
		disk_total     INTEGER,
		load_1         REAL,
		sampled_at     TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS metric_samples_sampled ON metric_samples(sampled_at)`,

	// One-shot tickets for the websocket. The socket cannot rely on the
	// session cookie — a cross-scheme upgrade does not always carry it — so an
	// authenticated request mints a token that is worth one connection for one
	// minute.
	`CREATE TABLE IF NOT EXISTS terminal_tickets (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL,
		mode       TEXT NOT NULL DEFAULT 'shell',
		expires_at INTEGER NOT NULL,
		used_at    INTEGER
	)`,

	// The background work queue, so a restart does not lose a deployment.
	`CREATE TABLE IF NOT EXISTS jobs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		kind         TEXT NOT NULL,
		payload      TEXT NOT NULL DEFAULT '{}',
		status       TEXT NOT NULL DEFAULT 'queued',
		attempts     INTEGER NOT NULL DEFAULT 0,
		last_error   TEXT,
		available_at INTEGER NOT NULL DEFAULT 0,
		reserved_at  INTEGER,
		created_at   TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS jobs_status ON jobs(status, available_at)`,
}

// migrations are ALTER TABLE statements for databases created by an older
// build. SQLite has no `ADD COLUMN IF NOT EXISTS`, so a duplicate-column error
// here is the expected outcome on every start after the first and is ignored;
// anything else is not.
var migrations = []string{
	`ALTER TABLE sites ADD COLUMN deleted_at TEXT`,
	`ALTER TABLE email_domains ADD COLUMN deleted_at TEXT`,
	`ALTER TABLE email_accounts ADD COLUMN deleted_at TEXT`,
	`ALTER TABLE email_domains ADD COLUMN dns_record_ids TEXT`,
	`ALTER TABLE services ADD COLUMN detected_at TEXT`,
	`ALTER TABLE sites ADD COLUMN php_settings TEXT`,
	`ALTER TABLE sites ADD COLUMN cache_enabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE sites ADD COLUMN cache_ttl TEXT`,
	`ALTER TABLE sites ADD COLUMN cache_mode TEXT`,
}

func (s *Store) migrate(ctx context.Context) error {
	for _, statement := range schema {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("applying schema: %w\n%s", err, statement)
		}
	}

	for _, statement := range migrations {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("applying migration: %w\n%s", err, statement)
		}
	}

	return nil
}
