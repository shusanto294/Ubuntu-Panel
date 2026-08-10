package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// dovecotHash produces the SHA512-CRYPT hash Dovecot stores.
//
// `doveadm pw` reads the password from stdin rather than from argv, so it never
// appears in `ps`.
func dovecotHash(password string) (string, error) {
	staged, err := os.CreateTemp("", "ubuntu-panel-pw-*")
	if err != nil {
		return "", errors.New("could not stage the password")
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)

	if _, err := staged.WriteString(password); err != nil {
		staged.Close()
		return "", errors.New("could not stage the password")
	}

	staged.Close()

	result := proc.Run("sudo doveadm pw -s SHA512-CRYPT < "+proc.Quote(name), time.Minute)

	if result.OK() && result.Output != "" {
		return strings.TrimSpace(result.Output), nil
	}

	output := result.Output
	if output == "" {
		output = "no output"
	}

	return "", fmt.Errorf("doveadm could not hash the password: %s", output)
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
func authTest(runner *tasks.Runner, address, password string) string {
	staged, err := os.CreateTemp("", "ubuntu-panel-auth-*")
	if err != nil {
		return ""
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)
	staged.WriteString(password)
	staged.Close()

	code := runner.Exec(fmt.Sprintf("sudo doveadm auth test %s < %s 2>&1",
		proc.Quote(address), proc.Quote(name)), time.Minute)

	if code == 0 {
		return "Dovecot accepted " + address + "."
	}

	return "Dovecot refused " + address + " — its answer is above. A passdb that is not " +
		"consulted, a database it cannot read and a wrong password all end up here, and it " +
		"says which."
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

	script := fmt.Sprintf(
		"INSERT INTO %s.virtual_users (domain_id, email, password, quota_bytes) "+
			"SELECT id, %s, %s, %d FROM %s.virtual_domains WHERE name = %s "+
			"ON DUPLICATE KEY UPDATE password = VALUES(password), quota_bytes = VALUES(quota_bytes);\n",
		mailDB, sqlQuote(address), sqlQuote(hash),
		int64(account.QuotaMB)*1024*1024, mailDB, sqlQuote(domain.Domain))

	if err := runSQL(script); err != nil {
		return fail(err.Error())
	}

	list := &tasks.List{}

	list.Add("Check the mail store", mailStoreCommand)

	list.Add("Create the mail directory",
		fmt.Sprintf("sudo mkdir -p '%[1]s/%[2]s/%[3]s' && sudo chown -R vmail:vmail '%[1]s/%[2]s'",
			vmailRoot, domain.Domain, account.LocalPart))

	// Last, because it is the only step that reports on the finished article
	// rather than building part of it. Never fatal: the mailbox exists either
	// way, and a mail server that is not answering yet is a thing to fix rather
	// than a reason to unwind.
	list.AddFn("Check the mailbox can log in", func(runner *tasks.Runner) (string, error) {
		return authTest(runner, address, account.Password), nil
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
