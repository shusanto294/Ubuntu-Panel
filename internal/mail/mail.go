// Package mail owns mail domains and mailboxes.
//
// Postfix and Dovecot read their virtual domain, mailbox and alias tables out
// of MariaDB, so adding a domain here is a row plus a DKIM key plus the DNS
// records that make other servers believe it. Nothing needs a configuration
// reload per mailbox — which is the whole reason the SQL backend is worth the
// setup.
package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

const (
	mailDB     = "ubuntu_panel_mail"
	mailDBUser = "panelmail"
	vmailRoot  = "/var/vmail"
)

// Service owns the mail lifecycle.
type Service struct {
	Store *store.Store
}

// New wires it up.
func New(st *store.Store) *Service { return &Service{Store: st} }

// ConfigRevision is what the files this package writes look like. It changes
// when they do, and never otherwise.
//
// A machine whose stored revision is older gets the mail server configured
// again on the next start; one that matches is left alone. It used to be the
// panel's own version that was compared, which meant every update — including
// the ones that did not touch a line of mail — rewrote the configuration and
// restarted Postfix, Dovecot and OpenDKIM. That is a mail outage of a second or
// two and a task in the log, both for nothing, on every release.
//
// **Bump this when you change what writeConfigs, writeDKIMConfig or
// EnsureSchema produce.** Nothing else needs to move with it: the panel's
// version is about releases, and this is about the contents of six files.
const ConfigRevision = "3"

// IsConfigured reports whether the mail server is installed and set up.
func (s *Service) IsConfigured() bool {
	return s.Store.ServiceInstalled("mail") && s.Store.SettingBool("mail_configured", false)
}

// dbPassword is what Postfix and Dovecot read the mail schema with. Generated
// once and kept: both configurations are written with it.
func (s *Service) dbPassword() string {
	password, err := s.Store.RememberSecret("mail_db_password", 28)
	if err != nil {
		return ""
	}

	return password
}

// sqlQuote is a single-quoted SQL literal; see the databases package for why
// this is the only escaping done by hand.
func sqlQuote(text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, "'", "''")

	return "'" + text + "'"
}

// runSQL feeds MariaDB a script from a staged file, so nothing lands in `ps`.
func runSQL(script string) error {
	staged, err := os.CreateTemp("", "ubuntu-panel-mail-*")
	if err != nil {
		return errors.New("could not stage the statement")
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)

	if _, err := staged.WriteString(script); err != nil {
		staged.Close()
		return errors.New("could not stage the statement")
	}

	staged.Close()

	result := proc.Run("sudo mysql < "+proc.Quote(name), 2*time.Minute)

	if result.OK() {
		return nil
	}

	output := result.Output
	if output == "" {
		output = "no output"
	}

	return fmt.Errorf("MariaDB rejected the statement: %s", output)
}

const mailSchema = "CREATE DATABASE IF NOT EXISTS " + mailDB + " CHARACTER SET utf8mb4;\n" +
	"CREATE TABLE IF NOT EXISTS " + mailDB + ".virtual_domains (" +
	"  id INT NOT NULL AUTO_INCREMENT," +
	"  name VARCHAR(190) NOT NULL," +
	"  PRIMARY KEY (id), UNIQUE KEY (name)" +
	") ENGINE=InnoDB;\n" +
	"CREATE TABLE IF NOT EXISTS " + mailDB + ".virtual_users (" +
	"  id INT NOT NULL AUTO_INCREMENT," +
	"  domain_id INT NOT NULL," +
	"  email VARCHAR(190) NOT NULL," +
	"  password VARCHAR(255) NOT NULL," +
	"  quota_bytes BIGINT NOT NULL DEFAULT 0," +
	"  PRIMARY KEY (id), UNIQUE KEY (email)," +
	"  FOREIGN KEY (domain_id) REFERENCES " + mailDB + ".virtual_domains(id) ON DELETE CASCADE" +
	") ENGINE=InnoDB;\n" +
	"CREATE TABLE IF NOT EXISTS " + mailDB + ".virtual_aliases (" +
	"  id INT NOT NULL AUTO_INCREMENT," +
	"  domain_id INT NOT NULL," +
	"  source VARCHAR(190) NOT NULL," +
	"  destination VARCHAR(190) NOT NULL," +
	"  PRIMARY KEY (id)," +
	"  FOREIGN KEY (domain_id) REFERENCES " + mailDB + ".virtual_domains(id) ON DELETE CASCADE" +
	") ENGINE=InnoDB;\n"

