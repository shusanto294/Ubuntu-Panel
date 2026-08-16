package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// dovecotHash produces the SHA512-CRYPT hash Dovecot stores.
//
// `doveadm pw` reads the password from stdin rather than from argv, so it never
// appears in `ps` — but it asks for it *twice*, enter and retype. Given it once
// the second read hits end of file, and rather than failing it prints
//
//	Error: Password mismatch
//
// on standard output and exits zero. That sentence was written into the
// password column as though it were the hash, so every mailbox the panel
// created was refused at login with the right password and the table looked
// fine at a glance. It is fed twice now, and the answer is checked for the
// shape of a hash before anything stores it.
func dovecotHash(password string) (string, error) {
	staged, err := os.CreateTemp("", "ubuntu-panel-pw-*")
	if err != nil {
		return "", errors.New("could not stage the password")
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)

	if _, err := staged.WriteString(password + "\n" + password + "\n"); err != nil {
		staged.Close()
		return "", errors.New("could not stage the password")
	}

	staged.Close()

	result := proc.Run("sudo doveadm pw -s SHA512-CRYPT < "+proc.Quote(name), time.Minute)

	hash := strings.TrimSpace(result.Output)

	if !result.OK() || hash == "" {
		if hash == "" {
			hash = "no output"
		}

		return "", fmt.Errorf("doveadm could not hash the password: %s", hash)
	}

	if !isPasswordHash(hash) {
		// Whatever this is, it is not a hash, and storing it is how a mailbox
		// ends up with a password nothing can ever match.
		return "", fmt.Errorf("doveadm did not return a password hash, it returned %q", hash)
	}

	return hash, nil
}

// isPasswordHash reports whether doveadm handed back something Dovecot will
// treat as a hash: the scheme it was asked for, then crypt's own prefix.
func isPasswordHash(hash string) bool {
	if !strings.HasPrefix(hash, "{SHA512-CRYPT}") {
		return false
	}

	if strings.ContainsAny(hash, " \t") {
		return false
	}

	return strings.HasPrefix(strings.TrimPrefix(hash, "{SHA512-CRYPT}"), "$6$")
}

// authTest asks Dovecot whether this mailbox can log in, and puts its answer in
// the console.
//
// "Login failed" in a webmail is the least informative sentence in mail: it
// covers a wrong password, a passdb that is not consulted, a database the
// server cannot reach, and a user it does not believe exists. `doveadm auth
// test` says which — through the same passdb chain a real login uses — and the
// panel already has the password it just set.
//
// The password goes in through stdin, so it never appears in `ps`.
func authTest(runner *tasks.Runner, address, password string) (string, bool) {
	staged, err := os.CreateTemp("", "ubuntu-panel-auth-*")
	if err != nil {
		return "", true
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)
	staged.WriteString(password)
	staged.Close()

	code := runner.Exec(fmt.Sprintf("sudo doveadm auth test %s < %s 2>&1",
		proc.Quote(address), proc.Quote(name)), time.Minute)

	if code == 0 {
		return "Dovecot accepted " + address + ".", true
	}

	return "Dovecot refused this mailbox, so a mail client will say the password is wrong. " +
		"Repair the mail server from the Email page — that rewrites the Dovecot configuration " +
		"and re-checks it — then repair this mailbox.", false
}

