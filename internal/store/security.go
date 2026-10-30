package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Reasons a sign-in attempt is recorded with.
const (
	LoginPassword  = "password"   // the address and password were checked
	LoginTwoFactor = "two_factor" // the second step was checked
	LoginRecovery  = "recovery"   // a recovery code was used for the second step
	LoginLocked    = "locked"     // refused without checking: too many failures
)

// LoginAttempt is one row of the sign-in history.
type LoginAttempt struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	Success   bool   `json:"success"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"created_at"`
}

// RecordLoginAttempt writes one down.
func (s *Store) RecordLoginAttempt(ctx context.Context, attempt LoginAttempt) error {
	if attempt.CreatedAt == 0 {
		attempt.CreatedAt = time.Now().Unix()
	}

	// What was typed into the address field is stored, bounded, because it is
	// the attacker's word list and worth reading. Never the password.
	if len(attempt.Email) > 190 {
		attempt.Email = attempt.Email[:190]
	}
	if len(attempt.UserAgent) > 300 {
		attempt.UserAgent = attempt.UserAgent[:300]
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO login_attempts (user_id, email, ip_address, user_agent, success, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullID(attempt.UserID), attempt.Email, attempt.IP, attempt.UserAgent,
		boolInt(attempt.Success), attempt.Reason, attempt.CreatedAt)

	return err
}

// FailedLoginsSince counts an address's failures inside a window, and says
// when the oldest of them was — which is when the lockout lifts.
//
// Only failures since the address last succeeded count: somebody who mistyped
// twice and then got in is not two strikes closer to being locked out tomorrow.
func (s *Store) FailedLoginsSince(ctx context.Context, ip string, since time.Time) (int, int64) {
	var (
		count  int
		oldest sql.NullInt64
	)

	s.db.QueryRowContext(ctx,
		`SELECT count(*), min(created_at) FROM login_attempts
		 WHERE ip_address = ? AND success = 0 AND reason != ? AND created_at >= ?
		   AND created_at > coalesce((SELECT max(created_at) FROM login_attempts
		                              WHERE ip_address = ? AND success = 1), 0)`,
		ip, LoginLocked, since.Unix(), ip,
	).Scan(&count, &oldest)

	return count, oldest.Int64
}

// LoginAttempts is the most recent history, newest first.
func (s *Store) LoginAttempts(ctx context.Context, limit int) ([]LoginAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, coalesce(user_id, 0), coalesce(email, ''), ip_address, coalesce(user_agent, ''),
		        success, reason, created_at
		 FROM login_attempts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attempts := []LoginAttempt{}

	for rows.Next() {
		var attempt LoginAttempt
		var success int

		if err := rows.Scan(&attempt.ID, &attempt.UserID, &attempt.Email, &attempt.IP,
			&attempt.UserAgent, &success, &attempt.Reason, &attempt.CreatedAt); err != nil {
			return nil, err
		}

		attempt.Success = success == 1
		attempts = append(attempts, attempt)
	}

	return attempts, rows.Err()
}

// LoginCounts is how many sign-ins worked and failed inside a window.
func (s *Store) LoginCounts(ctx context.Context, since time.Time) (succeeded, failed int) {
	s.db.QueryRowContext(ctx,
		`SELECT coalesce(sum(success = 1), 0), coalesce(sum(success = 0), 0)
		 FROM login_attempts WHERE created_at >= ?`, since.Unix(),
	).Scan(&succeeded, &failed)

	return succeeded, failed
}

// LockedAddresses are the addresses currently over the limit, and when each
// is let back in.
func (s *Store) LockedAddresses(ctx context.Context, limit int, window time.Duration) map[string]int64 {
	since := time.Now().Add(-window).Unix()

	rows, err := s.db.QueryContext(ctx,
		`SELECT ip_address, min(created_at) FROM login_attempts
		 WHERE success = 0 AND reason != ? AND created_at >= ?
		 GROUP BY ip_address HAVING count(*) >= ?`, LoginLocked, since, limit)
	if err != nil {
		return map[string]int64{}
	}
	defer rows.Close()

	locked := map[string]int64{}

	for rows.Next() {
		var ip string
		var oldest int64

		if rows.Scan(&ip, &oldest) == nil {
			// Re-checked one by one, because the query above does not know
			// about a success that reset the count.
			if count, first := s.FailedLoginsSince(ctx, ip, time.Unix(since, 0)); count >= limit {
				locked[ip] = first + int64(window.Seconds())
			}
		}
	}

	return locked
}

// ClearLoginFailures forgets an address's failures, which is what unlocking
// it from the Security page does.
func (s *Store) ClearLoginFailures(ctx context.Context, ip string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE ip_address = ? AND success = 0`, ip)

	return err
}

// LoginAttemptsAfter is everything recorded after an id, oldest first: what
// the alert watcher has not yet looked at.
func (s *Store) LoginAttemptsAfter(ctx context.Context, after int64) ([]LoginAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, coalesce(user_id, 0), coalesce(email, ''), ip_address, coalesce(user_agent, ''),
		        success, reason, created_at
		 FROM login_attempts WHERE id > ? ORDER BY id LIMIT 500`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attempts := []LoginAttempt{}

	for rows.Next() {
		var attempt LoginAttempt
		var success int

		if err := rows.Scan(&attempt.ID, &attempt.UserID, &attempt.Email, &attempt.IP,
			&attempt.UserAgent, &success, &attempt.Reason, &attempt.CreatedAt); err != nil {
			return nil, err
		}

		attempt.Success = success == 1
		attempts = append(attempts, attempt)
	}

	return attempts, rows.Err()
}

// LatestLoginAttemptID is where the alert watcher starts from on a fresh
// install, so the first run does not mail out the whole history.
func (s *Store) LatestLoginAttemptID(ctx context.Context) int64 {
	var id sql.NullInt64

	s.db.QueryRowContext(ctx, `SELECT max(id) FROM login_attempts`).Scan(&id)

	return id.Int64
}

// KnownLoginAddress says whether an account has signed in from an address
// before this attempt, which is what makes a sign-in worth an alert.
func (s *Store) KnownLoginAddress(ctx context.Context, userID int64, ip string, before int64) bool {
	var count int

	s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM login_attempts
		 WHERE user_id = ? AND ip_address = ? AND success = 1 AND id < ?`,
		userID, ip, before).Scan(&count)

	return count > 0
}

// ------------------------------------------------------------ two-factor

// TwoFactor is an account's second factor.
type TwoFactor struct {
	Secret    string
	Confirmed bool
	LastStep  int64
	Recovery  []string // hashes
}

// TwoFactorFor reads it.
func (s *Store) TwoFactorFor(ctx context.Context, userID int64) (TwoFactor, error) {
	var (
		secret, confirmed, recovery text
		factor                      TwoFactor
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT two_factor_secret, two_factor_confirmed_at, two_factor_recovery_codes, two_factor_last_step
		 FROM users WHERE id = ?`, userID,
	).Scan(&secret, &confirmed, &recovery, &factor.LastStep)

	if errors.Is(err, sql.ErrNoRows) {
		return factor, ErrNoUser
	}
	if err != nil {
		return factor, err
	}

	factor.Secret = s.decrypt(secret.String())
	factor.Confirmed = confirmed.String() != "" && factor.Secret != ""

	if stored := s.decrypt(recovery.String()); stored != "" {
		json.Unmarshal([]byte(stored), &factor.Recovery)
	}

	return factor, nil
}

