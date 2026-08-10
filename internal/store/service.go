package store

import (
	"context"
	"time"
)

// The statuses a catalogue entry's row can hold.
const (
	ServiceNotInstalled = "not_installed"
	ServiceQueued       = "queued"
	ServiceInstalling   = "installing"
	ServiceInstalled    = "installed"
	ServiceFailed       = "failed"
)

// Service is one row of the apps table: what the machine has, and what is
// being installed onto it. The catalogue metadata lives in the catalog
// package; this is only the state.
type Service struct {
	Key         string
	Status      string
	Version     string
	LastError   string
	TaskID      int64
	SortOrder   int
	QueuedAt    string
	InstalledAt string
	DetectedAt  string
}

// EnsureService creates the row for a catalogue entry if it is missing.
// Servers set up before an app joined the catalogue get theirs here.
func (s *Store) EnsureService(key string, sortOrder int) {
	s.db.Exec(
		`INSERT INTO services (key, status, sort_order) VALUES (?, 'not_installed', ?)
		 ON CONFLICT(key) DO UPDATE SET sort_order = excluded.sort_order`,
		key, sortOrder)
}

// FindService reads one row.
func (s *Store) FindService(ctx context.Context, key string) (*Service, error) {
	service := &Service{Key: key}

	var version, lastError, queuedAt, installedAt, detectedAt text
	var taskID nullInt

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT status, version, last_error, task_id, sort_order, queued_at, installed_at, detected_at
		   FROM services WHERE key = ?`, key,
	).Scan(&service.Status, &version, &lastError, &taskID, &service.SortOrder,
		&queuedAt, &installedAt, &detectedAt)
	if err != nil {
		return nil, notFound(err)
	}

	service.Version = version.String()
	service.LastError = lastError.String()
	service.TaskID = taskID.Int64
	service.QueuedAt = queuedAt.String()
	service.InstalledAt = installedAt.String()
	service.DetectedAt = detectedAt.String()

	return service, nil
}

// Services reads every row, in catalogue order.
func (s *Store) Services(ctx context.Context) (map[string]*Service, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT key, status, version, last_error, task_id, sort_order, queued_at, installed_at, detected_at
		   FROM services ORDER BY sort_order, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	services := map[string]*Service{}

	for rows.Next() {
		service := &Service{}

		var version, lastError, queuedAt, installedAt, detectedAt text
		var taskID nullInt

		if err := rows.Scan(&service.Key, &service.Status, &version, &lastError, &taskID,
			&service.SortOrder, &queuedAt, &installedAt, &detectedAt); err != nil {
			return nil, err
		}

		service.Version = version.String()
		service.LastError = lastError.String()
		service.TaskID = taskID.Int64
		service.QueuedAt = queuedAt.String()
		service.InstalledAt = installedAt.String()
		service.DetectedAt = detectedAt.String()

		services[service.Key] = service
	}

	return services, rows.Err()
}

// ServiceInstalled is the question every page that needs a runtime asks.
func (s *Store) ServiceInstalled(key string) bool {
	var one int

	err := s.db.QueryRow(`SELECT 1 FROM services WHERE key = ? AND status = 'installed'`, key).Scan(&one)

	return err == nil
}

// InstalledServiceKeys is the list the pages render.
func (s *Store) InstalledServiceKeys(ctx context.Context) []string {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT key FROM services WHERE status = 'installed' ORDER BY sort_order, key`)
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	keys := []string{}

	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}

	return keys
}

// SetServiceStatus records a probe or an install result. An empty version
// leaves the stored one alone, so a failed re-probe does not erase what is
// known about a working install.
func (s *Store) SetServiceStatus(key, status, version, lastError string) {
	installedAt := any(nil)
	if status == ServiceInstalled {
		installedAt = time.Now().UTC().Format("2006-01-02 15:04:05")
	}

	s.db.Exec(
		`INSERT INTO services (key, status, version, last_error, detected_at, installed_at)
		 VALUES (?, ?, ?, ?, datetime('now'), ?)
		 ON CONFLICT(key) DO UPDATE SET
		     status       = excluded.status,
		     version      = coalesce(nullif(excluded.version, ''), services.version),
		     last_error   = excluded.last_error,
		     detected_at  = datetime('now'),
		     installed_at = coalesce(excluded.installed_at, services.installed_at),
		     updated_at   = datetime('now')`,
		key, status, version, nullText(lastError), installedAt)
}

// SetServiceTask links the row to the task installing it, so the Apps page can
// show the console.
func (s *Store) SetServiceTask(key string, taskID int64) {
	s.db.Exec(`UPDATE services SET task_id = ?, updated_at = datetime('now') WHERE key = ?`,
		nullID(taskID), key)
}

// QueueService moves a row to queued, and reports whether it moved. Something
// already installed only moves when force says so.
func (s *Store) QueueService(key string, force bool) bool {
	query := `UPDATE services SET status = 'queued', last_error = NULL,
	                 queued_at = datetime('now'), updated_at = datetime('now')
	           WHERE key = ? AND status NOT IN ('queued', 'installing')`

	if !force {
		query += ` AND status != 'installed'`
	}

	result, err := s.db.Exec(query, key)
	if err != nil {
		return false
	}

	changed, _ := result.RowsAffected()

	return changed > 0
}

// QueuedServices is what the installer picks up.
func (s *Store) QueuedServices(ctx context.Context) []string {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT key FROM services WHERE status = 'queued' ORDER BY sort_order, key`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var keys []string

	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}

	return keys
}

// ServicesPending reports whether anything is queued or installing, which is
// what keeps the Apps page refreshing itself.
func (s *Store) ServicesPending() bool {
	var one int

	err := s.db.QueryRow(
		`SELECT 1 FROM services WHERE status IN ('queued', 'installing') LIMIT 1`).Scan(&one)

	return err == nil
}

// ResetInstallingServices is run at startup: a row that says "installing" for
// a process that no longer exists never changes again on its own.
func (s *Store) ResetInstallingServices() {
	s.db.Exec(
		`UPDATE services SET status = 'failed',
		        last_error = coalesce(nullif(last_error, ''), 'The panel restarted during the install.')
		  WHERE status = 'installing'`)
}
