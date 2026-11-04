package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Who may connect to a MariaDB database.
//
// Every database is reachable from this server only, unless it is given
// addresses: specific IPs, networks or domains, or anywhere. The panel used to
// open every database to the whole internet — MariaDB on every interface,
// port 3306 open in the firewall, every account valid from any address — so a
// leaked wp-config.php was the database, from anywhere. Now three things follow
// what the databases allow, and only that:
//
//   - the accounts: 'user'@'localhost' and 'user'@'127.0.0.1' always, plus one
//     per allowed address ('%' for anywhere);
//   - the firewall: 3306 open to the allowed addresses only, closed when no
//     database allows any;
//   - MariaDB itself: on 127.0.0.1 unless some database is reachable from off
//     the machine.
//
// Sites the panel deploys connect to 127.0.0.1, so they work in every mode.

// The three modes.
const (
	AccessLocal    = "local"
	AccessList     = "list"
	AccessAnywhere = "anywhere"
)

// Access is one database's setting.
type Access struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow"`

	// Resolved is the addresses each domain in Allow last resolved to. Kept so
	// a DNS server that does not answer for a minute does not lock an
	// application out: the previous addresses stand until new ones arrive.
	Resolved map[string][]string `json:"resolved,omitempty"`
}

// maxAllow is how many entries one database takes.
const maxAllow = 50

// localHosts are the accounts every database user has: the unix socket, and
// TCP to loopback, which is what the sites use.
var localHosts = []string{"localhost", "127.0.0.1"}

const (
	accessSettingPrefix = "mysql_access_"
	firewallSetting     = "mysql_firewall_applied"
	firewallComment     = "Ubuntu Panel MariaDB"
)

func accessSetting(id int64) string { return fmt.Sprintf("%s%d", accessSettingPrefix, id) }

// Access is a database's setting; this server only when none was saved.
func (s *Service) Access(id int64) Access {
	var access Access

	stored := s.Store.Setting(accessSetting(id), "")
	if stored == "" || json.Unmarshal([]byte(stored), &access) != nil {
		access = Access{Mode: AccessLocal}
	}

	switch access.Mode {
	case AccessLocal, AccessList, AccessAnywhere:
	default:
		access.Mode = AccessLocal
	}

	if access.Allow == nil {
		access.Allow = []string{}
	}

	return access
}

func (s *Service) saveAccess(id int64, access Access) error {
	encoded, err := json.Marshal(access)
	if err != nil {
		return err
	}

	return s.Store.SetSetting(accessSetting(id), string(encoded))
}

// ParseAllow is the entries in what was typed: IP addresses, IPv4 networks and
// domains, separated by commas, spaces or new lines.
func ParseAllow(text string) ([]string, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})

	seen := map[string]bool{}
	entries := []string{}

	for _, field := range fields {
		entry := strings.ToLower(strings.TrimSpace(field))
		if entry == "" || seen[entry] {
			continue
		}

		if entryKind(entry) == "" {
			return nil, fmt.Errorf("%q is not an IP address, a network such as 203.0.113.0/24, or a domain name", field)
		}

		seen[entry] = true
		entries = append(entries, entry)
	}

	if len(entries) > maxAllow {
		return nil, fmt.Errorf("up to %d addresses can be allowed", maxAllow)
	}

	return entries, nil
}

// entryKind is "ip", "network" or "domain", or "" for none of them.
func entryKind(entry string) string {
	if ip := net.ParseIP(entry); ip != nil {
		if ip.IsUnspecified() {
			return ""
		}

		return "ip"
	}

	if ip, network, err := net.ParseCIDR(entry); err == nil {
		// MariaDB matches networks by an IPv4 netmask only, and /0 is anywhere,
		// which has its own switch.
		ones, _ := network.Mask.Size()
		if ip.To4() == nil || ones == 0 {
			return ""
		}

		return "network"
	}

	if validDomain(entry) {
		return "domain"
	}

	return ""
}

func validDomain(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}

	letters := false

	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z':
				letters = true
			case r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}

	// All digits would be a malformed IP address, not a name.
	return letters
}