// TwoFactorEnabled is the shortcut the sign-in and the pages ask.
func (s *Store) TwoFactorEnabled(ctx context.Context, userID int64) bool {
	factor, err := s.TwoFactorFor(ctx, userID)

	return err == nil && factor.Confirmed
}

// StartTwoFactor stores a fresh, unconfirmed secret. Enrolling again replaces
// whatever was there, confirmed or not, which is why it needs the password.
func (s *Store) StartTwoFactor(ctx context.Context, userID int64, secret string) error {
	encrypted, err := s.encrypt(secret)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_secret = ?, two_factor_confirmed_at = NULL,
		        two_factor_recovery_codes = NULL, two_factor_last_step = 0,
		        updated_at = datetime('now')
		 WHERE id = ?`, encrypted, userID)

	return err
}

// ConfirmTwoFactor switches it on, with the step the confirming code used and
// a new set of recovery codes, which it returns in the clear — the only time
// they are ever seen.
func (s *Store) ConfirmTwoFactor(ctx context.Context, userID, step int64) ([]string, error) {
	codes, hashes := newRecoveryCodes(8)

	encoded, _ := json.Marshal(hashes)

	encrypted, err := s.encrypt(string(encoded))
	if err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_confirmed_at = datetime('now'), two_factor_last_step = ?,
		        two_factor_recovery_codes = ?, updated_at = datetime('now')
		 WHERE id = ? AND two_factor_secret IS NOT NULL`, step, encrypted, userID)

	return codes, err
}

