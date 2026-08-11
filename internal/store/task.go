package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// maxTaskOutput is where a console is truncated. Kept from the front, so what
// the page shows is the tail — which is where the failure is.
const maxTaskOutput = 400_000

// Task is one deployment, install or mail operation, with the console the
// browser polls. It is stored in activity_logs, which is the name the Laravel
// panel gave the table.
type Task struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"-"`
	SiteID      int64  `json:"-"`
	Type        string `json:"type"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Output      string `json:"output"`
	Steps       string `json:"-"`
	CurrentStep string `json:"current_step"`
	Progress    int    `json:"progress"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	CreatedAt   string `json:"created_at"`
}

// TaskStep is one line in the progress list the page renders.
type TaskStep struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // pending | running | success | failed | skipped
	Optional bool   `json:"optional"`

	// Which app this step belongs to. A batch install interleaves a dozen
	// apps' steps in one task, and this is what lets each app's card show the
	// step that is actually its own rather than "queued" throughout.
	Group string `json:"group"`
}

// CreateTask opens a task in the running state.
func (s *Store) CreateTask(ctx context.Context, kind, action string, userID, siteID int64, message string) (int64, error) {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO activity_logs (user_id, site_id, type, action, status, message, output, steps, progress, started_at)
		 VALUES (?, ?, ?, ?, 'running', ?, '', '[]', 0, datetime('now'))`,
		nullID(userID), nullID(siteID), kind, action, message)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// FindTask loads one.