// target is one allowed address in the two spellings it is needed in: as a
// MariaDB account host, and as a ufw source.
type target struct {
	host   string
	source string
}

// lookup resolves a domain to its addresses.
var lookup = func(name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return nil, err
	}

	ips := []string{}
	for _, address := range addresses {
		if !address.IP.IsUnspecified() && !address.IP.IsLoopback() {
			ips = append(ips, address.IP.String())
		}
	}

	sort.Strings(ips)

	if len(ips) == 0 {
		return nil, errors.New("no addresses")
	}

	return ips, nil
}

// resolve looks the domains up again. strict is for saving, where a domain
// that does not resolve is an error to show; otherwise its last addresses
// stand.
func (access *Access) resolve(strict bool) error {
	previous := access.Resolved
	access.Resolved = map[string][]string{}

	for _, entry := range access.Allow {
		if entryKind(entry) != "domain" {
			continue
		}

		ips, err := lookup(entry)
		if err != nil {
			if strict {
				return fmt.Errorf("%s does not resolve to an address", entry)
			}

			ips = previous[entry]
		}

		if len(ips) > 0 {
			access.Resolved[entry] = ips
		}
	}

	return nil
}

// targets are the addresses the setting allows beyond this server.
func (access Access) targets() []target {
	switch access.Mode {
	case AccessAnywhere:
		return []target{{host: "%", source: "any"}}
	case AccessList:
	default:
		return nil
	}

	targets := []target{}
	seen := map[string]bool{}

	add := func(t target) {
		if !seen[t.host] {
			seen[t.host] = true
			targets = append(targets, t)
		}
	}

	for _, entry := range access.Allow {
		switch entryKind(entry) {
		case "ip":
			ip := net.ParseIP(entry).String()
			add(target{host: ip, source: ip})
		case "network":
			_, network, _ := net.ParseCIDR(entry)
			add(target{host: network.IP.String() + "/" + net.IP(network.Mask).String(), source: network.String()})
		case "domain":
			for _, ip := range access.Resolved[entry] {
				if parsed := net.ParseIP(ip); parsed != nil {
					add(target{host: parsed.String(), source: parsed.String()})
				}
			}
		}
	}

	return targets
}

// Addresses are what the setting allows, as people read them: each IP and
// network, and each domain with the addresses it resolved to.
func (access Access) Addresses() []map[string]any {
	list := []map[string]any{}

	for _, entry := range access.Allow {
		item := map[string]any{"entry": entry, "kind": entryKind(entry)}
		if entryKind(entry) == "domain" {
			resolved := access.Resolved[entry]
			if resolved == nil {
				resolved = []string{}
			}
			item["resolved"] = resolved
		}
		list = append(list, item)
	}

	return list
}

// Hosts are the account hosts a database's users exist at.
func (s *Service) Hosts(id int64) []string {
	hosts := append([]string{}, localHosts...)

	for _, t := range s.Access(id).targets() {
		hosts = append(hosts, t.host)
	}

	return hosts
}

// SetAccess saves who may connect to a MariaDB database and makes the server
// match.
func (s *Service) SetAccess(ctx context.Context, id int64, mode, allow string) error {
	database, err := s.Store.FindDatabase(ctx, id)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if database.Engine != "mysql" {
		return errors.New("Only MariaDB databases have this setting.")
	}

	access := Access{Mode: mode, Allow: []string{}}

	switch mode {
	case AccessLocal, AccessAnywhere:
	case AccessList:
		entries, err := ParseAllow(allow)
		if err != nil {
			return err
		}

		if len(entries) == 0 {
			return errors.New("Enter at least one IP address, network or domain.")
		}

		access.Allow = entries

		if err := access.resolve(true); err != nil {
			return err
		}
	default:
		return errors.New("That is not one of the choices.")
	}

	previous := s.Store.Setting(accessSetting(id), "")

	if err := s.saveAccess(id, access); err != nil {
		return err
	}

	if err := s.SyncMySQLAccess(ctx, false); err != nil {
		// What the server has is what it had; the setting says so too.
		if previous == "" {
			s.Store.ForgetSetting(accessSetting(id))
		} else {
			s.Store.SetSetting(accessSetting(id), previous)
		}
		s.SyncMySQLAccess(ctx, false)

		return err
	}

	return nil
}

