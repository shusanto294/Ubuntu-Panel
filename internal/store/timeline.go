package store

import (
	"context"
	"strings"
	"time"
)

// SSHEvent is one SSH login or attempt, as the panel keeps it.
type SSHEvent struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	User      string `json:"user"`
	IP        string `json:"ip"`
	Method    string `json:"method"`
	CreatedAt int64  `json:"created_at"`
}

// RecordSSHEvents keeps events read from sshd's log, ignoring any already
// kept: the reader copies overlapping windows so nothing falls between two.
//
// One transaction for the lot: the first copy is a week of a machine being
// guessed at, and a commit per row would be thousands of disk syncs.
func (s *Store) RecordSSHEvents(ctx context.Context, events []SSHEvent) {
	if len(events) == 0 {
		return
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}

	for _, event := range events {
		tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO ssh_events (kind, username, ip_address, method, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			event.Kind, event.User, event.IP, event.Method, event.CreatedAt)
	}

	tx.Commit()
}

// PruneSSHEvents drops what is older than the retention window.
func (s *Store) PruneSSHEvents(ctx context.Context, keep time.Duration) {
	s.db.ExecContext(ctx, `DELETE FROM ssh_events WHERE created_at < ?`, time.Now().Add(-keep).Unix())
}

// Timeline filters beyond the task types.
const (
	TimelinePanelLogins = "panel-login"
	TimelineSSH         = "ssh"
)

// TimelineEntry is one line of the Logs page: a task, a panel sign-in or an
// SSH event, whichever Source says. Exactly one of the three is set.
type TimelineEntry struct {
	Source string        `json:"source"`
	Task   *Task         `json:"task,omitempty"`
	Login  *LoginAttempt `json:"login,omitempty"`
	SSH    *SSHEvent     `json:"ssh,omitempty"`
}

// Timeline is one page of everything that happened, newest first, and how
// many entries there are in all.
//
// The filter is a task type, TimelinePanelLogins, TimelineSSH, or "" for
// everything — which leaves out failed SSH attempts: a machine on the internet
// collects a thousand a day, and they would bury every other line. They are
// all under TimelineSSH.
//
// Paged in SQL over the three tables at once, and only the page's own rows are
// then read in full: tasks carry their console, and a thousand of those is not
// a list.
func (s *Store) Timeline(ctx context.Context, filter string, limit, offset int) ([]TimelineEntry, int, error) {
	ctx = ctxOrBackground(ctx)

	var (
		parts []string
		args  []any
	)

	tasks := `SELECT 'task' AS source, id, CAST(strftime('%s', created_at) AS INTEGER) AS at FROM activity_logs`
	logins := `SELECT 'panel' AS source, id, created_at AS at FROM login_attempts`
	ssh := `SELECT 'ssh' AS source, id, created_at AS at FROM ssh_events`

	switch filter {
	case "":
		parts = []string{tasks, logins, ssh + ` WHERE kind = 'accepted'`}
	case TimelinePanelLogins:
		parts = []string{logins}
	case TimelineSSH:
		parts = []string{ssh}
	default:
		parts = []string{tasks + ` WHERE type = ?`}
		args = append(args, filter)
	}

	union := strings.Join(parts, " UNION ALL ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (`+union+`)`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT source, id FROM (`+union+`) ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}

	type ref struct {
		source string
		id     int64
	}

	refs := []ref{}
	ids := map[string][]any{}

	for rows.Next() {
		var entry ref
		if err := rows.Scan(&entry.source, &entry.id); err != nil {
			rows.Close()
			return nil, 0, err
		}

		refs = append(refs, entry)
		ids[entry.source] = append(ids[entry.source], entry.id)
	}
	rows.Close()

	taskRows, err := s.tasksByID(ctx, ids["task"])
	if err != nil {
		return nil, 0, err
	}

	loginRows, err := s.loginsByID(ctx, ids["panel"])
	if err != nil {
		return nil, 0, err
	}

	sshRows, err := s.sshEventsByID(ctx, ids["ssh"])
	if err != nil {
		return nil, 0, err
	}

	entries := make([]TimelineEntry, 0, len(refs))

	for _, entry := range refs {
		line := TimelineEntry{Source: entry.source}

		switch entry.source {
		case "task":
			line.Task = taskRows[entry.id]
		case "panel":
			line.Login = loginRows[entry.id]
		case "ssh":
			line.SSH = sshRows[entry.id]
		}

		if line.Task != nil || line.Login != nil || line.SSH != nil {
			entries = append(entries, line)
		}
	}

	return entries, total, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func (s *Store) tasksByID(ctx context.Context, ids []any) (map[int64]*Task, error) {
	found := map[int64]*Task{}
	if len(ids) == 0 {
		return found, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, action, status, message, current_step, progress,
		        started_at, finished_at, created_at
		   FROM activity_logs WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}

	list, err := scanTaskList(rows)
	if err != nil {
		return nil, err
	}

	for _, task := range list {
		found[task.ID] = task
	}

	return found, nil
}

func (s *Store) loginsByID(ctx context.Context, ids []any) (map[int64]*LoginAttempt, error) {
	found := map[int64]*LoginAttempt{}
	if len(ids) == 0 {
		return found, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, coalesce(user_id, 0), coalesce(email, ''), ip_address, coalesce(user_agent, ''),
		        success, reason, created_at
		   FROM login_attempts WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		attempt := &LoginAttempt{}
		var success int

		if err := rows.Scan(&attempt.ID, &attempt.UserID, &attempt.Email, &attempt.IP,
			&attempt.UserAgent, &success, &attempt.Reason, &attempt.CreatedAt); err != nil {
			return nil, err
		}

		attempt.Success = success == 1
		found[attempt.ID] = attempt
	}

	return found, rows.Err()
}

func (s *Store) sshEventsByID(ctx context.Context, ids []any) (map[int64]*SSHEvent, error) {
	found := map[int64]*SSHEvent{}
	if len(ids) == 0 {
		return found, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, username, ip_address, method, created_at
		   FROM ssh_events WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		event := &SSHEvent{}

		if err := rows.Scan(&event.ID, &event.Kind, &event.User, &event.IP,
			&event.Method, &event.CreatedAt); err != nil {
			return nil, err
		}

		found[event.ID] = event
	}

	return found, rows.Err()
}