func (s *Store) FindTask(ctx context.Context, id int64) (*Task, error) {
	task := &Task{}

	var siteID, userID sql.NullInt64
	var message, output, steps, currentStep, startedAt, finishedAt text

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT id, user_id, site_id, type, action, status, message, output, steps,
		        current_step, progress, started_at, finished_at, created_at
		   FROM activity_logs WHERE id = ?`, id,
	).Scan(&task.ID, &userID, &siteID, &task.Type, &task.Action, &task.Status,
		&message, &output, &steps, &currentStep, &task.Progress,
		&startedAt, &finishedAt, &task.CreatedAt)

	if err != nil {
		return nil, notFound(err)
	}

	task.UserID = userID.Int64
	task.SiteID = siteID.Int64
	task.Message = message.String()
	task.Output = output.String()
	task.Steps = steps.String()
	task.CurrentStep = currentStep.String()
	task.StartedAt = startedAt.String()
	task.FinishedAt = finishedAt.String()

	return task, nil
}

// LatestTaskID is the most recent task of a type, running or not. Zero when
// there is none.
func (s *Store) LatestTaskID(ctx context.Context, kind string, runningOnly bool) int64 {
	query := `SELECT id FROM activity_logs WHERE type = ?`
	if runningOnly {
		query += ` AND status = 'running'`
	}
	query += ` ORDER BY id DESC LIMIT 1`

	var id int64
	s.db.QueryRowContext(ctxOrBackground(ctx), query, kind).Scan(&id)

	return id
}

// LatestTaskByAction narrows that to one action — several things share a type,
// and a page that only drives one of them should not show another's console.
func (s *Store) LatestTaskByAction(ctx context.Context, kind, action string, runningOnly bool) int64 {
	return s.LatestTaskByActions(ctx, kind, []string{action}, runningOnly)
}

// LatestTaskByActions is the same for a page that drives more than one action —
// the Apps page installs and uninstalls, and wants either.
func (s *Store) LatestTaskByActions(ctx context.Context, kind string, actions []string, runningOnly bool) int64 {
	if len(actions) == 0 {
		return s.LatestTaskID(ctx, kind, runningOnly)
	}

	query := `SELECT id FROM activity_logs WHERE type = ?` +
		` AND action IN (?` + strings.Repeat(", ?", len(actions)-1) + `)`

	args := []any{kind}
	for _, action := range actions {
		args = append(args, action)
	}

	if runningOnly {
		query += ` AND status = 'running'`
	}
	query += ` ORDER BY id DESC LIMIT 1`

	var id int64
	s.db.QueryRowContext(ctxOrBackground(ctx), query, args...).Scan(&id)

	return id
}

// RecentTaskWindow is how long a finished task stays on the page that started
// it.
//
// A console that disappears the instant the last step lands takes the result
// with it, and one that never disappears is furniture: the Apps page was
// opening with a nineteen-hour-old "completed 4 steps" card above the
// catalogue, on every visit, for ever.
const RecentTaskWindow = 10 * time.Minute

// RecentTaskID is the task a page should be showing: the one that is running,
// or the one that finished within the window. Zero when there is neither.
//
// Actions narrow it to the page's own work — several unrelated things share
// the `provision` type, and the Apps page has no business showing the console
// of a panel domain change made from Settings.
func (s *Store) RecentTaskID(ctx context.Context, kind string, actions []string, within time.Duration) int64 {
	query := `SELECT id FROM activity_logs WHERE type = ?`
	args := []any{kind}

	if len(actions) > 0 {
		query += ` AND action IN (?` + strings.Repeat(", ?", len(actions)-1) + `)`

		for _, action := range actions {
			args = append(args, action)
		}
	}

	// finished_at is UTC, written by SQLite's own clock, so the comparison is
	// made by the same clock rather than by the panel's idea of the time.
	query += ` AND (status = 'running' OR finished_at >= datetime('now', ?)) ORDER BY id DESC LIMIT 1`
	args = append(args, fmt.Sprintf("-%d seconds", int(within.Seconds())))

	var id int64
	s.db.QueryRowContext(ctxOrBackground(ctx), query, args...).Scan(&id)

	return id
}

// LatestTaskForSite is what the site page shows while something is deploying.
func (s *Store) LatestTaskForSite(ctx context.Context, siteID int64, runningOnly bool) int64 {
	query := `SELECT id FROM activity_logs WHERE site_id = ?`
	if runningOnly {
		query += ` AND status = 'running'`
	}
	query += ` ORDER BY id DESC LIMIT 1`

	var id int64
	s.db.QueryRowContext(ctxOrBackground(ctx), query, siteID).Scan(&id)

	return id
}

// TasksForSite is what the site page lists: this site's history and nobody
// else's.
func (s *Store) TasksForSite(ctx context.Context, siteID int64, limit int) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT id, type, action, status, message, current_step, progress,
		        started_at, finished_at, created_at
		   FROM activity_logs WHERE site_id = ? ORDER BY id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}

	return scanTaskList(rows)
}

// RecentTasks is the Logs page.
func (s *Store) RecentTasks(ctx context.Context, limit int) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT id, type, action, status, message, current_step, progress,
		        started_at, finished_at, created_at
		   FROM activity_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}

	return scanTaskList(rows)
}

// scanTaskList reads a list of tasks without their console: a page that renders
// twenty-five deployments of a Next.js site does not want twenty-five npm
// builds' worth of output in its payload.
func scanTaskList(rows *sql.Rows) ([]*Task, error) {
	defer rows.Close()

	var tasks []*Task

	for rows.Next() {
		task := &Task{}
		var message, currentStep, startedAt, finishedAt text

		if err := rows.Scan(&task.ID, &task.Type, &task.Action, &task.Status,
			&message, &currentStep, &task.Progress,
			&startedAt, &finishedAt, &task.CreatedAt); err != nil {
			return nil, err
		}

		task.Message = message.String()
		task.CurrentStep = currentStep.String()
		task.StartedAt = startedAt.String()
		task.FinishedAt = finishedAt.String()

		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// AppendTaskOutput adds a chunk to the console.
//
// The trim happens in SQL so that two workers writing to the same task cannot
// read-modify-write over each other: an apt install and its own progress bar
// are the same task from two goroutines often enough.
func (s *Store) AppendTaskOutput(id int64, chunk string) {
	if chunk == "" {
		return
	}

	s.db.Exec(
		`UPDATE activity_logs
		    SET output = substr(coalesce(output, '') || ?, -?),
		        updated_at = datetime('now')
		  WHERE id = ?`,
		chunk, maxTaskOutput, id)
}

// SetTaskStatus moves a task without finishing it.
func (s *Store) SetTaskStatus(id int64, status, message string) {
	s.db.Exec(
		`UPDATE activity_logs SET status = ?, message = ?, updated_at = datetime('now') WHERE id = ?`,
		status, message, id)
}

// SetTaskSteps replaces the progress list.
func (s *Store) SetTaskSteps(id int64, steps []TaskStep, current string, progress int) {
	encoded, err := json.Marshal(steps)
	if err != nil {
		return
	}

	s.db.Exec(
		`UPDATE activity_logs SET steps = ?, current_step = ?, progress = ?, updated_at = datetime('now')
		  WHERE id = ?`,
		string(encoded), current, progress, id)
}

// FinishTask closes it.
func (s *Store) FinishTask(id int64, status, message string, progress int) {
	s.db.Exec(
		`UPDATE activity_logs
		    SET status = ?, message = ?, progress = ?, current_step = NULL,
		        finished_at = datetime('now'), updated_at = datetime('now')
		  WHERE id = ?`,
		status, message, progress, id)
}

// FailRunningTasks marks whatever was running when the panel stopped. A task
// row that says "running" for a process that no longer exists is worse than a
// failure: the page waits on it forever.
func (s *Store) FailRunningTasks() {
	s.db.Exec(
		`UPDATE activity_logs
		    SET status = 'failed',
		        message = coalesce(nullif(message, ''), 'The panel restarted while this was running.'),
		        finished_at = datetime('now')
		  WHERE status = 'running'`)
}

// Console is the payload the live console component polls for. offset trims
// the output the browser already has; an offset past the end asks for nothing,
// which is how a caller that wants the status line and not a hundred kilobytes
// of apt says so.
func (t *Task) Console(offset int) map[string]any {
	if t == nil {
		return nil
	}

	output := t.Output
	length := len(output)

	if offset > 0 {
		if offset <= length {
			output = output[offset:]
		} else {
			output = ""
		}
	}

	var steps []TaskStep
	if strings.TrimSpace(t.Steps) != "" {
		json.Unmarshal([]byte(t.Steps), &steps)
	}
	if steps == nil {
		steps = []TaskStep{}
	}

	return map[string]any{
		"id":           t.ID,
		"type":         t.Type,
		"action":       t.Action,
		"status":       t.Status,
		"message":      t.Message,
		"output":       output,
		"offset":       length,
		"steps":        steps,
		"current_step": t.CurrentStep,
		"progress":     t.Progress,
		"running":      t.Status == "running",
		"started_at":   t.StartedAt,
		"finished_at":  t.FinishedAt,
		"created_at":   t.CreatedAt,
	}
}

// TaskConsole is the same thing by id, and null when there is no such task —
// which is what every page prop of this shape starts out as.
func (s *Store) TaskConsole(ctx context.Context, id int64, offset int) map[string]any {
	if id <= 0 {
		return nil
	}

	task, err := s.FindTask(ctx, id)
	if err != nil {
		return nil
	}

	return task.Console(offset)
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}

	return id
}
