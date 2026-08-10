package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EmailDomain is a domain this machine accepts and sends mail for.
type EmailDomain struct {
	ID            int64
	UserID        int64
	DNSAccountID  int64
	Domain        string
	Status        string
	DKIMSelector  string
	DKIMPublicKey string
	ManageDNS     bool
	DNSRecordIDs  string
	LastError     string
	CreatedAt     string
}

// EmailAccount is one mailbox.
type EmailAccount struct {
	ID            int64
	EmailDomainID int64
	UserID        int64
	LocalPart     string
	Password      string // decrypted
	QuotaMB       int
	Status        string
	LastError     string
	CreatedAt     string
}

const emailDomainColumns = `id, user_id, dns_account_id, domain, status, dkim_selector,
	dkim_public_key, manage_dns, dns_record_ids, last_error, created_at`

func (s *Store) scanEmailDomain(row siteScanner) (*EmailDomain, error) {
	domain := &EmailDomain{}

	var userID, dnsAccountID nullInt
	var publicKey, recordIDs, lastError text

	err := row.Scan(&domain.ID, &userID, &dnsAccountID, &domain.Domain, &domain.Status,
		&domain.DKIMSelector, &publicKey, &domain.ManageDNS, &recordIDs, &lastError,
		&domain.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}

	domain.UserID = userID.Int64
	domain.DNSAccountID = dnsAccountID.Int64
	domain.DKIMPublicKey = publicKey.String()
	domain.DNSRecordIDs = recordIDs.String()
	domain.LastError = lastError.String()

	return domain, nil
}

// FindEmailDomain loads a domain, deleted ones included — the worker taking
// one apart still needs its DKIM selector.
func (s *Store) FindEmailDomain(ctx context.Context, id int64) (*EmailDomain, error) {
	return s.scanEmailDomain(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+emailDomainColumns+` FROM email_domains WHERE id = ?`, id))
}

// FindEmailDomainByName is what stops the same domain being added twice.
func (s *Store) FindEmailDomainByName(ctx context.Context, name string) (*EmailDomain, error) {
	return s.scanEmailDomain(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+emailDomainColumns+` FROM email_domains WHERE domain = ? AND deleted_at IS NULL`,
		strings.ToLower(name)))
}

// EmailDomains is the list the Email page renders.
func (s *Store) EmailDomains(ctx context.Context) ([]*EmailDomain, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+emailDomainColumns+` FROM email_domains WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var domains []*EmailDomain

	for rows.Next() {
		domain, err := s.scanEmailDomain(rows)
		if err != nil {
			return nil, err
		}

		domains = append(domains, domain)
	}

	return domains, rows.Err()
}

// EmailDomainCount and MailboxCount are what block removing the mail server
// from under the mail it is holding.
func (s *Store) EmailDomainCount(ctx context.Context) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM email_domains WHERE deleted_at IS NULL`).Scan(&count)

	return count
}

func (s *Store) MailboxCount(ctx context.Context) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM email_accounts WHERE deleted_at IS NULL`).Scan(&count)

	return count
}

// CreateEmailDomain stores a domain for the worker to provision.
func (s *Store) CreateEmailDomain(ctx context.Context, domain *EmailDomain) (int64, error) {
	if domain.DKIMSelector == "" {
		domain.DKIMSelector = "mail"
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO email_domains (user_id, dns_account_id, domain, dkim_selector, manage_dns, status)
		 VALUES (?, ?, ?, ?, ?, 'pending')`,
		nullID(domain.UserID), nullID(domain.DNSAccountID),
		strings.ToLower(domain.Domain), domain.DKIMSelector, domain.ManageDNS)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// SetEmailDomainStatus records where provisioning got to.
func (s *Store) SetEmailDomainStatus(id int64, status, lastError string) {
	s.db.Exec(`UPDATE email_domains SET status = ?, last_error = ?, updated_at = datetime('now')
	           WHERE id = ?`, status, nullText(lastError), id)
}

// SetEmailDomainDKIM stores the public half of the key OpenDKIM generated. The
// private half never leaves /etc/opendkim.
func (s *Store) SetEmailDomainDKIM(id int64, publicKey string) {
	s.db.Exec(`UPDATE email_domains SET dkim_public_key = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(publicKey), id)
}

// SetEmailDomainDNS records what was published for the domain.
func (s *Store) SetEmailDomainDNS(id int64, recordIDs string) {
	s.db.Exec(`UPDATE email_domains SET dns_record_ids = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(recordIDs), id)
}

// MarkEmailDomainDeleted hides it while the worker takes it apart.
func (s *Store) MarkEmailDomainDeleted(id int64) {
	s.db.Exec(`UPDATE email_domains SET deleted_at = datetime('now'), status = 'deleting',
	           updated_at = datetime('now') WHERE id = ?`, id)
	s.db.Exec(`UPDATE email_accounts SET deleted_at = datetime('now'), status = 'deleting'
	           WHERE email_domain_id = ?`, id)
}

// DeleteEmailDomain removes the rows once the machine is clean.
func (s *Store) DeleteEmailDomain(id int64) {
	s.db.Exec(`DELETE FROM email_accounts WHERE email_domain_id = ?`, id)
	s.db.Exec(`DELETE FROM email_domains WHERE id = ?`, id)
}

// --------------------------------------------------------------- mailboxes

const emailAccountColumns = `id, email_domain_id, user_id, local_part, password,
	quota_mb, status, last_error, created_at`