// EnsureSchema creates the schema, the account Postfix and Dovecot read it
// with, and its grants — every statement written so that running it twice is
// running it once.
//
// Called before every piece of work that touches these tables rather than only
// when the mail server is configured. The panel's own record of "configured"
// lives in its SQLite file and MariaDB's copy of the tables does not: reinstall
// MariaDB, or drop the database by hand, and the flag still says yes while
// virtual_domains is gone. What the operator then sees is a raw
// `ERROR 1146 ... Table 'ubuntu_panel_mail.virtual_domains' doesn't exist`
// against a mail server the panel insists is set up. This makes the tables a
// precondition that repairs itself instead of a fact remembered elsewhere.
func (s *Service) EnsureSchema() error {
	password := sqlQuote(s.dbPassword())

	script := mailSchema +
		fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%[1]s'@'127.0.0.1' IDENTIFIED BY %[2]s;\n"+
				"CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY %[2]s;\n"+
				"ALTER USER '%[1]s'@'127.0.0.1' IDENTIFIED BY %[2]s;\n"+
				"ALTER USER '%[1]s'@'localhost' IDENTIFIED BY %[2]s;\n"+
				"GRANT SELECT ON %[3]s.* TO '%[1]s'@'127.0.0.1';\n"+
				"GRANT SELECT ON %[3]s.* TO '%[1]s'@'localhost';\n"+
				"FLUSH PRIVILEGES;\n",
			mailDBUser, password, mailDB)

	return runSQL(script)
}

// writeDKIMConfig writes OpenDKIM's configuration outright.
//
// It used to be appended to, and only when the file had no `Socket` line at
// all — which Debian's does: a unix socket under /run/opendkim. Postfix is
// configured to talk to a milter on 127.0.0.1:8891, so it found nothing there,
// and `milter_default_action = accept` meant mail went out unsigned rather than
// not at all. Unsigned mail from a new server is mail in a spam folder, and
// nothing in the panel said a word about it.
//
// TrustedHosts is the other half. Without it OpenDKIM signs nothing sent from
// this machine, because it does not consider this machine one of its own.
func (s *Service) writeDKIMConfig() error {
	trusted := "# Managed by " + config.Name + " — hosts whose outgoing mail is signed.\n" +
		"127.0.0.1\n" +
		"::1\n" +
		"localhost\n"

	if err := proc.PutFile("/etc/opendkim/TrustedHosts", trusted, true); err != nil {
		return err
	}

	contents := "# Managed by " + config.Name + ". Changes here are overwritten.\n" +
		"Syslog                  yes\n" +
		"SyslogSuccess           yes\n" +
		"LogWhy                  no\n" +
		"\n" +
		"Canonicalization        relaxed/simple\n" +
		// Sign what leaves, verify what arrives.
		"Mode                    sv\n" +
		"SubDomains              no\n" +
		"OversignHeaders         From\n" +
		"\n" +
		"UserID                  opendkim\n" +
		"UMask                   007\n" +
		"\n" +
		// The address Postfix's smtpd_milters names. A unix socket would be
		// tidier and is not what the panel told Postfix to expect.
		"Socket                  inet:8891@127.0.0.1\n" +
		"PidFile                 /run/opendkim/opendkim.pid\n" +
		"\n" +
		"KeyTable                /etc/opendkim/KeyTable\n" +
		"SigningTable            refile:/etc/opendkim/SigningTable\n" +
		"ExternalIgnoreList      refile:/etc/opendkim/TrustedHosts\n" +
		"InternalHosts           refile:/etc/opendkim/TrustedHosts\n"

	return proc.PutFile("/etc/opendkim.conf", contents, true)
}

