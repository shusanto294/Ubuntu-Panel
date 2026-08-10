// Package store owns the SQLite database: opening it, keeping the schema up to
// date, and the queries the rest of the panel runs against it.
//
// The driver is modernc.org/sqlite, a pure-Go translation of SQLite. That is a
// deliberate trade: it is slower than the C library by a margin no control
// panel will ever notice, and in exchange the binary needs no cgo, which is
// what makes `GOOS=linux GOARCH=arm64 go build` work from any machine and the
// result run on a server with nothing installed on it.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is a handle on the panel's database.
type Store struct {
	db *sql.DB
}

// Open prepares the database file and applies the schema. The directory is
// created if it is missing, because the first start on a new machine happens
// before anything else has had a reason to make it.
func Open(path string) (*Store, error) {
	if directory := filepath.Dir(path); directory != "." && directory != "/" {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("creating %s: %w", directory, err)
		}
	}

	// WAL lets the request handlers read while the queue writes. busy_timeout
	// turns the lock contention that produces into a short wait instead of an
	// immediate SQLITE_BUSY, which is how a background job used to be able to
	// make a page fail to render.
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one write lock for the whole file. Allowing many Go
	// connections to queue against it buys nothing and turns a slow write into
	// a pile of blocked ones, so writes are funnelled through a small pool.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	store := &Store{db: db}

	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	// A read-only database file is the failure that used to be invisible: the
	// panel served pages normally and every form came back "your session
	// expired", because sessions were handed out and never stored. Finding out
	// at startup costs one temporary row.
	if err := store.checkWritable(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

// DB exposes the handle for packages that run their own queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) checkWritable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ('_writable_probe', '1')
		 ON CONFLICT(key) DO UPDATE SET value = '1'`)
	if err != nil {
		return fmt.Errorf("the database is not writable: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = '_writable_probe'`)
	if err != nil {
		return fmt.Errorf("the database is not writable: %w", err)
	}

	return nil
}