// ForgetAccess removes a dropped database's setting.
func (s *Service) ForgetAccess(id int64) { s.Store.ForgetSetting(accessSetting(id)) }

// HasDomains is whether any database allows a domain, whose addresses can
// change.
func (s *Service) HasDomains(ctx context.Context) bool {
	rows, err := s.Store.Databases(ctx)
	if err != nil {
		return false
	}

	for _, database := range rows {
		if database.Engine != "mysql" {
			continue
		}

		access := s.Access(database.ID)
		if access.Mode != AccessList {
			continue
		}

		for _, entry := range access.Allow {
			if entryKind(entry) == "domain" {
				return true
			}
		}
	}

	return false
}

// SyncMySQLAccess makes MariaDB, its accounts and the firewall match what
// every database allows. Idempotent: what already matches is left alone, and
// MariaDB is restarted only when it has to listen somewhere else. resolve looks
// the allowed domains up again.
func (s *Service) SyncMySQLAccess(ctx context.Context, resolve bool) error {
	// The start, the clock and a save can all arrive at once.
	syncing.Lock()
	defer syncing.Unlock()

	if !s.Store.ServiceInstalled("mysql") {
		return nil
	}

	rows, err := s.Store.Databases(ctx)
	if err != nil {
		return err
	}

	hosts := map[string]map[string]bool{} // user → the hosts it should exist at
	grants := map[string][]string{}       // user → the databases it is granted
	passwords := map[string]string{}      // user → the password the panel holds
	anywhere := false
	sources := map[string]bool{}

	for _, database := range rows {
		if database.Engine != "mysql" || !SafeIdentifier(database.Name) || database.Status == "deleting" || database.Status == "creating" {
			continue
		}

		access := s.Access(database.ID)

		if resolve && access.Mode == AccessList {
			before, _ := json.Marshal(access.Resolved)
			access.resolve(false)
			if after, _ := json.Marshal(access.Resolved); string(after) != string(before) {
				s.saveAccess(database.ID, access)
			}
		}

		targets := access.targets()
		for _, t := range targets {
			if t.source == "any" {
				anywhere = true
			}
			sources[t.source] = true
		}

		users := mysqlGrantees(database.Name)
		if SafeIdentifier(database.Username) {
			users = append(users, database.Username)
			passwords[database.Username] = database.Password
		}

		for _, user := range users {
			if hosts[user] == nil {
				hosts[user] = map[string]bool{}
			}
			for _, host := range localHosts {
				hosts[user][host] = true
			}
			for _, t := range targets {
				hosts[user][t.host] = true
			}

			if !contains(grants[user], database.Name) {
				grants[user] = append(grants[user], database.Name)
			}
		}
	}

	if err := setMySQLListen(len(sources) > 0); err != nil {
		return err
	}

	if err := s.setMySQLFirewall(anywhere, sources); err != nil {
		return err
	}

	return syncMySQLAccounts(hosts, grants, passwords)
}

var syncing sync.Mutex

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}

// mysqlQuery is one column of a query's rows.
func mysqlQuery(query string) ([]string, bool) {
	result := proc.Run("sudo mysql -N -B -e "+proc.Quote(query)+" 2>/dev/null", time.Minute)
	if !result.OK() {
		return nil, false
	}

	rows := []string{}
	for _, line := range strings.Split(result.Output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rows = append(rows, line)
		}
	}

	return rows, true
}

// mysqlGrantees are the accounts granted a database, the panel's or not.
func mysqlGrantees(database string) []string {
	rows, _ := mysqlQuery(fmt.Sprintf("SELECT DISTINCT User FROM mysql.db WHERE Db = '%s'", database))

	users := []string{}
	for _, user := range rows {
		if SafeIdentifier(user) {
			users = append(users, user)
		}
	}

	return users
}

// mysqlUserHosts are the hosts an account exists at.
func mysqlUserHosts(user string) []string {
	rows, _ := mysqlQuery(fmt.Sprintf("SELECT Host FROM mysql.user WHERE User = '%s'", user))

	return rows
}