// resyncWebmail writes Roundcube's configuration again for every mail domain,
// so it always speaks to the server the way the server currently expects.
func (s *Service) resyncWebmail(runner *tasks.Runner) (string, error) {
	if !s.Store.ServiceInstalled("roundcube") {
		return "Roundcube is not installed, so there is no webmail configuration to write.", nil
	}

	domains, err := s.Store.EmailDomains(context.Background())
	if err != nil {
		return "", err
	}

	if len(domains) == 0 {
		return "No mail domains yet, so there is no webmail to configure.", nil
	}

	// One Roundcube serves every domain, so the last domain's name is only used
	// for the default it suggests on the login form.
	if err := s.webmailConfig(domains[len(domains)-1].Domain); err != nil {
		return "", err
	}

	// PHP-FPM holds the old file in its opcode cache, so a webmail that was
	// just reconfigured would go on using the configuration it started with.
	for _, version := range []string{"8.3", "8.2", "8.1"} {
		if proc.Ok("test -S /run/php/php" + version + "-fpm.sock") {
			runner.Exec("sudo systemctl reload php"+version+"-fpm || true", time.Minute)

			break
		}
	}

	return "Roundcube now points at submission on 587 over TLS, which is what the server " +
		"expects.", nil
}

// resyncMailboxes writes every mailbox the panel knows about into the mail
// server's tables again, hashed correctly.
func (s *Service) resyncMailboxes(runner *tasks.Runner) (string, error) {
	ctx := context.Background()

	domains, err := s.Store.EmailDomains(ctx)
	if err != nil {
		return "", err
	}

	written, failed := 0, 0

	for _, domain := range domains {
		script := fmt.Sprintf("INSERT IGNORE INTO %s.virtual_domains (name) VALUES (%s);\n",
			mailDB, sqlQuote(domain.Domain))

		accounts, err := s.Store.EmailAccounts(ctx, domain.ID)
		if err != nil {
			continue
		}

		for _, account := range accounts {
			address := account.Address(domain.Domain)

			if account.Password == "" {
				// Nothing stored to hash — the application key changed, or the
				// mailbox predates the panel keeping one.
				runner.Writef("%s: the panel has no copy of this password, so it cannot be "+
					"rewritten. Delete the mailbox and add it again.\n", address)

				failed++

				continue
			}

			hash, err := dovecotHash(account.Password)
			if err != nil {
				runner.Writef("%s: %v\n", address, err)

				failed++

				continue
			}

			script += fmt.Sprintf(
				"INSERT INTO %s.virtual_users (domain_id, email, password, quota_bytes) "+
					"SELECT id, %s, %s, %d FROM %s.virtual_domains WHERE name = %s "+
					"ON DUPLICATE KEY UPDATE password = VALUES(password), "+
					"quota_bytes = VALUES(quota_bytes);\n",
				mailDB, sqlQuote(address), sqlQuote(hash),
				int64(account.QuotaMB)*1024*1024, mailDB, sqlQuote(domain.Domain))

			written++
		}

		if err := runSQL(script); err != nil {
			return "", err
		}
	}

	if written == 0 && failed == 0 {
		return "There are no mailboxes to rewrite yet.", nil
	}

	if failed > 0 {
		return fmt.Sprintf("Rewrote %d mailbox(es); %d could not be rewritten, named above.",
			written, failed), nil
	}

	return fmt.Sprintf("Rewrote %d mailbox(es) with a freshly hashed password.", written), nil
}

