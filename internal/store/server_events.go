package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// ServerEvent is one line a program on the machine logged, read.
type ServerEvent struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Source    string `json:"source"`
	Site      string `json:"site"`
	Level     string `json:"level"`
	IP        string `json:"ip"`
	Message   string `json:"message"`
}

// RecordServerEvents keeps a batch, in one transaction.
func (s *Store) RecordServerEvents(ctx context.Context, events []ServerEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctxOrBackground(ctx), nil)
	if err != nil {
		return err
	}

	statement, err := tx.PrepareContext(ctx,
		`INSERT INTO server_events (created_at, source, site, level, ip_address, message)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer statement.Close()

	for _, event := range events {
		if _, err := statement.ExecContext(ctx, event.CreatedAt, event.Source, event.Site,
			event.Level, event.IP, event.Message); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

// LogCursor is how far into a file the copy has read, and which file it was
// by inode — a rotated log is a new file at the same path.
func (s *Store) LogCursor(ctx context.Context, path string) (inode string, offset int64, ok bool) {
	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT inode, offset FROM log_cursors WHERE path = ?`, path).Scan(&inode, &offset)

	return inode, offset, err == nil
}

func (s *Store) SetLogCursor(ctx context.Context, path, inode string, offset int64) {
	s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO log_cursors (path, inode, offset) VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET inode = excluded.inode, offset = excluded.offset`,
		path, inode, offset)
}

// ------------------------------------------------------------- the list

// Sources the combined list has besides the machine's: what the panel ran, the
// sign-ins to it, and SSH.
const (
	EventsPanel  = "panel"
	EventsLogins = "panel-login"
	EventsSSH    = "ssh"
)

// EventFilter narrows the combined list.
type EventFilter struct {
	// Scope is "server" for what the machine's programs logged, "panel" for
	// what the panel ran, its sign-ins and SSH, or "" for both.
	Scope string

	Since  int64  // Unix seconds; 0 for everything kept
	Source string // "" for all, a server source, or one of the three above
	Level  string // "", "warning" (and worse) or "error" (and worse)
	Site   string // a domain
	Search string // in the message, address, user or action
}

// EventEntry is one line of the combined list: exactly one of the four is set.
type EventEntry struct {
	Source string        `json:"source"`
	Task   *Task         `json:"task,omitempty"`
	Login  *LoginAttempt `json:"login,omitempty"`
	SSH    *SSHEvent     `json:"ssh,omitempty"`
	Event  *ServerEvent  `json:"event,omitempty"`
}

// eventParts are the four tables as one shape — source, id, time, level —
// each with the filter's WHERE, and the arguments it needs.
func eventParts(filter EventFilter) ([]string, []any) {
	var (
		parts []string
		args  []any
	)

	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(filter.Search) + "%"
	search := filter.Search != ""

	levels := map[string]string{
		"warning": `('warning','error','critical')`,
		"error":   `('error','critical')`,
	}[filter.Level]

	want := func(source string) bool {
		if filter.Scope == "server" && source != "server" || filter.Scope == "panel" && source == "server" {
			return false
		}

		switch filter.Source {
		case "":
			return true
		case EventsPanel, EventsLogins, EventsSSH:
			return filter.Source == source
		default:
			return source == "server"
		}
	}

	// What the panel ran.
	if want(EventsPanel) {
		where := []string{`CAST(strftime('%s', created_at) AS INTEGER) >= ?`}
		partArgs := []any{filter.Since}

		level := `CASE status WHEN 'failed' THEN 'error' ELSE 'info' END`
		if levels != "" {
			where = append(where, level+` IN `+levels)
		}
		if filter.Site != "" {
			where = append(where, `site_id IN (SELECT id FROM sites WHERE domain = ?)`)
			partArgs = append(partArgs, filter.Site)
		}
		if search {
			where = append(where, `(action LIKE ? ESCAPE '\' OR coalesce(message, '') LIKE ? ESCAPE '\')`)
			partArgs = append(partArgs, like, like)
		}

		parts = append(parts, `SELECT 'task' AS source, id, CAST(strftime('%s', created_at) AS INTEGER) AS at
			FROM activity_logs WHERE `+strings.Join(where, " AND "))
		args = append(args, partArgs...)
	}

	// The sign-ins to the panel and SSH have no site.
	if filter.Site == "" {
		if want(EventsLogins) {
			where := []string{`created_at >= ?`}
			partArgs := []any{filter.Since}

			if levels != "" {
				where = append(where, `CASE success WHEN 1 THEN 'info' ELSE 'warning' END IN `+levels)
			}
			if search {
				where = append(where, `(coalesce(email, '') LIKE ? ESCAPE '\' OR ip_address LIKE ? ESCAPE '\')`)
				partArgs = append(partArgs, like, like)
			}

			parts = append(parts, `SELECT 'panel' AS source, id, created_at AS at FROM login_attempts WHERE `+
				strings.Join(where, " AND "))
			args = append(args, partArgs...)
		}

		if want(EventsSSH) {
			where := []string{`created_at >= ?`}
			partArgs := []any{filter.Since}

			if levels != "" {
				where = append(where, `CASE kind WHEN 'accepted' THEN 'info' ELSE 'warning' END IN `+levels)
			}
			if search {
				where = append(where, `(username LIKE ? ESCAPE '\' OR ip_address LIKE ? ESCAPE '\')`)
				partArgs = append(partArgs, like, like)
			}

			parts = append(parts, `SELECT 'ssh' AS source, id, created_at AS at FROM ssh_events WHERE `+
				strings.Join(where, " AND "))
			args = append(args, partArgs...)
		}
	}

	// What the machine's programs logged.
	if want("server") {
		where := []string{`created_at >= ?`}
		partArgs := []any{filter.Since}

		if filter.Source != "" {
			where = append(where, `source = ?`)
			partArgs = append(partArgs, filter.Source)
		}
		if levels != "" {
			where = append(where, `level IN `+levels)
		}
		if filter.Site != "" {
			where = append(where, `site = ?`)
			partArgs = append(partArgs, filter.Site)
		}
		if search {
			where = append(where, `(message LIKE ? ESCAPE '\' OR ip_address LIKE ? ESCAPE '\')`)
			partArgs = append(partArgs, like, like)
		}

		parts = append(parts, `SELECT 'event' AS source, id, created_at AS at FROM server_events WHERE `+
			strings.Join(where, " AND "))
		args = append(args, partArgs...)
	}

	return parts, args
}

