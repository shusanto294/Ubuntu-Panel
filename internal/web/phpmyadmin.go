package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

const (
	// phpMyAdminAdmin is the MariaDB account the auto-login server signs in
	// as, and phpMyAdminSecret is where its password lives.
	//
	// Its own account rather than root: root on MariaDB authenticates through
	// the unix socket as the machine's administrator, which is what lets the
	// panel run `sudo mysql` without a password anywhere — and is exactly why
	// it cannot be handed to phpMyAdmin, which connects over TCP as www-data.
	phpMyAdminAdmin  = "panelpma"
	phpMyAdminSecret = "phpmyadmin_admin_password"

	// phpMyAdminBlowfish encrypts the cookie holding a manually-entered
	// password. Remembered rather than regenerated: a new one signs everybody
	// out, and phpMyAdmin refuses to run without it.
	phpMyAdminBlowfish = "phpmyadmin_blowfish"

	// phpMyAdminAutoLogin records whether the auto-login server was written.
	// It is only written when the panel's own session guards the path, so this
	// is also the answer to "is the button on the Databases page safe to
	// offer".
	phpMyAdminAutoLogin = "phpmyadmin_autologin"

	// The two server entries phpMyAdmin is configured with. One signs in by
	// itself as the administrator above; the other asks, so a database's own
	// credentials can be used to reach that database and nothing else.
	phpMyAdminServerAll    = 1
	phpMyAdminServerManual = 2
)

// phpMyAdminGatePath is the subrequest nginx makes before serving anything
// under /phpmyadmin. It answers 204 for a signed-in panel session and 401 for
// everything else.
const phpMyAdminGatePath = "/internal/phpmyadmin-gate"