// diagnose prints the state of everything a login goes through.
func (s *Service) diagnose(runner *tasks.Runner) (string, error) {
	// The password databases, in the order Dovecot tries them. A `driver = pam`
	// above `driver = sql` is the whole bug: PAM fails for an address that is
	// not a unix account, and Dovecot stops at the first failure.
	runner.Exec("echo '--- password databases, in order:'; sudo doveconf -n passdb userdb || true",
		30*time.Second)

	runner.Exec("echo '--- what includes what:'; "+
		"grep -nE '^[[:space:]]*!?#?!?include' /etc/dovecot/conf.d/10-auth.conf || true",
		30*time.Second)

	// Whether Dovecot's own account can read the table — with the credentials
	// from the file it actually reads, so a mismatch shows up here rather than
	// as a refused login.
	runner.Exec(fmt.Sprintf(
		"echo '--- can Dovecot read the mailbox table:'; "+
			"user=$(sudo sed -n 's/.*user=\\([^ ]*\\).*/\\1/p' /etc/dovecot/dovecot-sql.conf.ext); "+
			"pass=$(sudo sed -n 's/.*password=\\(.*\\)$/\\1/p' /etc/dovecot/dovecot-sql.conf.ext); "+
			"mysql -h 127.0.0.1 -u \"$user\" -p\"$pass\" -N -B "+
			"-e 'SELECT count(*) FROM %s.virtual_users;' 2>&1 "+
			"| sed 's/^/mailboxes visible to Dovecot: /'", mailDB), time.Minute)

	runner.Exec(fmt.Sprintf(
		"echo '--- mailboxes the panel wrote:'; "+
			"sudo mysql -N -B -e 'SELECT email, LEFT(password, 14), quota_bytes FROM %s.virtual_users;' "+
			"2>&1 | sed 's/^/  /'", mailDB), time.Minute)

	runner.Exec("echo '--- what is listening:'; "+
		"(sudo ss -lntp | grep -E ':(25|143|465|587|993|995)\\b' || echo 'nothing on any mail port')",
		30*time.Second)

	// Reaching a port is one claim and being answered by a mail server is
	// another. Bash speaks enough TCP to tell them apart without another
	// package on the machine.
	runner.Exec("echo '--- does each port answer:'; "+
		"for port in 25 587 465 143 993; do "+
		"  if exec 3<>/dev/tcp/127.0.0.1/$port 2>/dev/null; then "+
		"    read -t 3 -r line <&3 || line='(no banner — TLS from the first byte)'; "+
		"    echo \"  $port: $line\"; exec 3<&-; exec 3>&-; "+
		"  else echo \"  $port: nothing is listening\"; fi; "+
		"done", time.Minute)

	// Whether submission would take a login at all, which is what a webmail's
	// "Connection to server failed" hides.
	runner.Exec("echo '--- what submission offers:'; "+
		"(printf 'EHLO localhost\\r\\nQUIT\\r\\n' | "+
		"timeout 10 openssl s_client -quiet -starttls smtp -connect 127.0.0.1:587 2>/dev/null "+
		"| grep -iE 'AUTH|STARTTLS|250' | sed 's/^/  /') || "+
		"echo '  submission did not answer a STARTTLS conversation'", time.Minute)

	// Dovecot's own answer for a real mailbox, through the same passdb chain a
	// login uses. The password is the panel's stored copy, so nothing has to be
	// typed again.
	if account, address := s.firstMailbox(); address != "" {
		runner.Writef("--- asking Dovecot about %s:\n", address)
		authTest(runner, address, account)
	} else {
		runner.Write("--- no mailbox exists yet to test a login with.\n")
	}

	runner.Exec("echo '--- the last authentication lines in the mail log:'; "+
		"sudo grep -iE 'auth|login' /var/log/mail.log 2>/dev/null | tail -n 15 || "+
		"sudo journalctl -u dovecot --no-pager -n 15 || true", 30*time.Second)

	return "The login path is above: which password databases Dovecot consults, whether it can " +
		"read the mailbox table, what is in it, and what it says when asked directly.", nil
}

// firstMailbox is a real mailbox to test with, and its password.
func (s *Service) firstMailbox() (string, string) {
	domains, err := s.Store.EmailDomains(context.Background())
	if err != nil {
		return "", ""
	}

	for _, domain := range domains {
		accounts, err := s.Store.EmailAccounts(context.Background(), domain.ID)
		if err != nil {
			continue
		}

		for _, account := range accounts {
			if account.Password != "" {
				return account.Password, account.Address(domain.Domain)
			}
		}
	}

	return "", ""
}

// Hostname is the name this server gives when it greets another mail server.
//
// It matters more than it looks. Receivers check that the name in EHLO resolves
// to something, and a bare `vmi3499224` — which is what a VPS is called out of
// the box, and what Postfix uses by default — resolves to nothing at all. That
// is a spam score penalty on every message before the content is read, and it
// is also the name the TLS certificate has to match.
//
// So: whatever the operator set, or `mail.<domain>` for the first mail domain —
// which the panel has already published an A record for and issued a
// certificate for — and only then the machine's own name.
func (s *Service) Hostname() string {
	if name := s.Store.Setting("mail_hostname", ""); name != "" {
		return name
	}

	if domains, err := s.Store.EmailDomains(context.Background()); err == nil && len(domains) > 0 {
		return domains[len(domains)-1].WebmailHost()
	}

	return system.Hostname()
}