func (s *Store) scanEmailAccount(row siteScanner) (*EmailAccount, error) {
	account := &EmailAccount{}

	var userID nullInt
	var password, lastError text

	err := row.Scan(&account.ID, &account.EmailDomainID, &userID, &account.LocalPart,
		&password, &account.QuotaMB, &account.Status, &lastError, &account.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}

	account.UserID = userID.Int64
	account.Password = s.decrypt(password.String())
	account.LastError = lastError.String()

	return account, nil
}

// FindEmailAccount loads a mailbox.
func (s *Store) FindEmailAccount(ctx context.Context, id int64) (*EmailAccount, error) {
	return s.scanEmailAccount(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+emailAccountColumns+` FROM email_accounts WHERE id = ?`, id))
}

// EmailAccounts lists a domain's mailboxes.
func (s *Store) EmailAccounts(ctx context.Context, domainID int64) ([]*EmailAccount, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+emailAccountColumns+` FROM email_accounts
		  WHERE email_domain_id = ? AND deleted_at IS NULL ORDER BY local_part`, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*EmailAccount

	for rows.Next() {
		account, err := s.scanEmailAccount(rows)
		if err != nil {
			return nil, err
		}

		accounts = append(accounts, account)
	}

	return accounts, rows.Err()
}

// CreateEmailAccount stores a mailbox with its password encrypted. The panel
// keeps the password because Dovecot needs a hash it cannot reverse and the
// operator needs something to type into a mail client.
func (s *Store) CreateEmailAccount(ctx context.Context, account *EmailAccount) (int64, error) {
	password, err := s.encrypt(account.Password)
	if err != nil {
		return 0, err
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO email_accounts (email_domain_id, user_id, local_part, password, quota_mb, status)
		 VALUES (?, ?, ?, ?, ?, 'pending')`,
		account.EmailDomainID, nullID(account.UserID), strings.ToLower(account.LocalPart),
		password, account.QuotaMB)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// SetEmailAccountStatus records where provisioning got to.
func (s *Store) SetEmailAccountStatus(id int64, status, lastError string) {
	s.db.Exec(`UPDATE email_accounts SET status = ?, last_error = ?, updated_at = datetime('now')
	           WHERE id = ?`, status, nullText(lastError), id)
}

// MarkEmailAccountDeleted hides a mailbox while its mail is removed.
func (s *Store) MarkEmailAccountDeleted(id int64) {
	s.db.Exec(`UPDATE email_accounts SET deleted_at = datetime('now'), status = 'deleting',
	           updated_at = datetime('now') WHERE id = ?`, id)
}

// DeleteEmailAccount removes the row.
func (s *Store) DeleteEmailAccount(id int64) {
	s.db.Exec(`DELETE FROM email_accounts WHERE id = ?`, id)
}

// ------------------------------------------------------------------- shapes

// Address is the mailbox's full address.
func (account *EmailAccount) Address(domain string) string {
	return account.LocalPart + "@" + domain
}

// RecordIDs is the map of published DNS records for the domain.
func (domain *EmailDomain) RecordIDs() map[string]string {
	ids := map[string]string{}

	if strings.TrimSpace(domain.DNSRecordIDs) != "" {
		json.Unmarshal([]byte(domain.DNSRecordIDs), &ids)
	}

	return ids
}

// WebmailHost is the hostname this domain's mail and webmail answer on.
//
// Every domain answers on its own `mail.<domain>`, which is what gets the A
// record, the certificate and the webmail vhost. The panel-wide mail_hostname
// is what the server calls itself in SMTP conversation — usually the same, and
// not required to be.
func (domain *EmailDomain) WebmailHost() string { return "mail." + domain.Domain }

// Summary is the shape the Email pages read.
func (s *Store) EmailDomainSummary(ctx context.Context, domain *EmailDomain, withAccounts bool) map[string]any {
	webmailHost := domain.WebmailHost()
	mailHostname := s.Setting("mail_hostname", webmailHost)

	accounts := []map[string]any{}

	if withAccounts {
		mailboxes, err := s.EmailAccounts(ctx, domain.ID)
		if err == nil {
			for _, account := range mailboxes {
				address := account.Address(domain.Domain)

				quotaLabel := "Unlimited"
				if account.QuotaMB > 0 {
					quotaLabel = fmt.Sprintf("%d MB", account.QuotaMB)
				}

				accounts = append(accounts, map[string]any{
					"id":          account.ID,
					"address":     address,
					"local_part":  account.LocalPart,
					"quota_mb":    account.QuotaMB,
					"quota_label": quotaLabel,
					"status":      account.Status,
					"last_error":  account.LastError,
					"webmail_url": "https://" + webmailHost + "/?_user=" + address,
				})
			}
		}
	}

	return map[string]any{
		"id":              domain.ID,
		"domain":          domain.Domain,
		"status":          domain.Status,
		"manage_dns":      domain.ManageDNS,
		"dkim_selector":   domain.DKIMSelector,
		"dkim_public_key": domain.DKIMPublicKey,
		"last_error":      domain.LastError,
		"mail_hostname":   mailHostname,
		"webmail_host":    webmailHost,
		"webmail_url":     "https://" + webmailHost + "/",
		"accounts":        accounts,

		// What to type into a mail client, or paste into an application's
		// mailer configuration — the same information asked for in a
		// different shape.
		"client_settings": map[string]any{
			"imap":     map[string]any{"host": webmailHost, "port": 993, "security": "SSL/TLS"},
			"smtp":     map[string]any{"host": webmailHost, "port": 587, "security": "STARTTLS"},
			"smtp_ssl": map[string]any{"host": webmailHost, "port": 465, "security": "SSL/TLS"},
			"username": "the full email address",
		},

		"created_at": domain.CreatedAt,
	}
}
