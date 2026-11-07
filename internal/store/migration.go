package store

import (
	"context"
	"database/sql"
	"time"
)

// Migration is one site or mail domain moving between two panels, as this
// panel sees it.
type Migration struct {
	ID        int64  `json:"id"`
	Direction string `json:"direction"` // out | in
	Kind      string `json:"kind"`      // site | mail
	Name      string `json:"name"`      // the domain
	Peer      string `json:"peer"`      // the other panel's address (out) or address (in)
	RemoteID  string `json:"remote_id"` // the transfer's id on the receiving panel
	Status    string `json:"status"`
	Message   string `json:"message"`
	TaskID    int64  `json:"task_id"`
	LocalID   int64  `json:"local_id"` // the site or mail domain on this panel
	Options   string `json:"options"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const migrationColumns = `id, direction, kind, name, peer, remote_id, status, message,
	task_id, local_id, options, created_at, updated_at`

func scanMigration(row siteScanner) (*Migration, error) {
	m := &Migration{}
	var taskID, localID sql.NullInt64

	if err := row.Scan(&m.ID, &m.Direction, &m.Kind, &m.Name, &m.Peer, &m.RemoteID, &m.Status,
		&m.Message, &taskID, &localID, &m.Options, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, notFound(err)
	}

	m.TaskID, m.LocalID = taskID.Int64, localID.Int64

	return m, nil
}

// CreateMigration stores a new transfer and returns its id.
func (s *Store) CreateMigration(ctx context.Context, m *Migration) (int64, error) {
	now := time.Now().Unix()
	if m.Status == "" {
		m.Status = "pending"
	}
	if m.Options == "" {
		m.Options = "{}"
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO migrations (direction, kind, name, peer, remote_id, status, message, task_id,
		                         local_id, options, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Direction, m.Kind, m.Name, m.Peer, m.RemoteID, m.Status, m.Message,
		nullID(m.TaskID), nullID(m.LocalID), m.Options, now, now)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// FindMigration loads one.
func (s *Store) FindMigration(ctx context.Context, id int64) (*Migration, error) {
	return scanMigration(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+migrationColumns+` FROM migrations WHERE id = ?`, id))
}

// FindIncomingMigration loads a transfer this panel is receiving, by the id
// it gave the sender.
func (s *Store) FindIncomingMigration(ctx context.Context, remoteID string) (*Migration, error) {
	return scanMigration(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+migrationColumns+` FROM migrations WHERE direction = 'in' AND remote_id = ?`, remoteID))
}

// Migrations are the latest transfers both ways, newest first.
func (s *Store) Migrations(ctx context.Context, limit int) ([]*Migration, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+migrationColumns+` FROM migrations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []*Migration{}
	for rows.Next() {
		m, err := scanMigration(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}

	return list, rows.Err()
}

// SetMigrationStatus records where a transfer has got to.
func (s *Store) SetMigrationStatus(id int64, status, message string) {
	s.db.Exec(`UPDATE migrations SET status = ?, message = ?, updated_at = ? WHERE id = ?`,
		status, message, time.Now().Unix(), id)
}

// SetMigrationRemote records the transfer's id on the receiving panel.
func (s *Store) SetMigrationRemote(id int64, remoteID string) {
	s.db.Exec(`UPDATE migrations SET remote_id = ?, updated_at = ? WHERE id = ?`,
		remoteID, time.Now().Unix(), id)
}

// SetMigrationTask records the task that shows a transfer's console.
func (s *Store) SetMigrationTask(id, taskID int64) {
	s.db.Exec(`UPDATE migrations SET task_id = ?, updated_at = ? WHERE id = ?`,
		nullID(taskID), time.Now().Unix(), id)
}

// SetMigrationLocal records the site or mail domain a transfer made or moved.
func (s *Store) SetMigrationLocal(id, localID int64) {
	s.db.Exec(`UPDATE migrations SET local_id = ?, updated_at = ? WHERE id = ?`,
		nullID(localID), time.Now().Unix(), id)
}

// ActiveMigration is a transfer of name that is still going, either way, or
// nil.
func (s *Store) ActiveMigration(ctx context.Context, kind, name string) *Migration {
	m, err := scanMigration(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+migrationColumns+` FROM migrations
		  WHERE kind = ? AND name = ? AND status NOT IN ('done', 'failed', 'cancelled')
		  ORDER BY id DESC LIMIT 1`, kind, name))
	if err != nil {
		return nil
	}
	return m
}
