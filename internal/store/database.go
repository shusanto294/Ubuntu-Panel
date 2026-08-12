package store

import (
	"context"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
)

// Database is a database the panel created on one of the engines installed on
// this machine, with the credentials it generated for it.
type Database struct {
	ID            int64
	UserID        int64
	Engine        string
	Name          string
	Username      string
	Password      string // decrypted
	Charset       string
	Status        string
	LastError     string
	ManagedBySite bool
	CreatedAt     string
}

const databaseColumns = `id, user_id, engine, name, username, password, charset,
	status, last_error, managed_by_site, created_at`

func (s *Store) scanDatabase(row siteScanner) (*Database, error) {
	database := &Database{}

	var userID nullInt
	var username, password, charset, lastError text

	err := row.Scan(&database.ID, &userID, &database.Engine, &database.Name,
		&username, &password, &charset, &database.Status, &lastError,
		&database.ManagedBySite, &database.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}

	database.UserID = userID.Int64
	database.Username = username.String()
	database.Password = s.decrypt(password.String())
	database.Charset = charset.String()
	database.LastError = lastError.String()

	return database, nil
}

// FindDatabase loads one.
func (s *Store) FindDatabase(ctx context.Context, id int64) (*Database, error) {
	return s.scanDatabase(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+databaseColumns+` FROM databases WHERE id = ?`, id))
}

// Databases is the index page.
func (s *Store) Databases(ctx context.Context) ([]*Database, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+databaseColumns+` FROM databases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var databases []*Database

	for rows.Next() {
		database, err := s.scanDatabase(rows)
		if err != nil {
			return nil, err
		}

		databases = append(databases, database)
	}

	return databases, rows.Err()
}

// DatabaseCount is what blocks removing MariaDB from under the databases the
// panel created on it.
func (s *Store) DatabaseCount(ctx context.Context, engine string) int {
	var count int

	if engine == "" {
		s.db.QueryRowContext(ctxOrBackground(ctx), `SELECT count(*) FROM databases`).Scan(&count)
	} else {
		s.db.QueryRowContext(ctxOrBackground(ctx),
			`SELECT count(*) FROM databases WHERE engine = ?`, engine).Scan(&count)
	}

	return count
}

// DatabaseExists reports whether a name is already taken on an engine.
func (s *Store) DatabaseExists(ctx context.Context, engine, name string) bool {
	var one int

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT 1 FROM databases WHERE engine = ? AND name = ?`, engine, name).Scan(&one)

	return err == nil
}

// CreateDatabase stores the row the provisioning step then acts on.
func (s *Store) CreateDatabase(ctx context.Context, database *Database) (int64, error) {
	password, err := s.encrypt(database.Password)
	if err != nil {
		return 0, err
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO databases (user_id, engine, name, username, password, charset, status, managed_by_site)
		 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		nullID(database.UserID), database.Engine, database.Name, database.Username,
		password, nullText(database.Charset), database.ManagedBySite)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// SetDatabaseStatus records where provisioning got to.
func (s *Store) SetDatabaseStatus(id int64, status, lastError string) {
	s.db.Exec(`UPDATE databases SET status = ?, last_error = ?, updated_at = datetime('now')
	           WHERE id = ?`, status, nullText(lastError), id)
}

// SetDatabasePassword records a password the engine has already accepted.
// Encrypted like the one the row was created with, so the column stays
// readable by the same key and by nothing else.
func (s *Store) SetDatabasePassword(id int64, password string) error {
	encrypted, err := s.encrypt(password)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE databases SET password = ?, updated_at = datetime('now')
	                    WHERE id = ?`, encrypted, id)

	return err
}

// DeleteDatabase removes the row once the engine has dropped the database.
func (s *Store) DeleteDatabase(id int64) {
	s.db.Exec(`DELETE FROM databases WHERE id = ?`, id)
}

// Summary is the shape the Databases page reads. The password is never in it:
// it is fetched on demand from the credentials endpoint, so it is not sitting
// in the page payload of every list.
func (database *Database) Summary() map[string]any {
	return map[string]any{
		"id":              database.ID,
		"engine":          database.Engine,
		"engine_label":    config.EngineLabel(database.Engine),
		"name":            database.Name,
		"username":        database.Username,
		"charset":         database.Charset,
		"status":          database.Status,
		"last_error":      database.LastError,
		"managed_by_site": database.ManagedBySite,
		"created_at":      database.CreatedAt,
	}
}
