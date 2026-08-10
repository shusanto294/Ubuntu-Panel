package store

import (
	"context"
)

// DNSAccount is a credential for one DNS provider. The token and secret are
// stored encrypted and are never written into a page payload.
type DNSAccount struct {
	ID         int64
	UserID     int64
	Provider   string
	Label      string
	APIToken   string // decrypted
	APISecret  string // decrypted
	Email      string
	AccountID  string
	VerifiedAt string
	SitesCount int
}

const dnsAccountColumns = `id, user_id, provider, label, api_token, api_secret,
	email, account_id, verified_at`

func (s *Store) scanDNSAccount(row siteScanner) (*DNSAccount, error) {
	account := &DNSAccount{}

	var userID nullInt
	var token, secret, email, accountID, verifiedAt text

	err := row.Scan(&account.ID, &userID, &account.Provider, &account.Label,
		&token, &secret, &email, &accountID, &verifiedAt)
	if err != nil {
		return nil, notFound(err)
	}

	account.UserID = userID.Int64
	account.APIToken = s.decrypt(token.String())
	account.APISecret = s.decrypt(secret.String())
	account.Email = email.String()
	account.AccountID = accountID.String()
	account.VerifiedAt = verifiedAt.String()

	return account, nil
}

// FindDNSAccount loads a credential. A user id of zero skips the ownership
// check, which is what the queue workers need.
func (s *Store) FindDNSAccount(ctx context.Context, id, userID int64) (*DNSAccount, error) {
	if userID > 0 {
		return s.scanDNSAccount(s.db.QueryRowContext(ctxOrBackground(ctx),
			`SELECT `+dnsAccountColumns+` FROM dns_accounts WHERE id = ? AND user_id = ?`,
			id, userID))
	}

	return s.scanDNSAccount(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+dnsAccountColumns+` FROM dns_accounts WHERE id = ?`, id))
}

// DNSAccounts lists a user's credentials, with how many sites each is holding
// records for.
func (s *Store) DNSAccounts(ctx context.Context, userID int64) ([]*DNSAccount, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+dnsAccountColumns+`,
		        (SELECT count(*) FROM sites s WHERE s.dns_account_id = dns_accounts.id
		                                        AND s.deleted_at IS NULL)
		   FROM dns_accounts WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*DNSAccount

	for rows.Next() {
		account := &DNSAccount{}

		var userIDValue nullInt
		var token, secret, email, accountID, verifiedAt text

		if err := rows.Scan(&account.ID, &userIDValue, &account.Provider, &account.Label,
			&token, &secret, &email, &accountID, &verifiedAt, &account.SitesCount); err != nil {
			return nil, err
		}

		account.UserID = userIDValue.Int64
		account.APIToken = s.decrypt(token.String())
		account.APISecret = s.decrypt(secret.String())
		account.Email = email.String()
		account.AccountID = accountID.String()
		account.VerifiedAt = verifiedAt.String()

		accounts = append(accounts, account)
	}

	return accounts, rows.Err()
}

// DNSAccountCount is what the site form checks before offering to manage DNS.
func (s *Store) DNSAccountCount(ctx context.Context, userID int64) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM dns_accounts WHERE user_id = ?`, userID).Scan(&count)

	return count
}

// CreateDNSAccount stores a credential, encrypted.
func (s *Store) CreateDNSAccount(ctx context.Context, account *DNSAccount) (int64, error) {
	token, err := s.encrypt(account.APIToken)
	if err != nil {
		return 0, err
	}

	secret, err := s.encrypt(account.APISecret)
	if err != nil {
		return 0, err
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO dns_accounts (user_id, provider, label, api_token, api_secret, email, account_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		account.UserID, account.Provider, account.Label, token, nullText(secret),
		nullText(account.Email), nullText(account.AccountID))
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// UpdateDNSAccount changes the label, and the credential when a new one was
// given. An empty token means "leave the stored one alone", which is what the
// edit form sends when the field was not touched.
func (s *Store) UpdateDNSAccount(ctx context.Context, account *DNSAccount) error {
	if account.APIToken == "" {
		_, err := s.db.ExecContext(ctxOrBackground(ctx),
			`UPDATE dns_accounts SET label = ?, email = ?, account_id = ?, updated_at = datetime('now')
			  WHERE id = ?`,
			account.Label, nullText(account.Email), nullText(account.AccountID), account.ID)

		return err
	}

	token, err := s.encrypt(account.APIToken)
	if err != nil {
		return err
	}

	secret, err := s.encrypt(account.APISecret)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctxOrBackground(ctx),
		`UPDATE dns_accounts SET label = ?, api_token = ?, api_secret = ?, email = ?,
		        account_id = ?, verified_at = NULL, updated_at = datetime('now')
		  WHERE id = ?`,
		account.Label, token, nullText(secret), nullText(account.Email),
		nullText(account.AccountID), account.ID)

	return err
}

// MarkDNSAccountVerified records that the credential answered.
func (s *Store) MarkDNSAccountVerified(id int64, verified bool) {
	if verified {
		s.db.Exec(`UPDATE dns_accounts SET verified_at = datetime('now'), updated_at = datetime('now')
		           WHERE id = ?`, id)
		return
	}

	s.db.Exec(`UPDATE dns_accounts SET verified_at = NULL, updated_at = datetime('now') WHERE id = ?`, id)
}

// DeleteDNSAccount removes a credential. The sites that used it keep their
// records; nothing on the provider is touched.
func (s *Store) DeleteDNSAccount(ctx context.Context, id, userID int64) error {
	_, err := s.db.ExecContext(ctxOrBackground(ctx),
		`DELETE FROM dns_accounts WHERE id = ? AND user_id = ?`, id, userID)

	return err
}

// ClearSiteDNSAccount unlinks a credential from the sites that named it, so a
// deleted credential does not leave rows pointing at nothing.
func (s *Store) ClearSiteDNSAccount(id int64) {
	s.db.Exec(`UPDATE sites SET dns_account_id = NULL, manage_dns = 0 WHERE dns_account_id = ?`, id)
	s.db.Exec(`UPDATE email_domains SET dns_account_id = NULL, manage_dns = 0 WHERE dns_account_id = ?`, id)
}
