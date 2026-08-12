package databases

import (
	"fmt"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// mysqlRemoteSetting records that the server has been opened to the network,
// so the work is done once rather than on every database created afterwards.
const mysqlRemoteSetting = "mysql_listens_everywhere"

// mysqlRemoteConfig is the drop-in the panel writes.
//
// Its own file rather than an edit of 50-server.cnf: the packaged file belongs
// to apt, an upgrade replaces it, and a panel that had edited it would silently
// lose the setting. 99- so it is read after the packaged one and wins.
const mysqlRemoteConfig = `# Managed by Ubuntu Panel.
#
# MariaDB ships bound to 127.0.0.1, which is right for a server nothing
# connects to from outside. The panel hands out credentials for applications
# that do, so it listens on every interface — and every account it creates has
# a password, which is then the only thing in front of the data.
[mysqld]
bind-address = 0.0.0.0
`

// EnsureMySQLRemote makes MariaDB reachable from off the machine.
//
// Everything the panel hands out for MariaDB names the server's own address,
// so an application on another host can use it. That is only true if the
// server is listening there — otherwise the credentials are correct, the
// address is correct, and the connection is refused.
//
// Idempotent, and quiet when it has already been done: it restarts the
// database server, which is not a thing to do on every database created.
func EnsureMySQLRemote(st settingStore, runner *tasks.Runner) error {
	if st.SettingBool(mysqlRemoteSetting, false) {
		return nil
	}

	// Where the drop-in goes depends on which server is installed. MariaDB and
	// MySQL read their own directory last, so a file in the shared conf.d
	// would be overridden by the packaged 50-server.cnf it is meant to beat.
	directory := "/etc/mysql/mariadb.conf.d"

	switch {
	case proc.Ok("sudo test -d /etc/mysql/mariadb.conf.d"):
		directory = "/etc/mysql/mariadb.conf.d"
	case proc.Ok("sudo test -d /etc/mysql/mysql.conf.d"):
		directory = "/etc/mysql/mysql.conf.d"
	case proc.Ok("sudo test -d /etc/mysql/conf.d"):
		directory = "/etc/mysql/conf.d"
	default:
		return fmt.Errorf("no MariaDB configuration directory on this machine")
	}

	if err := proc.PutFile(directory+"/99-ubuntu-panel.cnf", mysqlRemoteConfig, true); err != nil {
		return err
	}

	// skip-networking would keep the port shut whatever bind-address says, and
	// a machine that was locked down by hand is exactly where this goes
	// unnoticed.
	proc.Ok("sudo sed -i 's/^[[:space:]]*skip-networking/#skip-networking/' " +
		directory + "/*.cnf 2>/dev/null")

	command := "sudo systemctl restart mariadb 2>/dev/null || sudo systemctl restart mysql"

	if runner != nil {
		if err := runner.Must(command, 3*time.Minute); err != nil {
			return err
		}
	} else if !proc.Ok(command) {
		return fmt.Errorf("MariaDB would not restart after being opened to the network")
	}

	// The port, if this machine has a firewall running. Optional: a server
	// without ufw is not a server that needs a rule.
	proc.Ok("if command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'; then " +
		"sudo ufw allow 3306/tcp; fi")

	st.SetSetting(mysqlRemoteSetting, "1")

	return nil
}

// settingStore is the little of the store this file needs, named here so the
// dependency is visible.
type settingStore interface {
	SettingBool(key string, fallback bool) bool
	SetSetting(key, value string) error
}

// mysqlHosts are the hosts every MariaDB account the panel creates exists for.
//
// MariaDB identifies an account by user *and* host, so 'app'@'localhost' and
// 'app'@'%' are two accounts with two passwords. The panel keeps one password
// and creates all three, because an application moved from this machine to
// another should not need a new account — and because a user who exists only
// for localhost fails from the address the panel prints, with an error that
// says the password is wrong.
//
// '%' does not cover localhost on MariaDB: a connection over the unix socket
// is matched against 'localhost' first, so the specific entries have to exist
// as well.
var mysqlHosts = []string{"localhost", "127.0.0.1", "%"}

// mysqlAccountScript creates or repairs the account for one database, at every
// host, with the password given — and grants it that database and no other.
//
// prefix is anything that has to run first, which is the CREATE DATABASE when
// this is a database being made rather than an account being repaired.
func mysqlAccountScript(name, username, password, prefix string) string {
	quoted := sqlQuote(password)
	script := prefix

	for _, host := range mysqlHosts {
		script += fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%[1]s'@'%[2]s' IDENTIFIED BY %[3]s;\n"+
				"ALTER USER '%[1]s'@'%[2]s' IDENTIFIED BY %[3]s;\n"+
				"GRANT ALL PRIVILEGES ON `%[4]s`.* TO '%[1]s'@'%[2]s';\n",
			username, host, quoted, name)
	}

	return script + "FLUSH PRIVILEGES;\n"
}
