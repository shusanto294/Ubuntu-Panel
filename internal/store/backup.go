package store

import (
	"context"
	"time"
)

// Backup is one copy of one site: its files, its database, or both.
//
// The row exists from the moment the work is queued rather than from the moment
// it finishes. A backup that dies halfway is then a `failed` row with the reason
// on it — something the person can see and delete — instead of a directory on
// disk that nothing in the panel knows about.
type Backup struct {
	ID     int64
	SiteID int64
	UserID int64

	// Kind is "manual" for one somebody asked for and "scheduled" for one the
	// panel took on its own. Retention only ever deletes scheduled backups:
	// somebody who took a copy before doing something risky does not expect the
	// panel to tidy it away.
	Kind   string
	Status string

	// Path is the directory holding this backup's parts. Removing the row and
	// removing that directory are one operation, and the panel does both.
	Path string
	Note string

	SizeBytes   int64
	HasFiles    bool
	HasDatabase bool

	DatabaseEngine string
	DatabaseName   string

	LastError  string
	CreatedAt  string
	FinishedAt string
}

const backupColumns = `id, site_id, user_id, kind, status, path, note, size_bytes,
	has_files, has_database, database_engine, database_name, last_error,
	created_at, finished_at`

func (s *Store) scanBackup(row siteScanner) (*Backup, error) {
	backup := &Backup{}

	var userID nullInt
	var note, engine, name, lastError, finishedAt text

	err := row.Scan(&backup.ID, &backup.SiteID, &userID, &backup.Kind, &backup.Status,
		&backup.Path, &note, &backup.SizeBytes, &backup.HasFiles, &backup.HasDatabase,
		&engine, &name, &lastError, &backup.CreatedAt, &finishedAt)
	if err != nil {
		return nil, notFound(err)
	}

	backup.UserID = userID.Int64
	backup.Note = note.String()
	backup.DatabaseEngine = engine.String()
	backup.DatabaseName = name.String()
	backup.LastError = lastError.String()
	backup.FinishedAt = finishedAt.String()

	return backup, nil
}

// CreateBackup records one before it is taken.
func (s *Store) CreateBackup(ctx context.Context, backup *Backup) (int64, error) {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO site_backups (site_id, user_id, kind, status, path, note,
			has_files, has_database, database_engine, database_name)
		 VALUES (?, ?, ?, 'pending', '', ?, ?, ?, ?, ?)`,
		backup.SiteID, nullableID(backup.UserID), orDefault(backup.Kind, "manual"),
		backup.Note, backup.HasFiles, backup.HasDatabase,
		backup.DatabaseEngine, backup.DatabaseName)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// FindBackup loads one.
func (s *Store) FindBackup(ctx context.Context, id int64) (*Backup, error) {
	return s.scanBackup(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+backupColumns+` FROM site_backups WHERE id = ?`, id))
}

// BackupsForSite is the list the Backups tab renders, newest first.
func (s *Store) BackupsForSite(ctx context.Context, siteID int64) ([]*Backup, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+backupColumns+` FROM site_backups WHERE site_id = ? ORDER BY id DESC`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []*Backup

	for rows.Next() {
		backup, err := s.scanBackup(rows)
		if err != nil {
			return nil, err
		}

		backups = append(backups, backup)
	}

	return backups, rows.Err()
}

// StartBackup marks one as running and records where it is being written.
func (s *Store) StartBackup(id int64, path string) {
	s.db.Exec(`UPDATE site_backups SET status = 'running', path = ?, last_error = NULL WHERE id = ?`,
		path, id)
}

// FinishBackup records how it went. A size of zero on a complete backup is
// left alone rather than written, because `du` failing is not a reason to claim
// the copy is empty.
func (s *Store) FinishBackup(id int64, status, message string, size int64) {
	s.db.Exec(`UPDATE site_backups SET status = ?, last_error = ?, size_bytes = ?,
		finished_at = datetime('now') WHERE id = ?`, status, message, size, id)
}

// DeleteBackup forgets the row. Its directory is removed by the caller, which
// is the only thing that knows whether the removal worked.
func (s *Store) DeleteBackup(id int64) { s.db.Exec(`DELETE FROM site_backups WHERE id = ?`, id) }

// PrunableBackups is the scheduled backups of one site beyond the newest
// `keep`, oldest last.
//
// Scheduled only. A manual backup is somebody's copy of the site as it was
// before they changed something, and the panel deleting it because a nightly
// run filled the quota is the panel throwing away the one thing it was asked
// to hold on to.
func (s *Store) PrunableBackups(ctx context.Context, siteID int64, keep int) ([]*Backup, error) {
	if keep < 1 {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+backupColumns+` FROM site_backups
		 WHERE site_id = ? AND kind = 'scheduled' AND status = 'complete'
		 ORDER BY id DESC LIMIT -1 OFFSET ?`, siteID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []*Backup

	for rows.Next() {
		backup, err := s.scanBackup(rows)
		if err != nil {
			return nil, err
		}

		backups = append(backups, backup)
	}

	return backups, rows.Err()
}

// ------------------------------------------------------------------ schedule

// SetSiteBackupSchedule stores how often a site backs itself up and when the
// next one is due.
func (s *Store) SetSiteBackupSchedule(siteID int64, schedule string, keep int, nextAt int64) {
	s.db.Exec(`UPDATE sites SET backup_schedule = ?, backup_keep = ?, backup_next_at = ? WHERE id = ?`,
		schedule, keep, nextAt, siteID)
}

// SetSiteBackupNext moves the due time on, after a scheduled run.
func (s *Store) SetSiteBackupNext(siteID int64, nextAt int64) {
	s.db.Exec(`UPDATE sites SET backup_next_at = ? WHERE id = ?`, nextAt, siteID)
}

// DueBackups is every live site whose scheduled backup is due.
//
// Asked for by the panel's own ticker rather than by cron, so a machine that
// was switched off overnight takes the backup it missed as soon as it is back
// instead of waiting a whole day for the next window.
func (s *Store) DueBackups(ctx context.Context, now time.Time) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites
		 WHERE deleted_at IS NULL AND backup_schedule IS NOT NULL AND backup_schedule != ''
		   AND backup_schedule != 'off' AND backup_next_at > 0 AND backup_next_at <= ?
		 ORDER BY id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var due []*Site

	for rows.Next() {
		site, err := s.scanSite(rows)
		if err != nil {
			return nil, err
		}

		due = append(due, site)
	}

	return due, rows.Err()
}

// Summary is the shape the Backups tab reads.
func (backup *Backup) Summary() map[string]any {
	return map[string]any{
		"id":           backup.ID,
		"site_id":      backup.SiteID,
		"kind":         backup.Kind,
		"status":       backup.Status,
		"note":         backup.Note,
		"size_bytes":   backup.SizeBytes,
		"has_files":    backup.HasFiles,
		"has_database": backup.HasDatabase,
		"database":     nullableText(backup.DatabaseName),
		"last_error":   backup.LastError,
		"created_at":   backup.CreatedAt,
		"finished_at":  nullableText(backup.FinishedAt),
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}

	return value
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}

	return id
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