// mailStoreCommand creates the vmail account and the root of the mail store.
//
// Created when the mail server is configured, and needed by every mailbox
// afterwards — so, like the tables, it is checked where it is used. `mkdir -p`
// on a machine with no vmail user is what produced "The mail directory could
// not be created", which named the symptom and not one thing that would fix
// it. useradd and groupadd are allowed to fail: on the second run they should.
const mailStoreCommand = "sudo groupadd -g 5000 vmail 2>/dev/null || true; " +
	"sudo useradd -g vmail -u 5000 vmail -d " + vmailRoot + " -m 2>/dev/null || true; " +
	"sudo mkdir -p " + vmailRoot + " && sudo chown vmail:vmail " + vmailRoot

// writeConfigs writes the little SQL map files Postfix and Dovecot each need,
// plus the drop-ins that tell them to use them.
func (s *Service) writeConfigs() error {
	password := s.dbPassword()

	maps := []struct{ path, query string }{
		{"/etc/postfix/sql/virtual-domains.cf", "SELECT 1 FROM virtual_domains WHERE name='%s'"},
		{"/etc/postfix/sql/virtual-mailboxes.cf", "SELECT 1 FROM virtual_users WHERE email='%s'"},
		{"/etc/postfix/sql/virtual-aliases.cf", "SELECT destination FROM virtual_aliases WHERE source='%s'"},
	}

	for _, entry := range maps {
		contents := fmt.Sprintf(
			"user = %s\npassword = %s\nhosts = 127.0.0.1\ndbname = %s\nquery = %s\n",
			mailDBUser, password, mailDB, entry.query)

		if err := proc.PutFile(entry.path, contents, true); err != nil {
			return err
		}
	}

	// Dovecot's user and password lookups.
	contents := fmt.Sprintf(
		"driver = mysql\n"+
			"connect = host=127.0.0.1 dbname=%s user=%s password=%s\n"+
			"default_pass_scheme = SHA512-CRYPT\n"+
			"password_query = SELECT email as user, password FROM virtual_users WHERE email='%%u'\n"+
			"user_query = SELECT CONCAT('*:bytes=', quota_bytes) AS quota_rule "+
			"FROM virtual_users WHERE email='%%u'\n",
		mailDB, mailDBUser, password)

	if err := proc.PutFile("/etc/dovecot/dovecot-sql.conf.ext", contents, true); err != nil {
		return err
	}

	// Dovecot itself, as a drop-in that sorts last in conf.d and therefore
	// wins.
	//
	// Only the SQL credentials file was ever written by an earlier panel, which
	// tells Dovecot how to reach the mailbox table and nothing about using it.
	// Out of the box it authenticates against system users and stores mail in
	// mbox under /var/mail — so every mailbox the panel created was refused at
	// login, and the two unix sockets Postfix is configured to use for SASL and
	// LMTP did not exist, which means nothing could have been delivered either.
	//
	// Plaintext auth is allowed because the only clients are on this machine:
	// Roundcube over the loopback and Postfix over a unix socket. Anything
	// arriving from outside meets TLS first.
	if err := proc.PutFile("/etc/dovecot/conf.d/99-ubuntu-panel.conf", dovecotConfig(), true); err != nil {
		return err
	}

	// Postfix main.cf additions, as a drop-in the panel owns entirely.
	hostname := s.Hostname()

	postfix := fmt.Sprintf(
		"# Managed by %s\n"+
			"myhostname = %s\n"+
			// The name in EHLO when this server sends, which is what a receiver
			// checks against DNS.
			"smtp_helo_name = $myhostname\n"+
			"virtual_mailbox_domains = proxy:mysql:/etc/postfix/sql/virtual-domains.cf\n"+
			"virtual_mailbox_maps = proxy:mysql:/etc/postfix/sql/virtual-mailboxes.cf\n"+
			"virtual_alias_maps = proxy:mysql:/etc/postfix/sql/virtual-aliases.cf\n"+
			"virtual_transport = lmtp:unix:private/dovecot-lmtp\n"+
			"smtpd_sasl_type = dovecot\n"+
			"smtpd_sasl_path = private/auth\n"+
			// Off on port 25, which is the one the whole internet can reach and
			// the one that only ever carries mail *to* this server. Clients
			// authenticate on submission and smtps below, where TLS is
			// compulsory.
			"smtpd_sasl_auth_enable = no\n"+
			"smtpd_relay_restrictions = permit_mynetworks, permit_sasl_authenticated, "+
			"defer_unauth_destination\n"+
			"smtpd_tls_security_level = may\n"+
			"smtp_tls_security_level = may\n"+
			"smtpd_milters = inet:127.0.0.1:8891\n"+
			"non_smtpd_milters = inet:127.0.0.1:8891\n"+
			"milter_default_action = accept\n"+
			// Roundcube offers 75 MB and Postfix's default refuses anything
			// over 10, so the webmail invites an attachment the server then
			// bounces.
			"message_size_limit = 78643200\n"+
			"mailbox_size_limit = 0\n",
		config.Name, hostname)

	return proc.PutFile("/etc/postfix/main.cf.d/ubuntu-panel.cf", postfix, true)
}