// nativeHash is a mysql_native_password hash: '*' and forty hex digits.
func nativeHash(hash string) bool {
	if len(hash) != 41 || hash[0] != '*' {
		return false
	}

	for _, r := range hash[1:] {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'F') {
			return false
		}
	}

	return true
}

// syncMySQLAccounts creates each account at the hosts it should have and drops
// it from the ones it should not. An account whose password the panel does not
// hold is created with the password hash it already has.
func syncMySQLAccounts(hosts map[string]map[string]bool, grants map[string][]string, passwords map[string]string) error {
	users := make([]string, 0, len(hosts))
	for user := range hosts {
		users = append(users, user)
	}
	sort.Strings(users)

	script := ""

	for _, user := range users {
		existing := map[string]bool{}
		for _, host := range mysqlUserHosts(user) {
			existing[host] = true
		}

		identified := ""
		if password, ok := passwords[user]; ok && password != "" {
			identified = "IDENTIFIED BY " + sqlQuote(password)
		} else if rows, _ := mysqlQuery(fmt.Sprintf(
			"SELECT authentication_string FROM mysql.user WHERE User = '%s' AND authentication_string <> '' LIMIT 1", user)); len(rows) == 1 && nativeHash(rows[0]) {
			identified = "IDENTIFIED BY PASSWORD " + sqlQuote(rows[0])
		}

		wanted := make([]string, 0, len(hosts[user]))
		for host := range hosts[user] {
			wanted = append(wanted, host)
		}
		sort.Strings(wanted)

		for _, host := range wanted {
			if existing[host] || identified == "" {
				continue
			}

			script += fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%s' %s;\n", user, host, identified)
			for _, database := range grants[user] {
				script += fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s';\n", database, user, host)
			}
		}

		for host := range existing {
			if !hosts[user][host] && !strings.ContainsAny(host, "'\\") {
				script += fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s';\n", user, host)
			}
		}
	}

	if script == "" {
		return nil
	}

	return runSQL(nil, "mysql", script+"FLUSH PRIVILEGES;\n")
}

// dropMySQLUserScript drops an account at every host it exists at.
func dropMySQLUserScript(user string, extra []string) string {
	hosts := map[string]bool{"%": true}
	for _, host := range localHosts {
		hosts[host] = true
	}
	for _, host := range extra {
		hosts[host] = true
	}
	for _, host := range mysqlUserHosts(user) {
		hosts[host] = true
	}

	script := ""
	for host := range hosts {
		if !strings.ContainsAny(host, "'\\") {
			script += fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s';\n", user, host)
		}
	}

	return script
}

// ------------------------------------------------------------ the listener

// mysqlConfigName is the drop-in the panel writes, in whichever directory the
// installed server reads last. Its own file rather than an edit of
// 50-server.cnf, which belongs to apt.
const mysqlConfigName = "99-ubuntu-panel.cnf"

func mysqlConfig(remote bool) string {
	if remote {
		return `# Managed by Ubuntu Panel.
#
# A database on the Databases page allows connections from off this machine,
# so MariaDB listens on every interface. Its accounts exist only for the
# addresses that database allows, and the firewall opens 3306 to them only.
[mysqld]
bind-address = 0.0.0.0
`
	}

	return `# Managed by Ubuntu Panel.
#
# No database on the Databases page allows connections from off this machine,
# so MariaDB listens on loopback only.
[mysqld]
bind-address = 127.0.0.1
`
}

func mysqlConfigDirectory() (string, error) {
	for _, directory := range []string{"/etc/mysql/mariadb.conf.d", "/etc/mysql/mysql.conf.d", "/etc/mysql/conf.d"} {
		if proc.Ok("sudo test -d " + directory) {
			return directory, nil
		}
	}

	return "", errors.New("no MariaDB configuration directory on this machine")
}

