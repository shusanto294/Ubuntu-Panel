package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ErrNoUser is returned when a lookup finds nobody. Callers checking a sign-in
// must treat it exactly like a wrong password, or the difference tells an
// attacker which addresses have accounts.
var ErrNoUser = errors.New("no such user")

// User is an account that can sign in to the panel.
type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// The roles an account can have.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// IsAdmin is whether the account may change things. An empty role is an
// account read before the column existed, which was an admin.
func (u *User) IsAdmin() bool { return u.Role != RoleViewer }

// UserCount says how many accounts exist. Zero is what sends a fresh install
// to the setup form instead of the sign-in page.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int

	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count)

	return count, err
}

// FindUser looks an account up by id.
func (s *Store) FindUser(ctx context.Context, id int64) (*User, error) {
	user := &User{}

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role FROM users WHERE id = ?`, id,
	).Scan(&user.ID, &user.Name, &user.Email, &user.Role)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoUser
	}

	return user, err
}

// CreateUser stores an account with a bcrypt hash of the password. The address
// is lower-cased so that signing in is not case-sensitive on a column that is.
func (s *Store) CreateUser(ctx context.Context, name, email, password string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	email = strings.ToLower(strings.TrimSpace(email))

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, email, password) VALUES (?, ?, ?)`,
		name, email, string(hash))
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &User{ID: id, Name: name, Email: email, Role: RoleAdmin}, nil
}

// Authenticate checks an address and password together.
//
// The hash comparison runs even when no account matched, against a fixed
// hash. Without it, a wrong address returns in microseconds and a wrong
// password in the tens of milliseconds bcrypt costs — which is a reliable way
// to enumerate who has an account.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	const decoy = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

	user := &User{}
	hash := decoy

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, role, password FROM users WHERE lower(email) = lower(?)`,
		strings.TrimSpace(email),
	).Scan(&user.ID, &user.Name, &user.Email, &user.Role, &hash)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	found := err == nil

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || !found {
		return nil, ErrNoUser
	}

	return user, nil
}

// UpdatePassword replaces an account's password.
func (s *Store) UpdatePassword(ctx context.Context, id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET password = ?, updated_at = datetime('now') WHERE id = ?`,
		string(hash), id)

	return err
}

// UpdateProfile changes the name and address on an account.
func (s *Store) UpdateProfile(ctx context.Context, id int64, name, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET name = ?, email = ?, updated_at = datetime('now') WHERE id = ?`,
		name, strings.ToLower(strings.TrimSpace(email)), id)

	return err
}

// CheckPassword verifies a password for an account that is already signed in,
// which is what guards a password change.
func (s *Store) CheckPassword(ctx context.Context, id int64, password string) bool {
	var hash string

	if err := s.db.QueryRowContext(ctx,
		`SELECT password FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
		return false
	}

	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Users lists every account, oldest first.
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, email, role FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []*User{}
	for rows.Next() {
		user := &User{}
		if err := rows.Scan(&user.ID, &user.Name, &user.Email, &user.Role); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

// SetUserRole changes what an account may do.
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = datetime('now') WHERE id = ?`, role, id)
	return err
}

// DeleteUser removes an account and signs it out everywhere.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// AdminCount is how many accounts may change things: never fewer than one.
func (s *Store) AdminCount(ctx context.Context) int {
	var count int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role != ?`, RoleViewer).Scan(&count)
	return count
}