// Configure provisions the mail server's own schema and the Postfix/Dovecot
// configuration that reads it. Idempotent; run after the mail packages land.
func (s *Service) Configure(ctx context.Context, userID int64) error {
	if !s.Store.ServiceInstalled("mail") {
		return errors.New("the mail server is not installed")
	}

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.configure", userID, 0,
		"Configuring the mail server")
	if err != nil {
		return err
	}

	// Staged rather than inlined into a step command: the grant carries the
	// mail database password.
	if err := s.EnsureSchema(); err != nil {
		s.Store.AppendTaskOutput(taskID, err.Error())
		s.Store.FinishTask(taskID, "failed", "The mail schema could not be created.", 0)

		return err
	}

	if err := s.writeDKIMConfig(); err != nil {
		s.Store.AppendTaskOutput(taskID, err.Error())
		s.Store.FinishTask(taskID, "failed", "The OpenDKIM configuration could not be written.", 0)

		return err
	}

	if err := s.writeConfigs(); err != nil {
		s.Store.AppendTaskOutput(taskID, err.Error())
		s.Store.FinishTask(taskID, "failed", "The mail configuration could not be written.", 0)

		return err
	}

	list := &tasks.List{}

	list.Add("Create the mail store", mailStoreCommand)

	list.Add("Point Postfix at the panel's configuration",
		"grep -q 'main.cf.d' /etc/postfix/main.cf || "+
			"echo '' | sudo tee -a /etc/postfix/main.cf > /dev/null",
		"sudo mkdir -p /etc/postfix/main.cf.d /etc/postfix/sql",
		"for f in /etc/postfix/main.cf.d/*.cf; do "+
			"  while IFS= read -r line; do case \"$line\" in \\#*|'') continue;; esac; "+
			"    sudo postconf -e \"$line\" >/dev/null 2>&1 || true; "+
			"  done < \"$f\"; done",
		"sudo chmod 640 /etc/postfix/sql/*.cf && sudo chown root:postfix /etc/postfix/sql/*.cf")

	list.Add("Configure Dovecot",
		"sudo chown root:dovecot /etc/dovecot/dovecot-sql.conf.ext",
		"sudo chmod 640 /etc/dovecot/dovecot-sql.conf.ext",

		// Stop asking PAM about mailboxes that are not unix accounts.
		//
		// Debian's 10-auth.conf includes auth-system.conf.ext, and it is
		// included before this panel's drop-in — so PAM is the first passdb in
		// the chain. Asked about apple@example.com it does not answer "no such
		// user" and move on; it fails, and Dovecot stops at the first passdb
		// that fails rather than trying the next one. The mailbox is in the SQL
		// table, the password is right, and the login is refused: exactly the
		// "Login failed" a webmail shows with nothing else to say.
		//
		// Commented out rather than deleted, and only when it is still active,
		// so running this again is running it once.
		"grep -qE '^[[:space:]]*!include auth-system.conf.ext' /etc/dovecot/conf.d/10-auth.conf && "+
			"sudo sed -i 's|^[[:space:]]*!include auth-system.conf.ext|"+
			"# Disabled by Ubuntu Panel: mailboxes are SQL rows, not unix accounts.\\n"+
			"#!include auth-system.conf.ext|' /etc/dovecot/conf.d/10-auth.conf || true",

		// Read the whole configuration back before restarting on it: Dovecot
		// refuses to start on a file it cannot parse, and a mail server that
		// will not come up is worse than one that is merely misconfigured.
		"sudo doveconf -n > /dev/null",

		// The passdb chain is what was just changed, so it is what gets
		// checked: one line naming the driver, and a failure here is a mail
		// server that will refuse every login.
		"sudo doveconf -n passdb | grep -q 'driver = sql' || "+
			"{ echo 'Dovecot is not configured to read mailboxes from SQL.'; exit 1; }",

		"sudo systemctl restart dovecot",
		"sudo systemctl restart postfix")

	// Submission and smtps, which is how anything sends mail.
	//
	// Debian ships both commented out in master.cf, so a stock Postfix listens
	// on 25 and nothing else — and 25 accepts mail *for* this server rather
	// than *from* its users. A webmail pointed at 587 gets "Connection to
	// server failed", which is true and says nothing about why.
	//
	// `postconf -M` and `-P` edit master.cf by name, so running this again
	// changes nothing rather than appending a second copy of the service.
	list.Add("Open the submission ports",
		"sudo postconf -M submission/inet='submission inet n - y - - smtpd'",
		"sudo postconf -P 'submission/inet/syslog_name=postfix/submission'",
		// Compulsory TLS: a password on port 587 is a password, and this is the
		// port a phone on a hotel network uses.
		"sudo postconf -P 'submission/inet/smtpd_tls_security_level=encrypt'",
		"sudo postconf -P 'submission/inet/smtpd_sasl_auth_enable=yes'",
		"sudo postconf -P 'submission/inet/smtpd_sasl_type=dovecot'",
		"sudo postconf -P 'submission/inet/smtpd_sasl_path=private/auth'",
		"sudo postconf -P 'submission/inet/smtpd_sasl_security_options=noanonymous'",
		"sudo postconf -P 'submission/inet/smtpd_client_restrictions=permit_sasl_authenticated,reject'",

		"sudo postconf -M smtps/inet='smtps inet n - y - - smtpd'",
		"sudo postconf -P 'smtps/inet/syslog_name=postfix/smtps'",
		"sudo postconf -P 'smtps/inet/smtpd_tls_wrappermode=yes'",
		"sudo postconf -P 'smtps/inet/smtpd_sasl_auth_enable=yes'",
		"sudo postconf -P 'smtps/inet/smtpd_sasl_type=dovecot'",
		"sudo postconf -P 'smtps/inet/smtpd_sasl_path=private/auth'",
		"sudo postconf -P 'smtps/inet/smtpd_sasl_security_options=noanonymous'",
		"sudo postconf -P 'smtps/inet/smtpd_client_restrictions=permit_sasl_authenticated,reject'")

	list.Add("Start OpenDKIM",
		"sudo mkdir -p /etc/opendkim/keys",
		"sudo touch /etc/opendkim/KeyTable /etc/opendkim/SigningTable",
		"sudo chown -R opendkim:opendkim /etc/opendkim",
		"sudo systemctl restart opendkim")

	list.Add("Restart Postfix", "sudo systemctl restart postfix")

	// The webmail's own configuration, for every domain that has one.
	//
	// Repairing the mail server used to mean Postfix and Dovecot and nothing
	// else, and the webmail's settings are written by the domain setup — so a
	// change to how the server expects to be spoken to left Roundcube speaking
	// the old way until somebody set a domain up again. Making submission
	// demand STARTTLS while Roundcube still had a plaintext `localhost:587` is
	// exactly that: the server was right, the test passed, and the webmail said
	// "Connection to server failed".
	list.AddFn("Rewrite the webmail configuration", s.resyncWebmail).Optional()

	// Every mailbox rewritten from the panel's own copy of its password.
	//
	// A mailbox whose stored hash is wrong is refused at login and looks
	// perfectly fine in the table, so repairing the mail server means repairing
	// them: the panel has the passwords, hashing is cheap, and writing a
	// correct hash over a correct hash costs nothing.
	list.AddFn("Rewrite the mailbox passwords", s.resyncMailboxes).Optional()

	// What the login path actually looks like on this machine, written into the
	// console where the operator already is.
	//
	// "Login failed" is the least informative sentence in mail, and every layer
	// under it is invisible from a browser: which password databases Dovecot
	// consults and in what order, whether it can reach the table at all,
	// whether the row is there, and what it says when asked directly. Guessing
	// between those from a screenshot is a round trip each time.
	list.AddFn("Check the login path", s.diagnose).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the mail server was not configured")
	}

	s.Store.SetSetting("mail_configured", "1")

	// What the configuration on disk was written from. This is what
	// NeedsConfiguring compares against, so a machine only has its mail server
	// rewritten when the panel actually changed how it writes one.
	s.Store.SetSetting("mail_configured_revision", ConfigRevision)

	return nil
}

