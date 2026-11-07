package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/migrate"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Moving a mail domain to another panel.
//
// What makes a mail domain is three things, and all three come across: its
// mailboxes with the same passwords, so every phone and mail client goes on
// collecting without being told anything; its DKIM key, so the signature DNS
// already publishes keeps verifying; and the stored mail, as the maildirs
// Dovecot keeps on disk. Mail that arrives at the old server while the DNS
// changes over is copied across afterwards (CatchUp).

// SnapshotMarker is the file a package's time is kept in: what the catch-up
// copies is everything newer than it.
const SnapshotMarker = ".migration-snapshot"

// validMailDomain is what is allowed into a path on a command line.
func validMailDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") {
		return false
	}

	for _, r := range domain {
		if !(r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z') {
			return false
		}
	}

	return true
}

// MigrationManifest is what the new server needs to know about a mail domain.
func (s *Service) MigrationManifest(ctx context.Context, domain *store.EmailDomain) (*migrate.Mail, error) {
	selector := domain.DKIMSelector
	if selector == "" {
		selector = "mail"
	}

	manifest := &migrate.Mail{
		Domain: domain.Domain, DKIMSelector: selector, DKIMPublic: domain.DKIMPublicKey,
		ManageDNS: domain.ManageDNS, Mailboxes: []migrate.Mailbox{},
	}

	if key, err := proc.ReadFile(fmt.Sprintf("/etc/opendkim/keys/%s/%s.private", domain.Domain, selector), true); err == nil {
		manifest.DKIMPrivate = key
	}

	accounts, err := s.Store.EmailAccounts(ctx, domain.ID)
	if err != nil {
		return nil, err
	}

	for _, account := range accounts {
		manifest.Mailboxes = append(manifest.Mailboxes, migrate.Mailbox{
			LocalPart: account.LocalPart, Name: account.Name, Password: account.Password, QuotaMB: account.QuotaMB,
		})
	}

	return manifest, nil
}

// PackageForMigration adds the steps that pack a domain's stored mail into
// directory/mail.tar.gz, marking the time so a catch-up can follow.
func (s *Service) PackageForMigration(list *tasks.List, domain *store.EmailDomain, directory string) {
	marker := proc.Quote(directory + "/" + SnapshotMarker)

	list.Add("Make room for the package",
		fmt.Sprintf("sudo mkdir -p %[1]s && sudo chmod 700 %[1]s && sudo touch %[2]s",
			proc.Quote(directory), marker))

	list.Add("Pack the stored mail",
		fmt.Sprintf("if sudo test -d %[1]s; then sudo tar -czpf %[2]s --numeric-owner --warning=no-file-changed "+
			"-C %[3]s %[4]s || [ $? = 1 ]; else sudo tar -czf %[2]s -T /dev/null; fi",
			proc.Quote(vmailRoot+"/"+domain.Domain), proc.Quote(directory+"/mail.tar.gz"),
			proc.Quote(vmailRoot), proc.Quote(domain.Domain)))

	list.Add("Hand the package to the panel",
		fmt.Sprintf("sudo chown -R \"$(id -un)\" %s", proc.Quote(directory)))
}

// PackageCatchUp packs the mail that arrived since the package in directory
// was made into directory/catch-up.tar.gz, and moves the mark on.
func (s *Service) PackageCatchUp(runner *tasks.Runner, domain string, directory string) (string, error) {
	if !validMailDomain(domain) {
		return "", errors.New("that is not a mail domain")
	}

	marker := directory + "/" + SnapshotMarker
	next := marker + ".next"
	archive := directory + "/catch-up.tar.gz"

	// The next mark is taken before the copy, so a message that arrives while
	// it runs is in this copy or the next one, never neither.
	inner := fmt.Sprintf("set -o pipefail; cd %[1]s && "+
		"if [ -d %[2]s ]; then find %[2]s -type f -newer %[3]s -print0; fi | "+
		"tar -czpf %[4]s --numeric-owner --null -T -",
		proc.Quote(vmailRoot), proc.Quote(domain), proc.Quote(marker), proc.Quote(archive))

	command := fmt.Sprintf("touch %[1]s && sudo bash -c %[2]s && sudo chown \"$(id -un)\" %[3]s && mv %[1]s %[4]s",
		proc.Quote(next), proc.Quote(inner), proc.Quote(archive), proc.Quote(marker))

	if err := runner.Must(command, 30*time.Minute); err != nil {
		return "", err
	}

	count := strings.TrimSpace(proc.Run("tar -tzf "+proc.Quote(archive)+" 2>/dev/null | wc -l", time.Minute).Output)

	return count + " message file(s) arrived here while the move was under way.", nil
}