// RegenerateRecoveryCodes replaces the set, invalidating every old one.
func (s *Store) RegenerateRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes, hashes := newRecoveryCodes(8)

	encoded, _ := json.Marshal(hashes)

	encrypted, err := s.encrypt(string(encoded))
	if err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_recovery_codes = ?, updated_at = datetime('now')
		 WHERE id = ? AND two_factor_confirmed_at IS NOT NULL`, encrypted, userID)

	return codes, err
}

// UseTwoFactorStep records the step a code was accepted for. The update is
// conditional on the step being newer, so two requests racing with the same
// code cannot both win.
func (s *Store) UseTwoFactorStep(ctx context.Context, userID, step int64) bool {
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_last_step = ? WHERE id = ? AND two_factor_last_step < ?`,
		step, userID, step)
	if err != nil {
		return false
	}

	rows, _ := result.RowsAffected()

	return rows == 1
}

// UseRecoveryCode checks a recovery code and, if it is one, spends it.
func (s *Store) UseRecoveryCode(ctx context.Context, userID int64, code string) bool {
	factor, err := s.TwoFactorFor(ctx, userID)
	if err != nil || !factor.Confirmed {
		return false
	}

	presented := hashRecoveryCode(code)
	remaining := make([]string, 0, len(factor.Recovery))
	matched := false

	for _, hash := range factor.Recovery {
		if !matched && subtle.ConstantTimeCompare([]byte(hash), []byte(presented)) == 1 {
			matched = true
			continue
		}

		remaining = append(remaining, hash)
	}

	if !matched {
		return false
	}

	encoded, _ := json.Marshal(remaining)

	encrypted, err := s.encrypt(string(encoded))
	if err != nil {
		return false
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_recovery_codes = ? WHERE id = ?`, encrypted, userID)

	return err == nil
}

// DisableTwoFactor removes it entirely.
func (s *Store) DisableTwoFactor(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET two_factor_secret = NULL, two_factor_confirmed_at = NULL,
		        two_factor_recovery_codes = NULL, two_factor_last_step = 0,
		        updated_at = datetime('now')
		 WHERE id = ?`, userID)

	return err
}

// newRecoveryCodes makes codes shaped like a3f9k-2m8xq: ten characters from an
// alphabet with nothing that reads as something else, which is fifty bits each.
func newRecoveryCodes(count int) (codes, hashes []string) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"

	for range count {
		buffer := make([]byte, 10)
		rand.Read(buffer)

		code := make([]byte, 0, 11)
		for i, b := range buffer {
			if i == 5 {
				code = append(code, '-')
			}
			code = append(code, alphabet[int(b)%len(alphabet)])
		}

		codes = append(codes, string(code))
		hashes = append(hashes, hashRecoveryCode(string(code)))
	}

	return codes, hashes
}