// CreateAccount provisions one mailbox.
func (s *Service) CreateAccount(ctx context.Context, accountID int64) error {
	account, err := s.Store.FindEmailAccount(ctx, accountID)
	if err != nil {
		return err
	}

	domain, err := s.Store.FindEmailDomain(ctx, account.EmailDomainID)
	if err != nil {
		return err
	}

	address := account.Address(domain.Domain)

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.account.create", account.UserID, 0, address)
	if err != nil {
		return err
	}

	fail := func(message string) error {
		s.Store.FinishTask(taskID, "failed", message, 0)
		s.Store.SetEmailAccountStatus(accountID, "failed", message)

		return errors.New(message)
	}

	hash, err := dovecotHash(account.Password)
	if err != nil {
		return fail(err.Error())
	}

	// Same precondition as the domain task, and for the same reason: the tables
	// are checked where they are used, not remembered elsewhere.
	if err := s.EnsureSchema(); err != nil {
		return fail(err.Error())
	}

	// The domain row first. The mailbox insert selects the domain's id, so a
	// missing domain makes it a statement that inserts nothing and reports
	// success — a mailbox the panel calls active and Dovecot has never heard
	// of, which is a "Login failed" with a correct password behind it.
	script := fmt.Sprintf("INSERT IGNORE INTO %s.virtual_domains (name) VALUES (%s);\n",
		mailDB, sqlQuote(domain.Domain))

	script += fmt.Sprintf(
		"INSERT INTO %s.virtual_users (domain_id, email, password, quota_bytes) "+
			"SELECT id, %s, %s, %d FROM %s.virtual_domains WHERE name = %s "+
			"ON DUPLICATE KEY UPDATE password = VALUES(password), quota_bytes = VALUES(quota_bytes);\n",
		mailDB, sqlQuote(address), sqlQuote(hash),
		int64(account.QuotaMB)*1024*1024, mailDB, sqlQuote(domain.Domain))

	if err := runSQL(script); err != nil {
		return fail(err.Error())
	}

	// Asked of the table rather than assumed from an exit status: the insert
	// above is a SELECT with no rows on a machine whose mail schema was
	// reinstalled underneath, and MariaDB calls that success.
	if !proc.Ok(fmt.Sprintf("sudo mysql -N -B -e %s | grep -q '^1$'",
		proc.Quote(fmt.Sprintf("SELECT count(*) FROM %s.virtual_users WHERE email = %s;",
			mailDB, sqlQuote(address))))) {
		return fail("The mailbox was not written to the mail server's table, so nothing would " +
			"accept its password. Repair the mail server from the Email page and try again.")
	}

	list := &tasks.List{}

	list.Add("Check the mail store", mailStoreCommand)

	// Who the mailbox belongs to, as the webmail will show it. Optional in
	// every sense: the identity row only exists once somebody has signed in, a
	// machine with no Roundcube has no table at all, and neither is a reason to
	// fail a mailbox that receives mail perfectly well.
	if account.Name != "" && s.Store.ServiceInstalled("roundcube") {
		list.AddFn("Name the mailbox in webmail", func(runner *tasks.Runner) (string, error) {
			if err := s.nameWebmailIdentity(address, account.Name); err != nil {
				return "", err
			}

			return fmt.Sprintf("Mail from %s is signed %q where the webmail has an identity "+
				"for it; a desktop client carries its own.", address, account.Name), nil
		}).Optional()
	}

	list.Add("Create the mail directory",
		fmt.Sprintf("sudo mkdir -p '%[1]s/%[2]s/%[3]s' && sudo chown -R vmail:vmail '%[1]s/%[2]s'",
			vmailRoot, domain.Domain, account.LocalPart))

	// Last, because it is the only step that reports on the finished article
	// rather than building part of it. Never fatal: the mailbox exists either
	// way, and a mail server that is not answering yet is a thing to fix rather
	// than a reason to unwind — but what Dovecot said goes on the mailbox, so
	// the Email page carries the answer rather than the log being the only
	// place it was ever written.
	list.AddFn("Check the mailbox can log in", func(runner *tasks.Runner) (string, error) {
		answer, ok := authTest(runner, address, account.Password)

		if !ok {
			s.Store.SetEmailAccountStatus(accountID, "active", answer)
		}

		return answer, nil
	}).Optional()

	if tasks.New(s.Store, taskID).Run(list) {
		s.Store.SetEmailAccountStatus(accountID, "active", "")

		return nil
	}

	// The task's own message, which is the failing command and what it wrote,
	// rather than a sentence naming the step. "The mail directory could not be
	// created" was true and told nobody why.
	message := "The mailbox could not be created."

	if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
		message = task.Message
	}

	s.Store.SetEmailAccountStatus(accountID, "failed", message)

	return errors.New(message)
}

// DeleteAccount removes a mailbox, and its stored mail when asked.
func (s *Service) DeleteAccount(ctx context.Context, accountID int64, deleteMail bool) error {
	account, err := s.Store.FindEmailAccount(ctx, accountID)
	if err != nil {
		return err
	}

	domain, err := s.Store.FindEmailDomain(ctx, account.EmailDomainID)
	if err != nil {
		return err
	}

	address := account.Address(domain.Domain)

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.account.delete", account.UserID, 0, address)
	if err != nil {
		return err
	}

	runSQL(fmt.Sprintf("DELETE FROM %s.virtual_users WHERE email = %s;\n", mailDB, sqlQuote(address)))

	list := &tasks.List{}

	if deleteMail {
		list.Add("Delete the stored mail",
			fmt.Sprintf("sudo rm -rf '%s/%s/%s'", vmailRoot, domain.Domain, account.LocalPart)).Optional()
	} else {
		list.Add("Leave the stored mail in place", "true")
	}

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the mailbox was not fully removed")
	}

	s.Store.DeleteEmailAccount(accountID)

	return nil
}

// nameWebmailIdentity writes the display name into Roundcube's own identity for
// this address.
//
// An UPDATE and never an INSERT. Roundcube creates the user and its first
// identity when somebody signs in, and a row invented here — with a user_id
// nothing owns — is one Roundcube would either ignore or trip over. So a
// mailbox named before its first sign-in gets its name on the next
// provisioning run, which is what repairing it does.
//
// Wrapped in a check that the tables are there: a machine whose webmail was
// never installed has no such database, and a statement against it is an error
// rather than nothing happening.
func (s *Service) nameWebmailIdentity(address, name string) error {
	if !proc.Ok(fmt.Sprintf("sudo mysql -N -B -e %s | grep -q .",
		proc.Quote(fmt.Sprintf(
			"SELECT 1 FROM information_schema.tables WHERE table_schema = %s "+
				"AND table_name = 'identities';", sqlQuote(roundcubeDB))))) {
		return nil
	}

	return runSQL(fmt.Sprintf(
		"UPDATE %[1]s.identities i JOIN %[1]s.users u ON u.user_id = i.user_id "+
			"SET i.name = %[2]s WHERE u.username = %[3]s AND i.email = %[3]s;\n",
		roundcubeDB, sqlQuote(name), sqlQuote(address)))
}
