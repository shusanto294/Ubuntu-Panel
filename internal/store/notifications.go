package store

import (
	"context"
	"time"
)

// Notification is one thing the panel told its owner.
type Notification struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Kind      string `json:"kind"`
	Level     string `json:"level"` // critical | warning | info
	Title     string `json:"title"`
	Body      string `json:"body"`
	Link      string `json:"link"`
	ReadAt    *int64 `json:"read_at"`
}

// AddNotification keeps one and returns its id.
func (s *Store) AddNotification(ctx context.Context, n Notification) (int64, error) {
	if n.CreatedAt == 0 {
		n.CreatedAt = time.Now().Unix()
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO notifications (created_at, kind, level, title, body, link) VALUES (?, ?, ?, ?, ?, ?)`,
		n.CreatedAt, n.Kind, n.Level, n.Title, n.Body, n.Link)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// Notifications is a page of them, newest first, and how many there are.
// unreadOnly leaves out the ones already read; level, when set, keeps only
// that level.
func (s *Store) Notifications(ctx context.Context, unreadOnly bool, level string, limit, offset int) ([]Notification, int, error) {
	ctx = ctxOrBackground(ctx)

	where := `1 = 1`
	args := []any{}

	if unreadOnly {
		where += ` AND read_at IS NULL`
	}
	if level != "" {
		where += ` AND level = ?`
		args = append(args, level)
	}

	total := 0
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM notifications WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, kind, level, title, body, link, read_at
		   FROM notifications WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	list := []Notification{}

	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.CreatedAt, &n.Kind, &n.Level, &n.Title, &n.Body, &n.Link, &n.ReadAt); err != nil {
			return nil, 0, err
		}
		list = append(list, n)
	}

	return list, total, rows.Err()
}

// UnreadNotifications is how many have not been read.
func (s *Store) UnreadNotifications(ctx context.Context) int {
	count := 0
	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&count)

	return count
}

// ReadNotifications marks the ids read, or every one when ids is empty.
func (s *Store) ReadNotifications(ctx context.Context, ids []int64) {
	now := time.Now().Unix()

	if len(ids) == 0 {
		s.db.ExecContext(ctxOrBackground(ctx), `UPDATE notifications SET read_at = ? WHERE read_at IS NULL`, now)
		return
	}

	args := []any{now}
	for _, id := range ids {
		args = append(args, id)
	}

	s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE notifications SET read_at = ? WHERE read_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, args...)
}

// ClearNotifications deletes the read ones, or all of them, and says how many.
func (s *Store) ClearNotifications(ctx context.Context, readOnly bool) int64 {
	query := `DELETE FROM notifications`
	if readOnly {
		query += ` WHERE read_at IS NOT NULL`
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx), query)
	if err != nil {
		return 0
	}

	count, _ := result.RowsAffected()

	return count
}

// PruneNotifications deletes the ones older than a time.
func (s *Store) PruneNotifications(ctx context.Context, before time.Time) int64 {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`DELETE FROM notifications WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0
	}

	count, _ := result.RowsAffected()

	return count
}

// FailedTasksSince are the tasks that failed after a time, oldest first.
func (s *Store) FailedTasksSince(ctx context.Context, since time.Time) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT id, type, action, status, message, current_step, progress,
		        started_at, finished_at, created_at
		   FROM activity_logs
		  WHERE status = 'failed' AND finished_at IS NOT NULL AND finished_at > ?
		  ORDER BY id LIMIT 50`, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}

	return scanTaskList(rows)
}