// Events is one page of the combined list, newest first, and how many entries
// match in all.
//
// Each table is limited to the rows the page could need before they are put
// together, so a page near the top of a month of events sorts a page's worth
// from each table rather than the month.
func (s *Store) Events(ctx context.Context, filter EventFilter, limit, offset int) ([]EventEntry, int, error) {
	ctx = ctxOrBackground(ctx)

	parts, args := eventParts(filter)
	if len(parts) == 0 {
		return []EventEntry{}, 0, nil
	}

	total := 0
	for i, part := range parts {
		count := 0
		partArgs := argsFor(parts, args, i)

		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (`+part+`)`, partArgs...).Scan(&count); err != nil {
			return nil, 0, err
		}

		total += count
	}

	limited := make([]string, len(parts))
	for i, part := range parts {
		limited[i] = `SELECT * FROM (` + part + ` ORDER BY at DESC, id DESC LIMIT ` +
			strconv.Itoa(max(0, limit+offset)) + `)`
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT source, id FROM (`+strings.Join(limited, " UNION ALL ")+`)
		  ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
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

	tasks, err := s.tasksByID(ctx, ids["task"])
	if err != nil {
		return nil, 0, err
	}
	logins, err := s.loginsByID(ctx, ids["panel"])
	if err != nil {
		return nil, 0, err
	}
	ssh, err := s.sshEventsByID(ctx, ids["ssh"])
	if err != nil {
		return nil, 0, err
	}
	events, err := s.serverEventsByID(ctx, ids["event"])
	if err != nil {
		return nil, 0, err
	}

	entries := make([]EventEntry, 0, len(refs))

	for _, ref := range refs {
		entry := EventEntry{Source: ref.source}

		switch ref.source {
		case "task":
			entry.Task = tasks[ref.id]
		case "panel":
			entry.Login = logins[ref.id]
		case "ssh":
			entry.SSH = ssh[ref.id]
		case "event":
			entry.Event = events[ref.id]
		}

		if entry.Task != nil || entry.Login != nil || entry.SSH != nil || entry.Event != nil {
			entries = append(entries, entry)
		}
	}

	return entries, total, nil
}

// argsFor is the slice of args the i-th part uses: each part's placeholders
// are counted in its text.
func argsFor(parts []string, args []any, i int) []any {
	start := 0
	for j := 0; j < i; j++ {
		start += strings.Count(parts[j], "?")
	}

	return args[start : start+strings.Count(parts[i], "?")]
}

// EventCounts is how many entries each source has since a time, for the
// page's filter chips.
func (s *Store) EventCounts(ctx context.Context, since int64) map[string]int {
	ctx = ctxOrBackground(ctx)
	counts := map[string]int{}

	single := map[string]string{
		EventsPanel:  `SELECT count(*) FROM activity_logs WHERE created_at >= datetime(?, 'unixepoch')`,
		EventsLogins: `SELECT count(*) FROM login_attempts WHERE created_at >= ?`,
		EventsSSH:    `SELECT count(*) FROM ssh_events WHERE created_at >= ?`,
	}

	for source, query := range single {
		count := 0
		s.db.QueryRowContext(ctx, query, since).Scan(&count)
		counts[source] = count
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT source, count(*) FROM server_events WHERE created_at >= ? GROUP BY source`, since)
	if err != nil {
		return counts
	}
	defer rows.Close()

	for rows.Next() {
		var (
			source string
			count  int
		)
		if rows.Scan(&source, &count) == nil {
			counts[source] = count
		}
	}

	return counts
}

