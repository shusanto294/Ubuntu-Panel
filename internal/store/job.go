package store

import (
	"context"
	"encoding/json"
	"time"
)

// Job is one piece of background work: a deployment, an install, a mail
// domain. It is a row rather than a goroutine so that a panel restart
// mid-deployment resumes the work rather than losing it.
type Job struct {
	ID       int64
	Kind     string
	Payload  string
	Attempts int
}

// PushJob enqueues work. payload is JSON.
func (s *Store) PushJob(kind string, payload any) (int64, error) {
	encoded := "{}"

	if payload != nil {
		bytes, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}

		encoded = string(bytes)
	}

	result, err := s.db.Exec(
		`INSERT INTO jobs (kind, payload, status, available_at) VALUES (?, ?, 'queued', ?)`,
		kind, encoded, time.Now().Unix())
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// QueuedJobs is the work still waiting, oldest first.
//
// A read of the queue rather than a claim on it: nothing here reserves a job,
// so calling it does not take work away from a worker. It exists so that the
// panel's own pages and an assistant over MCP can be checked to queue the same
// thing — the two paths are meant to be one, and this is how a test says so.
func (s *Store) QueuedJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT id, kind, payload, attempts FROM jobs WHERE status = 'queued' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*Job

	for rows.Next() {
		job := &Job{}

		if err := rows.Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempts); err != nil {
			return nil, err
		}

		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// ClaimJob takes the next job that is due.
//
// The claim is a conditional UPDATE, which is what stops two workers running
// the same deployment: only one UPDATE can match a given row in the reserved
// state, so the loser gets zero rows changed and goes back to sleep.
func (s *Store) ClaimJob(ctx context.Context) (*Job, error) {
	return s.ClaimJobOf(ctx, nil, false)
}

// ClaimJobOf claims the next job whose kind is one of kinds — or, with
// except, is none of them — so a lane of quick jobs never waits behind a
// deployment and a deployment worker never takes the quick ones.
func (s *Store) ClaimJobOf(ctx context.Context, kinds []string, except bool) (*Job, error) {
	now := time.Now().Unix()

	var job Job

	query := `SELECT id, kind, payload, attempts FROM jobs WHERE status = 'queued' AND available_at <= ?`
	args := []any{now}

	if len(kinds) > 0 {
		if except {
			query += ` AND kind NOT IN (` + placeholders(len(kinds)) + `)`
		} else {
			query += ` AND kind IN (` + placeholders(len(kinds)) + `)`
		}
		for _, kind := range kinds {
			args = append(args, kind)
		}
	}

	err := s.db.QueryRowContext(ctxOrBackground(ctx), query+` ORDER BY id LIMIT 1`, args...).
		Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempts)
	if err != nil {
		return nil, notFound(err)
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE jobs SET status = 'running', reserved_at = ?, attempts = attempts + 1
		  WHERE id = ? AND status = 'queued'`, now, job.ID)
	if err != nil {
		return nil, err
	}

	if changed, _ := result.RowsAffected(); changed == 0 {
		// Another worker got there first.
		return nil, ErrNotFound
	}

	job.Attempts++

	return &job, nil
}

// FinishJob removes a job that ran to completion. Keeping finished jobs would
// make the table the log, and the log already exists.
func (s *Store) FinishJob(id int64) {
	s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
}

// RetryJob puts a job back on the queue to run again after a delay.
func (s *Store) RetryJob(id int64, after time.Duration, reason string) {
	s.db.Exec(`UPDATE jobs SET status = 'queued', last_error = ?, available_at = ?, reserved_at = NULL WHERE id = ?`,
		reason, time.Now().Add(after).Unix(), id)
}

// FailJob records why a job did not finish and leaves the row behind.
func (s *Store) FailJob(id int64, reason string) {
	s.db.Exec(`UPDATE jobs SET status = 'failed', last_error = ? WHERE id = ?`, reason, id)
}

// RequeueRunningJobs is run at startup. A job marked running belongs to a
// process that no longer exists, so it goes back on the queue: every handler
// is written to be safe to run twice, because this is how a deployment
// survives a restart.
func (s *Store) RequeueRunningJobs() int64 {
	result, err := s.db.Exec(
		`UPDATE jobs SET status = 'queued', reserved_at = NULL WHERE status = 'running'`)
	if err != nil {
		return 0
	}

	changed, _ := result.RowsAffected()

	return changed
}

// Decode reads a job's payload.
func (job *Job) Decode(into any) error {
	return json.Unmarshal([]byte(job.Payload), into)
}