// NeedsConfiguring reports whether the mail server has to be set up, or set up
// again.
//
// Nobody should have to press a repair button after installing a panel, and
// nobody should watch their mail server restart after an update that had
// nothing to do with mail. So: when the packages have landed and nothing has
// configured them, and when the panel has since changed what it writes —
// ConfigRevision, not the release number.
//
// A machine configured by a panel from before that distinction has no revision
// stored, so it is written once more and then left alone.
func (s *Service) NeedsConfiguring() bool {
	if !s.Store.ServiceInstalled("mail") {
		return false
	}

	if !s.Store.SettingBool("mail_configured", false) {
		return true
	}

	return s.Store.Setting("mail_configured_revision", "") != ConfigRevision
}

// dovecotConfig is the drop-in, built rather than written straight to disk so
// that what it promises a mailbox will have can be checked without a Dovecot
// running to be asked.
func dovecotConfig() string {
	return "# Managed by " + config.Name + " — included last, so these win.\n" +
		"protocols = imap pop3 lmtp\n" +
		"\n" +
		"mail_home = " + vmailRoot + "/%d/%n\n" +
		"mail_location = maildir:~/Maildir\n" +
		"mail_uid = vmail\n" +
		"mail_gid = vmail\n" +
		"first_valid_uid = 5000\n" +
		"last_valid_uid = 5000\n" +
		"\n" +
		"auth_mechanisms = plain login\n" +
		"disable_plaintext_auth = no\n" +
		"\n" +
		// The folders every mail client expects a mailbox to already have.
		//
		// A fresh Maildir has an inbox and nothing else, and Dovecot will not
		// invent the rest unless it is told to. Thunderbird then refuses to
		// send at all — "no Sent folder" — because it will not file a copy of
		// the message into a folder that is not there, and the person is left
		// with a mailbox that receives and cannot send. Roundcube papered over
		// it by creating the folders itself on first use, which is why sending
		// one webmail message appeared to repair the account.
		//
		// `auto = subscribe` is both halves: the folder is created the first
		// time the mailbox is opened, and it is added to the subscription list
		// so a client that only lists subscribed folders can see it. `create`
		// alone leaves Thunderbird looking at an empty list.
		//
		// `special_use` is what stops the guessing. It is the IMAP flag that
		// says "this is the Sent folder" independently of what it is called, so
		// a client in any language files sent mail, drafts and deletions in the
		// right place rather than matching on the English name.
		"namespace inbox {\n" +
		"  inbox = yes\n" +
		"\n" +
		"  mailbox Drafts {\n" +
		"    special_use = \\Drafts\n" +
		"    auto = subscribe\n" +
		"  }\n" +
		"\n" +
		"  mailbox Sent {\n" +
		"    special_use = \\Sent\n" +
		"    auto = subscribe\n" +
		"  }\n" +
		"\n" +
		"  mailbox Junk {\n" +
		"    special_use = \\Junk\n" +
		"    auto = subscribe\n" +
		"  }\n" +
		"\n" +
		"  mailbox Trash {\n" +
		"    special_use = \\Trash\n" +
		"    auto = subscribe\n" +
		"  }\n" +
		"\n" +
		"  mailbox Archive {\n" +
		"    special_use = \\Archive\n" +
		"    auto = subscribe\n" +
		"  }\n" +
		"}\n" +
		"\n" +
		"passdb {\n" +
		"  driver = sql\n" +
		"  args = /etc/dovecot/dovecot-sql.conf.ext\n" +
		"}\n" +
		"\n" +
		"userdb {\n" +
		"  driver = static\n" +
		"  args = uid=vmail gid=vmail home=" + vmailRoot + "/%d/%n\n" +
		"}\n" +
		"\n" +
		"service auth {\n" +
		"  unix_listener /var/spool/postfix/private/auth {\n" +
		"    mode = 0660\n" +
		"    user = postfix\n" +
		"    group = postfix\n" +
		"  }\n" +
		"\n" +
		"  unix_listener auth-userdb {\n" +
		"    mode = 0600\n" +
		"    user = vmail\n" +
		"  }\n" +
		"}\n" +
		"\n" +
		"service lmtp {\n" +
		"  unix_listener /var/spool/postfix/private/dovecot-lmtp {\n" +
		"    mode = 0600\n" +
		"    user = postfix\n" +
		"    group = postfix\n" +
		"  }\n" +
		"}\n"
}