// EventSites are the sites the machine's events name, for the site filter.
func (s *Store) EventSites(ctx context.Context) []string {
	sites := []string{}

	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT DISTINCT site FROM server_events WHERE site != '' ORDER BY site`)
	if err != nil {
		return sites
	}
	defer rows.Close()

	for rows.Next() {
		var site string
		if rows.Scan(&site) == nil {
			sites = append(sites, site)
		}
	}

	return sites
}

func (s *Store) serverEventsByID(ctx context.Context, ids []any) (map[int64]*ServerEvent, error) {
	found := map[int64]*ServerEvent{}
	if len(ids) == 0 {
		return found, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, source, site, level, ip_address, message
		   FROM server_events WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		event := &ServerEvent{}
		if err := rows.Scan(&event.ID, &event.CreatedAt, &event.Source, &event.Site,
			&event.Level, &event.IP, &event.Message); err != nil {
			return nil, err
		}
		found[event.ID] = event
	}

	return found, rows.Err()
}

// ClearServerEvents deletes the machine's events older than before — all of
// them for a zero time — from the sources named, or every source when none
// is, and says how many went.
func (s *Store) ClearServerEvents(ctx context.Context, sources []string, before time.Time) int64 {
	cutoff := before.Unix()
	if before.IsZero() {
		cutoff = time.Now().Add(time.Hour).Unix()
	}

	query := `DELETE FROM server_events WHERE created_at < ?`
	args := []any{cutoff}

	if len(sources) > 0 {
		query += ` AND source IN (` + placeholders(len(sources)) + `)`
		for _, source := range sources {
			args = append(args, source)
		}
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx), query, args...)
	if err != nil {
		return 0
	}

	count, _ := result.RowsAffected()

	return count
}

// LatestServerEventID is the newest event's id, or 0.
func (s *Store) LatestServerEventID(ctx context.Context) int64 {
	var id int64
	s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT coalesce(max(id), 0) FROM server_events`).Scan(&id)

	return id
}

// ServerEventsAfter are the events after an id at one of the levels named,
// oldest first, at most limit of them, and how many there are in all.
func (s *Store) ServerEventsAfter(ctx context.Context, after int64, levels []string, limit int) ([]ServerEvent, int) {
	ctx = ctxOrBackground(ctx)

	args := []any{after}
	for _, level := range levels {
		args = append(args, level)
	}
	where := `id > ? AND level IN (` + placeholders(len(levels)) + `)`

	total := 0
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM server_events WHERE `+where, args...).Scan(&total)

	events := []ServerEvent{}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, source, site, level, ip_address, message
		   FROM server_events WHERE `+where+` ORDER BY id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return events, total
	}
	defer rows.Close()

	for rows.Next() {
		var event ServerEvent
		if rows.Scan(&event.ID, &event.CreatedAt, &event.Source, &event.Site, &event.Level,
			&event.IP, &event.Message) == nil {
			events = append(events, event)
		}
	}

	return events, total
}

// ForgetServerEvents deletes the stored events of the sources named whose
// message contains one of hints and that drop says to drop: a filter added
// later, applied to what was collected before it.
func (s *Store) ForgetServerEvents(ctx context.Context, sources, hints []string, drop func(source, message string) bool) int64 {
	if len(sources) == 0 || len(hints) == 0 {
		return 0
	}

	query := `SELECT id, source, message FROM server_events WHERE source IN (` + placeholders(len(sources)) + `) AND (`
	args := []any{}
	for _, source := range sources {
		args = append(args, source)
	}
	for i, hint := range hints {
		if i > 0 {
			query += ` OR `
		}
		query += `instr(message, ?) > 0`
		args = append(args, hint)
	}
	query += `)`

	rows, err := s.db.QueryContext(ctxOrBackground(ctx), query, args...)
	if err != nil {
		return 0
	}

	ids := []int64{}
	for rows.Next() {
		var id int64
		var source, message string
		if rows.Scan(&id, &source, &message) == nil && drop(source, message) {
			ids = append(ids, id)
		}
	}
	rows.Close()

	var count int64
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		params := make([]any, len(batch))
		for i, id := range batch {
			params[i] = id
		}
		if result, err := s.db.ExecContext(ctxOrBackground(ctx),
			`DELETE FROM server_events WHERE id IN (`+placeholders(len(batch))+`)`, params...); err == nil {
			n, _ := result.RowsAffected()
			count += n
		}
	}

	return count
}
