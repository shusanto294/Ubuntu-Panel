package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
)

// APIToken is a key an AI assistant signs its MCP requests with.
//
// The token itself is never stored: the row keeps a SHA-256 of it and the first
// few characters, which is enough to name it in a list and not enough to use.
// It is shown once, on the request that created it — the same arrangement every
// other panel uses for an API key, and the reason the page says to copy it now.
type APIToken struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"-"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	LastUsedAt string `json:"last_used_at"`
	CreatedAt  string `json:"created_at"`
}

// TokenPrefix is what every issued token starts with, so one pasted into the
// wrong box is recognisable as a panel key.
const TokenPrefix = "up_"

// NewAPIToken mints a token. The first return is the secret, which the caller
// has one chance to show; the second is what goes in the column.
func NewAPIToken() (secret, hash string) {
	secret = TokenPrefix + crypto.Token(32)

	return secret, HashAPIToken(secret)
}

// HashAPIToken is the stored form. A plain SHA-256 rather than bcrypt: the
// token is 32 random bytes, so there is no dictionary to run against it, and
// every MCP call has to look one up.
func HashAPIToken(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))

	return hex.EncodeToString(sum[:])
}

// TokenLabel is the shortened form the page lists: enough to tell two keys
// apart, and nothing that would let either be used.
func TokenLabel(secret string) string {
	trimmed := strings.TrimPrefix(secret, TokenPrefix)

	if len(trimmed) > 6 {
		trimmed = trimmed[:6]
	}

	return TokenPrefix + trimmed
}

// CreateAPIToken stores a minted token and returns its id.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, hash, prefix string) (int64, error) {
	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix) VALUES (?, ?, ?, ?)`,
		userID, name, hash, prefix)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// APITokens is the list the page renders, newest first.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT id, user_id, name, prefix, last_used_at, created_at
		   FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := []*APIToken{}

	for rows.Next() {
		token := &APIToken{}

		var lastUsed text

		if err := rows.Scan(&token.ID, &token.UserID, &token.Name, &token.Prefix,
			&lastUsed, &token.CreatedAt); err != nil {
			return nil, err
		}

		token.LastUsedAt = lastUsed.String()

		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

// FindAPIToken is the lookup every MCP request runs: the hash of what was sent,
// against the column. A miss is ErrNotFound rather than a nil row, because the
// caller answers 401 either way.
func (s *Store) FindAPIToken(ctx context.Context, hash string) (*APIToken, error) {
	token := &APIToken{}

	var lastUsed text

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT id, user_id, name, prefix, last_used_at, created_at
		   FROM api_tokens WHERE token_hash = ?`, hash,
	).Scan(&token.ID, &token.UserID, &token.Name, &token.Prefix, &lastUsed, &token.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}

	token.LastUsedAt = lastUsed.String()

	return token, nil
}

// TouchAPIToken records that a key was used, so a forgotten one is recognisable
// as forgotten before somebody deletes the wrong row.
func (s *Store) TouchAPIToken(id int64) {
	s.db.Exec(`UPDATE api_tokens SET last_used_at = datetime('now') WHERE id = ?`, id)
}

// DeleteAPIToken revokes a key. Scoped to the owner: an id is a small number,
// and nobody's key should be removable by guessing one.
func (s *Store) DeleteAPIToken(id, userID int64) {
	s.db.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
}

// APITokenCount is what the AI page shows before anything has been issued.
func (s *Store) APITokenCount(ctx context.Context, userID int64) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM api_tokens WHERE user_id = ?`, userID).Scan(&count)

	return count
}