// setMySQLListen puts MariaDB on every interface or on loopback, restarting
// it only when that changes.
func setMySQLListen(remote bool) error {
	directory, err := mysqlConfigDirectory()
	if err != nil {
		return err
	}

	path := directory + "/" + mysqlConfigName
	want := mysqlConfig(remote)

	current, readErr := proc.ReadFile(path, true)

	// Loopback is what MariaDB ships with; a machine that never had the file
	// is already there.
	if current == want || (readErr != nil && !remote) {
		return nil
	}

	if err := proc.PutFile(path, want, true); err != nil {
		return err
	}

	// skip-networking would keep the port shut whatever bind-address says.
	if remote {
		proc.Ok("sudo sed -i 's/^[[:space:]]*skip-networking/#skip-networking/' " + directory + "/*.cnf 2>/dev/null")
	}

	if !proc.Ok("sudo systemctl restart mariadb 2>/dev/null || sudo systemctl restart mysql") {
		return errors.New("MariaDB would not restart with the new address")
	}

	return nil
}

// ------------------------------------------------------------ the firewall

// FirewallActive is whether ufw is filtering, which decides whether the
// firewall is part of what keeps a database private.
func FirewallActive() bool { return ufwActive() }

func ufwActive() bool {
	return proc.Ok("command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'")
}

// setMySQLFirewall opens 3306 to the allowed sources and nothing else. The
// rules it added are remembered, so the ones no longer wanted can be removed;
// the plain "allow 3306/tcp" earlier panels added is removed whenever anywhere
// is not wanted.
func (s *Service) setMySQLFirewall(anywhere bool, sources map[string]bool) error {
	if !ufwActive() {
		return nil
	}

	want := []string{}
	if anywhere {
		want = []string{"any"}
	} else {
		for source := range sources {
			want = append(want, source)
		}
		sort.Strings(want)
	}

	var applied []string
	stored := s.Store.Setting(firewallSetting, "")
	json.Unmarshal([]byte(stored), &applied)

	if stored != "" && strings.Join(applied, ",") == strings.Join(want, ",") {
		return nil
	}

	wanted := map[string]bool{}
	for _, source := range want {
		wanted[source] = true
	}

	// --force: ufw asks before deleting, and nobody is there to answer.
	rule := func(verb, source string) string {
		if verb != "" {
			verb = "--force " + verb
		}
		if source == "any" {
			return "sudo ufw " + verb + "allow 3306/tcp"
		}

		return "sudo ufw " + verb + "allow from " + proc.Quote(source) + " to any port 3306 proto tcp"
	}

	if !anywhere {
		proc.Ok(rule("delete ", "any") + " >/dev/null 2>&1")
	}

	for _, source := range applied {
		if !wanted[source] {
			proc.Ok(rule("delete ", source) + " >/dev/null 2>&1")
		}
	}

	had := map[string]bool{}
	for _, source := range applied {
		had[source] = true
	}

	for _, source := range want {
		if had[source] {
			continue
		}

		if !proc.Ok(rule("", source) + " comment " + proc.Quote(firewallComment) + " >/dev/null 2>&1") {
			return fmt.Errorf("the firewall would not open 3306 to %s", source)
		}
	}

	encoded, _ := json.Marshal(want)

	return s.Store.SetSetting(firewallSetting, string(encoded))
}

// ------------------------------------------------------------ the accounts

// mysqlAccountScript creates or repairs an account for one database at every
// host given, with the password given — and grants it that database and no
// other.
//
// MariaDB identifies an account by user *and* host, and '%' does not cover a
// connection over the unix socket, so each host is an account of its own,
// all with the one password.
//
// prefix is anything that has to run first, which is the CREATE DATABASE when
// this is a database being made rather than an account being repaired.
func mysqlAccountScript(name, username, password, prefix string, hosts []string) string {
	quoted := sqlQuote(password)
	script := prefix

	for _, host := range hosts {
		script += fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%[1]s'@'%[2]s' IDENTIFIED BY %[3]s;\n"+
				"ALTER USER '%[1]s'@'%[2]s' IDENTIFIED BY %[3]s;\n"+
				"GRANT ALL PRIVILEGES ON `%[4]s`.* TO '%[1]s'@'%[2]s';\n",
			username, host, quoted, name)
	}

	return script + "FLUSH PRIVILEGES;\n"
}
