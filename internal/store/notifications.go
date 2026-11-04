package store

import (
	"context"
	"encoding/json"
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

	// Data is what the notification is about, for its page: "ips", "task",
	// "events_after" and "events_until".
	Data map[string]any `json:"data"`
}

const notificationColumns = `id, created_at, kind, level, title, body, link, read_at, data`

func scanNotification(row interface{ Scan(...any) error }) (Notification, error) {
	var (
		n    Notification
		data string
	)

	if err := row.Scan(&n.ID, &n.CreatedAt, &n.Kind, &n.Level, &n.Title, &n.Body, &n.Link, &n.ReadAt, &data); err != nil {
		return n, err
	}

	n.Data = map[string]any{}
	json.Unmarshal([]byte(data), &n.Data)

	return n, nil
}

// FindNotification is one, by id.
func (s *Store) FindNotification(ctx context.Context, id int64) (*Notification, error) {
	n, err := scanNotification(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+notificationColumns+` FROM notifications WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}

	return &n, nil
}

// AddNotification keeps one and returns its id.
func (s *Store) AddNotification(ctx context.Context, n Notification) (int64, error) {
	if n.CreatedAt == 0 {
		n.CreatedAt = time.Now().Unix()
	}

	data := "{}"
	if len(n.Data) > 0 {
		if encoded, err := json.Marshal(n.Data); err == nil {
			data = string(encoded)
		}
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO notifications (created_at, kind, level, title, body, link, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.CreatedAt, n.Kind, n.Level, n.Title, n.Body, n.Link, data)
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
		`SELECT `+notificationColumns+`
		   FROM notifications WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	list := []Notification{}

	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
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

// DeleteNotifications deletes the ids given, and says how many went.
func (s *Store) DeleteNotifications(ctx context.Context, ids []int64) int64 {
	if len(ids) == 0 {
		return 0
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`DELETE FROM notifications WHERE id IN (`+placeholders(len(ids))+`)`, args...)
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
