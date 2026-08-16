package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
)

// TicketTTL is how long a terminal ticket is worth anything. It is handed out
// by an authenticated request and redeemed seconds later by the websocket, so a
// minute is generous.
const TicketTTL = 60 * time.Second

// ErrTicket is any reason a ticket was not accepted. The websocket says the
// same thing whichever it was: a client that can tell "expired" from "already
// used" from "never existed" learns something about tickets it did not issue.
var ErrTicket = errors.New("that ticket is not valid")

// IssueTicket mints a one-shot token for a websocket to redeem.
//
// The socket cannot rely on the session cookie: a cross-scheme upgrade does not
// always carry it, and the panel is reached over both. The ticket grants
// nothing on its own — it is single use, it expires in a minute, and it names
// the account that asked for it.
func (s *Store) IssueTicket(ctx context.Context, userID int64, mode string) (string, error) {
	if mode != "shell" && mode != "stream" {
		mode = "shell"
	}

	token := crypto.Token(32)

	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO terminal_tickets (token, user_id, mode, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, mode, time.Now().Add(TicketTTL).Unix())
	if err != nil {
		return "", err
	}

	return token, nil
}

// RedeemTicket spends a ticket and reports whose it was.
//
// Single use by construction: the UPDATE only matches a row nobody has spent,
// so two sockets racing the same token cannot both win.
func (s *Store) RedeemTicket(ctx context.Context, token string) (int64, string, error) {
	if token == "" {
		return 0, "", ErrTicket
	}

	var userID int64
	var mode string

	err := s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT user_id, mode FROM terminal_tickets
		  WHERE token = ? AND used_at IS NULL AND expires_at >= ?`,
		token, time.Now().Unix()).Scan(&userID, &mode)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrTicket
	}

	if err != nil {
		return 0, "", err
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE terminal_tickets SET used_at = ? WHERE token = ? AND used_at IS NULL`,
		time.Now().Unix(), token)
	if err != nil {
		return 0, "", err
	}

	if spent, _ := result.RowsAffected(); spent == 0 {
		return 0, "", ErrTicket
	}

	return userID, mode, nil
}

// PruneTickets drops the ones nobody redeemed. Run with the session sweep.
func (s *Store) PruneTickets() {
	s.db.Exec(`DELETE FROM terminal_tickets WHERE expires_at < ?`,
		time.Now().Add(-time.Hour).Unix())
}