// hashRecoveryCode is SHA-256 rather than bcrypt: each code is fifty random
// bits, which is not something a fast hash helps anybody guess, and checking
// one means checking up to eight.
func hashRecoveryCode(code string) string {
	code = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	if !strings.Contains(code, "-") && len(code) == 10 {
		code = code[:5] + "-" + code[5:]
	}

	sum := sha256.Sum256([]byte(code))

	return hex.EncodeToString(sum[:])
}

// -------------------------------------------------------------- sessions

// PanelSession is one browser signed in to an account.
type PanelSession struct {
	ID           string `json:"-"`
	Key          string `json:"key"`
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	LastActivity int64  `json:"last_activity"`
}

// SessionsFor lists an account's signed-in browsers, most recently used first.
// The id itself never leaves the server: it is the cookie, and a page that
// prints it is a page that leaks it. Key is a hash of it, which is enough to
// name one to revoke.
func (s *Store) SessionsFor(ctx context.Context, userID int64, since time.Time) ([]PanelSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, coalesce(ip_address, ''), coalesce(user_agent, ''), last_activity
		 FROM sessions WHERE user_id = ? AND last_activity >= ? ORDER BY last_activity DESC`,
		userID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []PanelSession{}

	for rows.Next() {
		var entry PanelSession

		if err := rows.Scan(&entry.ID, &entry.IP, &entry.UserAgent, &entry.LastActivity); err != nil {
			return nil, err
		}

		entry.Key = SessionKey(entry.ID)
		list = append(list, entry)
	}

	return list, rows.Err()
}

// SessionKey is the public name of a session id.
func SessionKey(id string) string {
	sum := sha256.Sum256([]byte("session:" + id))

	return hex.EncodeToString(sum[:8])
}

// RevokeSession signs one browser out, by key.
func (s *Store) RevokeSession(ctx context.Context, userID int64, key string) error {
	list, err := s.SessionsFor(ctx, userID, time.Unix(0, 0))
	if err != nil {
		return err
	}

	for _, entry := range list {
		if entry.Key == key {
			_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`,
				entry.ID, userID)
			return err
		}
	}

	return ErrNotFound
}

// RevokeOtherSessions signs every other browser out — what a password change
// and a "sign out everywhere else" both want.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, keep string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keep)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// IPCountries are the countries already known for these addresses, as
// two-letter codes. An address missing from the result has not been looked up
// in the last month; one mapped to "" was, and could not be placed.
func (s *Store) IPCountries(ctx context.Context, ips []string) map[string]string {
	known := map[string]string{}
	if len(ips) == 0 {
		return known
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ips)), ",")
	args := make([]any, 0, len(ips)+1)

	for _, ip := range ips {
		args = append(args, ip)
	}

	args = append(args, time.Now().Add(-30*24*time.Hour).Unix())

	rows, err := s.db.QueryContext(ctx,
		`SELECT ip, country FROM ip_countries WHERE ip IN (`+placeholders+`) AND checked_at >= ?`, args...)
	if err != nil {
		return known
	}
	defer rows.Close()

	for rows.Next() {
		var ip, country string

		if rows.Scan(&ip, &country) == nil {
			known[ip] = country
		}
	}

	return known
}

// SaveIPCountries remembers what a lookup found.
func (s *Store) SaveIPCountries(ctx context.Context, countries map[string]string) {
	now := time.Now().Unix()

	for ip, country := range countries {
		s.db.ExecContext(ctx,
			`INSERT INTO ip_countries (ip, country, checked_at) VALUES (?, ?, ?)
			 ON CONFLICT(ip) DO UPDATE SET country = excluded.country, checked_at = excluded.checked_at`,
			ip, country, now)
	}
}