// phpMyAdminGate is that endpoint.
//
// It exists because the auto-login server is a MariaDB administrator with no
// password prompt in front of it. Without something checking who is asking,
// publishing that would mean every database on the machine was one URL away
// for anybody who found it. nginx asks this first, and serves nothing until it
// answers 204.
//
// Deliberately not the usual redirect-to-login: nginx reads the status code,
// and a 302 to /login would be read as "allowed".
func (a *App) phpMyAdminGate(w http.ResponseWriter, r *http.Request) {
	if userFrom(r.Context()) == nil {
		w.WriteHeader(http.StatusUnauthorized)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// phpMyAdminURL is where a link on a page should point.
//
// The bare path when there is no auto-login to offer, so the operator gets
// phpMyAdmin's own login form rather than a server that is not configured.
func (a *App) phpMyAdminURL(server int) string {
	if !a.Store.SettingBool(phpMyAdminAutoLogin, false) {
		return sites.PhpMyAdminPath + "/"
	}

	return fmt.Sprintf("%s/?server=%d", sites.PhpMyAdminPath, server)
}

// EnsurePhpMyAdminReady puts phpMyAdmin in the state the Databases page
// promises: served, guarded, and configured with both ways in.
//
// Run at every start and again whenever phpMyAdmin is installed, because the
// three pieces have to agree and each of them can be missing on its own — the
// nginx location on a machine whose vhost predates it, the MariaDB account on
// one where phpMyAdmin was installed before the panel offered auto-login, and
// the configuration file whenever either of the other two changed.
//
// The order matters and is the whole of the safety here: the guard goes on
// before the account that needs no password is written into the configuration,
// and if the guard cannot be installed the auto-login server is left out
// entirely. phpMyAdmin then behaves as it always did — a login form — rather
// than as an open door.
func (a *App) EnsurePhpMyAdminReady(runner *tasks.Runner) error {
	if !a.Store.ServiceInstalled("phpmyadmin") {
		return nil
	}

	guarded := a.ensurePhpMyAdminServed(runner)

	// The account is only useful with the guard, and creating a MariaDB
	// superuser that nothing is going to use is not a thing to do quietly.
	autoLogin := false

	if guarded {
		if err := a.ensurePhpMyAdminAccount(); err != nil {
			a.Log.Warn("could not create phpMyAdmin's MariaDB account", "error", err)
		} else {
			autoLogin = true
		}
	}

	if err := a.writePhpMyAdminConfig(autoLogin); err != nil {
		return err
	}

	if autoLogin {
		a.Store.SetSetting(phpMyAdminAutoLogin, "1")
	} else {
		a.Store.ForgetSetting(phpMyAdminAutoLogin)
	}

	return nil
}

// ensurePhpMyAdminServed makes sure the panel's vhost has the location and the
// guard, and reports whether the guard is live on the file nginx is actually
// serving.
//
// Reported rather than assumed: auth_request is a module, and a machine whose
// nginx was built without it refuses the whole configuration — at which point
// the vhost is rolled back and phpMyAdmin is served by whatever was there
// before, which is not guarded at all.
func (a *App) ensurePhpMyAdminServed(runner *tasks.Runner) bool {
	live := func() string {
		return proc.Run("sudo cat /etc/nginx/sites-available/ubuntu-panel 2>/dev/null",
			time.Minute).Output
	}

	current := live()

	if !strings.Contains(current, sites.PhpMyAdminPath) ||
		!strings.Contains(current, phpMyAdminGatePath) {
		if err := a.refreshPanelVhost(runner); err != nil {
			a.Log.Warn("could not put phpMyAdmin into the panel's nginx vhost", "error", err)
		}

		current = live()
	}

	return strings.Contains(current, phpMyAdminGatePath)
}

// ensurePhpMyAdminAccount creates the MariaDB user the auto-login server signs
// in as, or resets its password to the one the panel has stored.
//
// Idempotent, because it runs on every start: a user that exists has its
// password set to what the configuration file is about to say, which is also
// the repair for an account somebody changed by hand.
func (a *App) ensurePhpMyAdminAccount() error {
	if !a.Store.ServiceInstalled("mysql") {
		return nil
	}

	password, err := a.Store.RememberSecret(phpMyAdminSecret, 28)
	if err != nil {
		return err
	}

	// Through a staged file rather than -e: the password would otherwise be in
	// the command line of a process anybody on the machine can list.
	//
	// GRANT OPTION as well, because phpMyAdmin's user management is half of
	// what an administrator opens it for, and a superuser that cannot grant is
	// a superuser that fails at it confusingly.
	script := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
			"ALTER USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
			"GRANT ALL PRIVILEGES ON *.* TO '%[1]s'@'localhost' WITH GRANT OPTION;\n"+
			"FLUSH PRIVILEGES;\n",
		phpMyAdminAdmin, sqlEscape(password))

	path, cleanup, err := stageScript(script)
	if err != nil {
		return err
	}

	defer cleanup()

	if result := proc.Run("sudo mysql < "+proc.Quote(path), 2*time.Minute); !result.OK() {
		return fmt.Errorf("MariaDB refused the phpMyAdmin account: %s", result.Output)
	}

	return nil
}

// writePhpMyAdminConfig writes config.inc.php.
//
// Two servers, and which of them exists is the whole of this feature. The
// first signs in by itself as the administrator, for the button on the
// Databases page that means "show me everything". The second asks for a
// username and password, for the button beside one database that means "let me
// in as this database's own user" — which is the same phpMyAdmin, reached with
// credentials that cannot see anything else.
//
// The second is the default, so a bare /phpmyadmin is a login form. Anybody
// who arrives at that URL without meaning to gets what they would have got
// before this existed.
func (a *App) writePhpMyAdminConfig(autoLogin bool) error {
	blowfish, err := a.Store.RememberSecret(phpMyAdminBlowfish, 32)
	if err != nil {
		return err
	}

	// phpMyAdmin checks the length of this string and warns when it is short.
	if len(blowfish) > 32 {
		blowfish = blowfish[:32]
	}

	var body strings.Builder

	fmt.Fprintf(&body, `<?php
/* Managed by Ubuntu Panel. Rewritten whenever the panel starts — edit the
   panel's settings rather than this file. */

declare(strict_types=1);

$cfg['blowfish_secret'] = '%s';
$cfg['TempDir'] = '/var/lib/ubuntu-panel/phpmyadmin-tmp';

/* Served from a path on the panel's own origin rather than a vhost of its
   own, so phpMyAdmin works the prefix out from the request. */
$cfg['PmaAbsoluteUri'] = '';

$i = 0;
`, blowfish)

	// Written first so it is server 1, whether or not it is there: the links
	// the panel builds name a number, and a numbering that moves depending on
	// configuration is a link that opens the wrong thing.
	if autoLogin {
		password, err := a.Store.RememberSecret(phpMyAdminSecret, 28)
		if err != nil {
			return err
		}

		fmt.Fprintf(&body, `
/* 1 — every database, signed in as the panel's own MariaDB administrator.
   There is no password prompt on this one, which is why nothing reaches it
   without a signed-in panel session: see phpMyAdminGate in the panel. */
$i++;
$cfg['Servers'][$i]['verbose'] = 'All databases';
$cfg['Servers'][$i]['host'] = '127.0.0.1';
$cfg['Servers'][$i]['auth_type'] = 'config';
$cfg['Servers'][$i]['user'] = '%s';
$cfg['Servers'][$i]['password'] = '%s';
$cfg['Servers'][$i]['AllowNoPassword'] = false;
`, phpMyAdminAdmin, phpEscape(password))
	} else {
		// A placeholder, so the manual server keeps its number on a machine
		// where auto-login could not be set up.
		body.WriteString("\n$i++; /* 1 — reserved for the panel's auto-login server. */\n")
	}

	fmt.Fprintf(&body, `
/* %d — sign in with a database's own username and password. */
$i++;
$cfg['Servers'][$i]['verbose'] = 'Sign in with database credentials';
$cfg['Servers'][$i]['host'] = '127.0.0.1';
$cfg['Servers'][$i]['auth_type'] = 'cookie';
$cfg['Servers'][$i]['AllowNoPassword'] = false;

/* The one that asks, so a bare /phpmyadmin is a login form. */
$cfg['ServerDefault'] = %d;
`, phpMyAdminServerManual, phpMyAdminServerManual)

	path := sites.PhpMyAdminParent + "/phpmyadmin/config.inc.php"

	if err := proc.PutFile(path, body.String(), true); err != nil {
		return err
	}

	// Readable by the PHP-FPM worker and by nobody else: on a machine with
	// auto-login it holds a MariaDB superuser's password.
	proc.Ok(fmt.Sprintf("sudo chown root:www-data %s && sudo chmod 640 %s",
		proc.Quote(path), proc.Quote(path)))

	return nil
}

// phpEscape quotes a value for a single-quoted PHP string.
func phpEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
}

// sqlEscape quotes a value for a single-quoted SQL string.
func sqlEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value)
}

// stageScript writes a script to a file only this user can read, for a command
// that would otherwise take it on the command line where `ps` can read it.
func stageScript(script string) (string, func(), error) {
	staged, err := os.CreateTemp("", "ubuntu-panel-pma-*")
	if err != nil {
		return "", func() {}, errors.New("could not stage the SQL statement")
	}

	name := staged.Name()
	cleanup := func() { os.Remove(name) }

	staged.Chmod(0o600)

	if _, err := staged.WriteString(script); err != nil {
		staged.Close()
		cleanup()

		return "", func() {}, errors.New("could not stage the SQL statement")
	}

	staged.Close()

	return name, cleanup, nil
}
