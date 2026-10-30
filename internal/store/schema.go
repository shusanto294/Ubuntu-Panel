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

	// The zones this machine answers for itself, and the records in them.
	//
	// Unlike every other DNS the panel drives, these rows are the record of
	// truth: the zone files under /etc/bind are generated from them on every
	// change. A zone's name is unique across the machine rather than per
	// person, because there is one nameserver here and it cannot answer two
	// ways for the same name.
	`CREATE TABLE IF NOT EXISTS dns_zones (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER,
		name       TEXT NOT NULL UNIQUE,
		serial     INTEGER NOT NULL DEFAULT 0,
		ttl        INTEGER NOT NULL DEFAULT 3600,
		refresh    INTEGER NOT NULL DEFAULT 3600,
		retry      INTEGER NOT NULL DEFAULT 600,
		expire     INTEGER NOT NULL DEFAULT 1209600,
		minimum    INTEGER NOT NULL DEFAULT 3600,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,

	`CREATE TABLE IF NOT EXISTS dns_zone_records (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		zone_id    INTEGER NOT NULL,
		type       TEXT NOT NULL,
		name       TEXT NOT NULL,
		content    TEXT NOT NULL,
		priority   INTEGER NOT NULL DEFAULT -1,
		ttl        INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS dns_zone_records_zone ON dns_zone_records(zone_id)`,

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
		tls_mode          TEXT,
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
		name            TEXT,
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

	// The keys an AI assistant signs its MCP requests with. Only the hash is
	// kept: the token is shown once, when it is issued, and a database somebody
	// walks off with is then a list of names rather than a way in.
	`CREATE TABLE IF NOT EXISTS api_tokens (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id      INTEGER NOT NULL,
		name         TEXT NOT NULL,
		token_hash   TEXT NOT NULL UNIQUE,
		prefix       TEXT NOT NULL,
		server_name  TEXT,
		last_used_at TEXT,
		created_at   TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE INDEX IF NOT EXISTS api_tokens_user ON api_tokens(user_id)`,

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

	// One backup of one site: where it is on disk, what went into it, and how
	// big it turned out.
	//
	// The row is written before the work starts, so a backup that fails halfway
	// is a `failed` row somebody can see and delete rather than a directory
	// nothing knows about. `path` is the directory the archive members live in;
	// removing the row and removing that directory are the same operation, and
	// the panel does both.
	`CREATE TABLE IF NOT EXISTS site_backups (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id      INTEGER NOT NULL,
		user_id      INTEGER,
		kind         TEXT NOT NULL DEFAULT 'manual',
		status       TEXT NOT NULL DEFAULT 'pending',
		path         TEXT NOT NULL DEFAULT '',
		note         TEXT,
		size_bytes   INTEGER NOT NULL DEFAULT 0,
		has_files    INTEGER NOT NULL DEFAULT 1,
		has_database INTEGER NOT NULL DEFAULT 0,
		database_engine TEXT,
		database_name   TEXT,
		last_error   TEXT,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		finished_at  TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS site_backups_site ON site_backups(site_id, id DESC)`,

	// Every attempt to sign in to the panel, right or wrong. It is what the
	// lockout counts, and what the Security page shows as login activity.
	// `email` is what was typed, not an account id: the failures that matter
	// most are the ones for addresses that have no account.
	`CREATE TABLE IF NOT EXISTS login_attempts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER,
		email      TEXT,
		ip_address TEXT NOT NULL,
		user_agent TEXT,
		success    INTEGER NOT NULL DEFAULT 0,
		reason     TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS login_attempts_ip ON login_attempts(ip_address, created_at)`,
	`CREATE INDEX IF NOT EXISTS login_attempts_created ON login_attempts(created_at)`,

	// The country each address the Security page has shown belongs to, looked
	// up once and kept, so a flag beside an address costs nothing the second
	// time. An empty country is an address the lookup could not place — a
	// private range, or one the service did not know — and is not asked again.
	// Every SSH login and attempt, copied out of sshd's log once a minute. The
	// journal is the machine's to rotate and often keeps days, not months; this
	// keeps them as long as the panel keeps its own sign-ins, so the Logs page
	// can say who got in, or tried to, long after the journal has forgotten.
	// The unique index is what makes copying an overlapping window harmless.
	`CREATE TABLE IF NOT EXISTS ssh_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		kind       TEXT NOT NULL,
		username   TEXT NOT NULL,
		ip_address TEXT NOT NULL,
		method     TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ssh_events_unique
		ON ssh_events(created_at, ip_address, username, kind, method)`,
	`CREATE INDEX IF NOT EXISTS ssh_events_kind ON ssh_events(kind, created_at)`,

	`CREATE TABLE IF NOT EXISTS ip_countries (
		ip         TEXT PRIMARY KEY,
		country    TEXT NOT NULL,
		checked_at INTEGER NOT NULL
	)`,
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
	`ALTER TABLE api_tokens ADD COLUMN server_name TEXT`,
	`ALTER TABLE sites ADD COLUMN tls_mode TEXT`,
	`ALTER TABLE email_accounts ADD COLUMN name TEXT`,

	// How often this site backs itself up, how many to keep, and when the next
	// one is due. `backup_next_at` is a Unix second rather than a cron line:
	// the panel decides when, so there is no expression to parse and nothing to
	// disagree with about what "daily" means.
	`ALTER TABLE sites ADD COLUMN backup_schedule TEXT`,
	`ALTER TABLE sites ADD COLUMN backup_keep INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE sites ADD COLUMN backup_next_at INTEGER NOT NULL DEFAULT 0`,

	// The site this one is a staging copy of.
	//
	// A staging site is an ordinary site row and not a second kind of thing:
	// it has a vhost, a certificate, a PHP version, backups and a terminal,
	// because it is a site. This column is the only difference, and what it
	// buys is the pair of buttons that keep the two in step.
	`ALTER TABLE sites ADD COLUMN staging_of INTEGER`,
	`CREATE INDEX IF NOT EXISTS sites_staging_of ON sites(staging_of)`,

	// How the panel authenticates to this site's git remote.
	//
	// `repo_auth` is none, key or token. The deploy key's private half is a
	// file on disk under the panel's own directory, never a column — only its
	// public half is here, because the page shows it on every render and
	// reading a file for that is a sudo per page load. The token is a
	// credential and is encrypted with the application key like every other.
	`ALTER TABLE sites ADD COLUMN repo_auth TEXT`,
	`ALTER TABLE sites ADD COLUMN repo_token TEXT`,
	`ALTER TABLE sites ADD COLUMN repo_deploy_key TEXT`,

	// Pulling without being asked.
	//
	// Two mechanisms, because they fail in opposite situations: a webhook is
	// instant and needs the panel to be reachable from the internet, and
	// polling needs nothing of anybody but finds out late. `auto_pull` is the
	// polling interval, `auto_pull_next_at` the Unix second it is next due —
	// the same arrangement as the backup schedule, so nothing has to parse a
	// cron expression. `webhook_secret` is what a push is signed with.
	`ALTER TABLE sites ADD COLUMN webhook_secret TEXT`,
	`ALTER TABLE sites ADD COLUMN auto_pull TEXT`,
	`ALTER TABLE sites ADD COLUMN auto_pull_next_at INTEGER NOT NULL DEFAULT 0`,

	// The commit the site is serving, so a pull can say what it changed and a
	// poll can tell whether there is anything to do.
	`ALTER TABLE sites ADD COLUMN last_commit TEXT`,

	// Two-factor sign-in. The secret is encrypted with the application key like
	// every other credential; it is set when somebody starts enrolling and only
	// counts once `two_factor_confirmed_at` says a code from it was typed back.
	// The recovery codes are kept as hashes, and `two_factor_last_step` is the
	// last thirty-second window a code was accepted for, so the same code
	// cannot be used twice.
	`ALTER TABLE users ADD COLUMN two_factor_secret TEXT`,
	`ALTER TABLE users ADD COLUMN two_factor_confirmed_at TEXT`,
	`ALTER TABLE users ADD COLUMN two_factor_recovery_codes TEXT`,
	`ALTER TABLE users ADD COLUMN two_factor_last_step INTEGER NOT NULL DEFAULT 0`,

	// The highest CPU and memory reading of the minute a sample stands for,
	// read once a second. The percentages beside them are the minute's
	// averages; without these a spike between two samples was never recorded.
	`ALTER TABLE metric_samples ADD COLUMN cpu_max REAL`,
	`ALTER TABLE metric_samples ADD COLUMN memory_max REAL`,
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