// ImportMigrated sets a mail domain up from a package, as the task taskID:
// the domain, its DKIM key, its mailboxes and their mail. The webmail host
// and its certificate wait for the DNS (FinishImport).
func (s *Service) ImportMigrated(ctx context.Context, domainID, taskID int64, directory string, manifest *migrate.Mail) error {
	domain, err := s.Store.FindEmailDomain(ctx, domainID)
	if err != nil {
		return err
	}

	if !validMailDomain(domain.Domain) {
		return errors.New("that is not a mail domain")
	}

	list := &tasks.List{}

	list.AddFn("Check the mail tables", func(runner *tasks.Runner) (string, error) {
		if err := s.EnsureSchema(); err != nil {
			return "", err
		}
		return "Mail tables are in place.", nil
	})

	list.AddFn("Name this server after the domain", s.nameServerAfter(domain)).Optional()
	list.AddFn("Register the domain", s.registerDomain(ctx, domainID))

	if manifest.DKIMPrivate != "" {
		list.AddFn("Install the DKIM key", func(runner *tasks.Runner) (string, error) {
			return s.installDKIM(runner, domain, manifest)
		})
	} else {
		list.AddFn("Generate a DKIM key", s.generateDKIM(ctx, domainID))
	}

	list.Add("Check the mail store", mailStoreCommand)

	list.AddFn("Create the mailboxes", func(runner *tasks.Runner) (string, error) {
		accounts, err := s.Store.EmailAccounts(ctx, domainID)
		if err != nil {
			return "", err
		}

		for _, account := range accounts {
			if err := s.writeMailbox(account, domain); err != nil {
				s.Store.SetEmailAccountStatus(account.ID, "failed", err.Error())
				return "", fmt.Errorf("%s: %w", account.Address(domain.Domain), err)
			}
			s.Store.SetEmailAccountStatus(account.ID, "active", "")
		}

		return fmt.Sprintf("%d mailbox(es) created, each with the password it had.", len(accounts)), nil
	})

	list.Add("Unpack the stored mail",
		fmt.Sprintf("sudo tar -xzpf %[1]s --numeric-owner -C %[2]s && sudo mkdir -p %[3]s && "+
			"sudo chown -R vmail:vmail %[3]s && sudo find %[3]s -type f -perm /6111 -exec chmod 600 {} +",
			proc.Quote(directory+"/mail.tar.gz"), proc.Quote(vmailRoot), proc.Quote(vmailRoot+"/"+domain.Domain)))

	list.AddFn("Check the mailboxes can log in", func(runner *tasks.Runner) (string, error) {
		accounts, _ := s.Store.EmailAccounts(ctx, domainID)
		if len(accounts) == 0 {
			return "There are no mailboxes.", nil
		}
		answer, _ := authTest(runner, accounts[0].Address(domain.Domain), accounts[0].Password)
		return answer, nil
	}).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		message := "The mail domain could not be set up here."
		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}
		s.Store.SetEmailDomainStatus(domainID, "failed", message)

		return errors.New(message)
	}

	s.Store.SetEmailDomainStatus(domainID, "active", "")

	return nil
}

// installDKIM puts the key the domain signed with on the old server in place
// here, so the selector's record in DNS stays right.
func (s *Service) installDKIM(runner *tasks.Runner, domain *store.EmailDomain, manifest *migrate.Mail) (string, error) {
	selector := manifest.DKIMSelector
	if selector == "" || strings.ContainsAny(selector, "/ .'\"") {
		selector = "mail"
	}

	keyDir := "/etc/opendkim/keys/" + domain.Domain
	private := keyDir + "/" + selector + ".private"

	if err := runner.Must(fmt.Sprintf("sudo mkdir -p %s", proc.Quote(keyDir)), time.Minute); err != nil {
		return "", err
	}

	if err := proc.PutFile(private, manifest.DKIMPrivate, true); err != nil {
		return "", err
	}

	if err := runner.Must(fmt.Sprintf(
		"sudo chown -R opendkim:opendkim '%[1]s' && sudo chmod 600 '%[1]s/%[3]s.private' && "+
			"(grep -q '%[3]s._domainkey.%[2]s' /etc/opendkim/KeyTable || "+
			"echo '%[3]s._domainkey.%[2]s %[2]s:%[3]s:%[1]s/%[3]s.private' "+
			"| sudo tee -a /etc/opendkim/KeyTable > /dev/null) && "+
			"(grep -q '^\\*@%[2]s' /etc/opendkim/SigningTable || "+
			"echo '*@%[2]s %[3]s._domainkey.%[2]s' | sudo tee -a /etc/opendkim/SigningTable > /dev/null) && "+
			"sudo systemctl restart opendkim",
		keyDir, domain.Domain, selector), 3*time.Minute); err != nil {
		return "", err
	}

	if manifest.DKIMPublic != "" {
		s.Store.SetEmailDomainDKIM(domain.ID, manifest.DKIMPublic)
	}

	return "The DKIM key from the old server signs " + domain.Domain + "'s mail here, so its DNS record stays right.", nil
}

// FinishImport is what waits for the DNS: the webmail host, its certificate
// and the address mail clients configure themselves from.
func (s *Service) FinishImport(ctx context.Context, domainID, taskID int64) error {
	domain, err := s.Store.FindEmailDomain(ctx, domainID)
	if err != nil {
		return err
	}

	list := &tasks.List{}
	list.AddFn("Serve mail."+domain.Domain, s.serveMailHost(ctx, domainID)).Optional()
	list.AddFn("Get a certificate for mail."+domain.Domain, s.secureMailHost(ctx, domainID)).Optional()
	list.AddFn("Serve autoconfig."+domain.Domain, s.serveAutoconfig(ctx, domainID)).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the mail host could not be set up")
	}

	return nil
}

// ApplyCatchUp unpacks mail that arrived at the old server during the move.
// A message already here is left as it is: maildir file names are unique.
func (s *Service) ApplyCatchUp(runner *tasks.Runner, domain, archive string) (string, error) {
	if !validMailDomain(domain) {
		return "", errors.New("that is not a mail domain")
	}

	if err := runner.Must(fmt.Sprintf(
		"sudo tar -xzpf %[1]s --numeric-owner --skip-old-files -C %[2]s && sudo chown -R vmail:vmail %[3]s && "+
			"sudo find %[3]s -type f -perm /6111 -exec chmod 600 {} +",
		proc.Quote(archive), proc.Quote(vmailRoot), proc.Quote(vmailRoot+"/"+domain)), 30*time.Minute); err != nil {
		return "", err
	}

	return "The mail that reached the old server during the move is in its mailboxes here.", nil
}
