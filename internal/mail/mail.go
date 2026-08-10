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

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
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
	dovecot := "# Managed by " + config.Name + " — included last, so these win.\n" +
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

	if err := proc.PutFile("/etc/dovecot/conf.d/99-ubuntu-panel.conf", dovecot, true); err != nil {
		return err
	}

	// Postfix main.cf additions, as a drop-in the panel owns entirely.
	hostname := s.Store.Setting("mail_hostname", system.Hostname())

	postfix := fmt.Sprintf(
		"# Managed by %s\n"+
			"myhostname = %s\n"+
			"virtual_mailbox_domains = proxy:mysql:/etc/postfix/sql/virtual-domains.cf\n"+
			"virtual_mailbox_maps = proxy:mysql:/etc/postfix/sql/virtual-mailboxes.cf\n"+
			"virtual_alias_maps = proxy:mysql:/etc/postfix/sql/virtual-aliases.cf\n"+
			"virtual_transport = lmtp:unix:private/dovecot-lmtp\n"+
			"smtpd_sasl_type = dovecot\n"+
			"smtpd_sasl_path = private/auth\n"+
			"smtpd_sasl_auth_enable = yes\n"+
			"smtpd_tls_security_level = may\n"+
			"smtp_tls_security_level = may\n"+
			"smtpd_milters = inet:127.0.0.1:8891\n"+
			"non_smtpd_milters = inet:127.0.0.1:8891\n"+
			"milter_default_action = accept\n",
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
		// Read the whole configuration back before restarting on it: Dovecot
		// refuses to start on a file it cannot parse, and a mail server that
		// will not come up is worse than one that is merely misconfigured.
		"sudo doveconf -n > /dev/null",
		"sudo systemctl restart dovecot",
		"sudo systemctl restart postfix")

	list.Add("Start OpenDKIM",
		"sudo mkdir -p /etc/opendkim/keys",
		"sudo chown -R opendkim:opendkim /etc/opendkim",
		"grep -q '^Socket' /etc/opendkim.conf || "+
			"printf 'Socket inet:8891@127.0.0.1\\nKeyTable /etc/opendkim/KeyTable\\n"+
			"SigningTable refile:/etc/opendkim/SigningTable\\nCanonicalization relaxed/simple\\n' "+
			"| sudo tee -a /etc/opendkim.conf > /dev/null",
		"sudo touch /etc/opendkim/KeyTable /etc/opendkim/SigningTable",
		"sudo systemctl restart opendkim || true")

	list.Add("Restart Postfix", "sudo systemctl restart postfix")

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the mail server was not configured")
	}

	s.Store.SetSetting("mail_configured", "1")

	return nil
}
