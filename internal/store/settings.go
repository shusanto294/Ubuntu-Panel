package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
)

// Setting reads a panel-wide setting, falling back when it is unset.
func (s *Store) Setting(key, fallback string) string {
	var value sql.NullString

	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil || !value.Valid || value.String == "" {
		return fallback
	}

	return value.String
}

// SettingBool reads a setting written by a checkbox.
func (s *Store) SettingBool(key string, fallback bool) bool {
	value := s.Setting(key, "")

	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}

	return fallback
}

// SetSetting stores a setting in the clear.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value, secret) VALUES (?, ?, 0)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, secret = 0, updated_at = datetime('now')`,
		key, value)

	return err
}

// ForgetSetting removes one.
func (s *Store) ForgetSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)

	return err
}

// SecretSetting reads a setting that was stored encrypted.
func (s *Store) SecretSetting(key string) string {
	return s.decrypt(s.Setting(key, ""))
}

// SetSecretSetting stores a setting encrypted with the application key.
func (s *Store) SetSecretSetting(key, value string) error {
	encrypted, err := s.encrypt(value)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`INSERT INTO settings (key, value, secret) VALUES (?, ?, 1)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, secret = 1, updated_at = datetime('now')`,
		key, encrypted)

	return err
}

// RememberSecret generates a secret once and then keeps returning the same
// one. The mail server's database password is the reason this exists: it has
// to survive a panel restart, and nobody should ever have to type it.
func (s *Store) RememberSecret(key string, length int) (string, error) {
	if existing := s.SecretSetting(key); existing != "" {
		return existing, nil
	}

	if s.cipher == nil || !s.cipher.Ready() {
		return "", crypto.ErrNoKey
	}

	value := crypto.Password(length)

	if err := s.SetSecretSetting(key, value); err != nil {
		return "", err
	}

	return value, nil
}

// PHPVersion and NodeVersion are the defaults new sites are created with.
func (s *Store) PHPVersion() string  { return s.Setting("php_version", "8.3") }
func (s *Store) NodeVersion() string { return s.Setting("node_version", "22") }

// ErrNotFound is what every model lookup returns for a row that is not there,
// so handlers can answer 404 without knowing which table was asked.
var ErrNotFound = errors.New("not found")

// notFound maps sql.ErrNoRows onto it.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}

	return err
}

// text is the scan target for a nullable TEXT column, and nullInt for a
// nullable INTEGER one. A column the schema allows to be NULL cannot be
// scanned straight into a string, and every model here has several.
type text struct{ sql.NullString }

// String is the value, empty when the column was NULL. The method shadows the
// promoted NullString.String field, which is what makes `column.String()` read
// the same everywhere.
func (t text) String() string { return t.NullString.String }

type nullInt = sql.NullInt64

// ctxOrBackground lets the model helpers be called from a queue worker that
// has no request context.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
