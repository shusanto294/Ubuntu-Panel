package security

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Protection is the optional protections on the Security page's Protection
// tab, each switched on and off on its own. Every one of them is off until the
// operator turns it on: each changes what visitors, mail clients or the kernel
// are allowed to do, and that is the owner's call, not the installer's.
//
// They are made of the programs the machine already runs, like the rest of
// the package:
//
//   - fail2ban jails reading nginx's access logs (website login brute force,
//     vulnerability scanners), Postfix and Dovecot's log (mail passwords), and
//     fail2ban's own log (repeat offenders);
//   - an nginx file in conf.d with rate limits and, behind Cloudflare, the
//     directives that put the visitor's address back into $remote_addr;
//   - a sysctl file with the network settings a public server should have.
type Protection struct {
	WebLogin JailRule `json:"web_login"`
	WebScan  JailRule `json:"web_scan"`
	Mail     JailRule `json:"mail"`
	Recidive JailRule `json:"recidive"`

	RateLogin Rate `json:"rate_login"`
	RateFlood Rate `json:"rate_flood"`

	RealIP bool `json:"real_ip"`
	Kernel bool `json:"kernel"`
	Scan   bool `json:"scan"`
}

// JailRule is one optional fail2ban jail: so many matches within FindTime seconds
// and the address is banned for BanTime seconds (Permanent for ever).
type JailRule struct {
	Enabled  bool `json:"enabled"`
	MaxRetry int  `json:"max_retry"`
	FindTime int  `json:"find_time"`
	BanTime  int  `json:"ban_time"`
}

// Rate is one nginx rate limit: Rate requests a minute (logins) or a second
// (everything else) per address, with Burst more allowed at once.
type Rate struct {
	Enabled bool `json:"enabled"`
	Rate    int  `json:"rate"`
	Burst   int  `json:"burst"`
}

// ProtectionDefaults are the values a switch turns on with: five within ten
// minutes, banned for good. Strict, and safe to be strict about because
// nothing the panel does is ever counted — its own addresses, its private
// networks and everyone who signed in to it are on every jail's ignore list,
// and the website jails read the sites' logs only, never the one the panel's
// own pages are logged to.
var ProtectionDefaults = Protection{
	WebLogin:  JailRule{MaxRetry: 5, FindTime: 600, BanTime: Permanent},
	WebScan:   JailRule{MaxRetry: 5, FindTime: 600, BanTime: Permanent},
	Mail:      JailRule{MaxRetry: 5, FindTime: 600, BanTime: Permanent},
	Recidive:  JailRule{MaxRetry: 3, FindTime: 86400, BanTime: Permanent},
	RateLogin: Rate{Rate: 10, Burst: 10},
	RateFlood: Rate{Rate: 20, Burst: 100},
}

// Validate is what a save is checked against.
func (j JailRule) Validate() map[string]string {
	problems := map[string]string{}

	if j.MaxRetry < 1 || j.MaxRetry > 1000 {
		problems["max_retry"] = "Allow between 1 and 1000 matches."
	}
	if j.FindTime < 60 || j.FindTime > 7*86400 {
		problems["find_time"] = "Count over a window between a minute and a week."
	}
	if j.BanTime != Permanent && (j.BanTime < 60 || j.BanTime > 365*86400) {
		problems["ban_time"] = "Ban for between a minute and a year, or permanently."
	}

	return problems
}

func (r Rate) Validate(perSecond bool) map[string]string {
	problems := map[string]string{}

	limit := 600
	if perSecond {
		limit = 1000
	}

	if r.Rate < 1 || r.Rate > limit {
		problems["rate"] = fmt.Sprintf("Allow between 1 and %d requests.", limit)
	}
	if r.Burst < 0 || r.Burst > 10000 {
		problems["burst"] = "A burst is between 0 and 10,000 requests."
	}

	return problems
}

// AnyJail is whether any of the optional jails is on.
func (p Protection) AnyJail() bool {
	return p.WebLogin.Enabled || p.WebScan.Enabled || p.Mail.Enabled || p.Recidive.Enabled
}

// CloudflareRanges are the addresses Cloudflare's proxies connect from,
// as published at cloudflare.com/ips. A site whose DNS is proxied through
// Cloudflare is reached from these, never from the visitor, so they are never
// banned or rate limited — doing so would refuse every visitor at once — and
// they are the only addresses trusted to say who the visitor really is.
var CloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// ------------------------------------------------------------------ jails

// The optional jails, as fail2ban knows them. recidive keeps fail2ban's own
// name: its packaged filter ignores the lines its own bans write by looking
// for a jail of that name.
const (
	JailWebLogin = "panel-web-login"
	JailWebScan  = "panel-web-scan"
	JailMail     = "panel-mail-smtp"
	JailMailIMAP = "panel-mail-imap"
	JailRecidive = "recidive"

	protectJailPath   = "/etc/fail2ban/jail.d/ubuntu-panel-protect.local"
	webLoginFilter    = "/etc/fail2ban/filter.d/ubuntu-panel-web-login.conf"
	webScanFilterPath = "/etc/fail2ban/filter.d/ubuntu-panel-web-scan.conf"
)

// ProtectJails are the jails a protection setting runs, for status and unban.
func (p Protection) ProtectJails() []string {
	jails := []string{}

	if p.WebLogin.Enabled {
		jails = append(jails, JailWebLogin)
	}
	if p.WebScan.Enabled {
		jails = append(jails, JailWebScan)
	}
	if p.Mail.Enabled {
		jails = append(jails, JailMail, JailMailIMAP)
	}
	if p.Recidive.Enabled {
		jails = append(jails, JailRecidive)
	}

	return jails
}

// The access log line in nginx's combined format, as fail2ban sees it: the
// date is cut out of the line before the expression is tried, so the brackets
// may be empty — or not, on an older fail2ban — and both are matched.
const accessPrefix = `^<HOST> \S+ \S+ \[[^\]]*\] "`

// webLoginFilterFile counts login form posts. WordPress answers a wrong
// password with 200 and a right one with a redirect, so only the 200s count
// there; other applications redirect both ways, and every post counts.
// xmlrpc.php is counted whatever the answer: the panel denies it on WordPress
// sites, and anything still posting to it is a script.
var webLoginFilterFile = "# Managed by Ubuntu Panel: website login brute force.\n" +
	"[Definition]\n" +
	"failregex = " + accessPrefix + `POST /wp-login\.php(?:\?\S*)? HTTP/[^"]*" 200 ` + "\n" +
	"            " + accessPrefix + `POST /xmlrpc\.php(?:\?\S*)? HTTP/[^"]*" \d{3} ` + "\n" +
	"            " + accessPrefix + `POST \S*/(?:login|signin|sign-in|user/login|admin/login|auth/login|administrator/index\.php)/?(?:\?\S*)? HTTP/[^"]*" \d{3} ` + "\n" +
	"ignoreregex =\n"

// scanTargets are files nobody but a vulnerability scanner asks for.
var scanTargets = []string{
	`\.env`, `\.git/`, `\.aws/`, `\.ssh/`, `\.svn/`, `\.hg/`, `\.htpasswd`, `\.DS_Store`,
	`wp-config\.php\.\w+`, `wp-config\.(?:bak|old|txt|save|orig)`,
	`vendor/phpunit`, `eval-stdin\.php`, `cgi-bin/`, `boaform`, `HNAP1`,
	`actuator/`, `solr/`, `manager/html`, `telescope/`, `_ignition/`,
	`(?:backup|dump|database|db)\.sql`, `(?:shell|wso|c99|r57|alfa|xleet)\.php`,
	`\.\./`,
	// fail2ban reads its files with Python's ConfigParser, where % starts an
	// interpolation; a literal one is written twice.
	`%%2e%%2e`,
}

// webScanFilterFile counts requests for those files that were refused or not
// there — a 200 is a file the site does have, and is not this jail's business.
var webScanFilterFile = "# Managed by Ubuntu Panel: vulnerability scanners.\n" +
	"[Definition]\n" +
	"failregex = " + accessPrefix + `(?:GET|POST|HEAD|PUT|OPTIONS) /\S*(?:` +
	strings.Join(scanTargets, "|") + `)\S* HTTP/[^"]*" [45]\d\d ` + "\n" +
	"ignoreregex =\n"

// JailEnv is what the jail file needs to know about the machine.
type JailEnv struct {
	IgnoreIPs []string
	MailLog   bool // /var/log/mail.log is written; otherwise the journal
	Fail2ban  bool // /var/log/fail2ban.log is written
}

// ProtectJailFile renders every optional jail that is on, or "" when none is.
func ProtectJailFile(p Protection, env JailEnv) string {
	if !p.AnyJail() {
		return ""
	}

	ignore := append([]string{"127.0.0.1/8", "::1"}, env.IgnoreIPs...)
	ignore = append(ignore, CloudflareRanges...)

	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — the Protection tab on the Security page.\n")
	file.WriteString("# Edits made here are overwritten the next time it saves.\n")

	section := func(name string, jail JailRule, lines ...string) {
		fmt.Fprintf(&file, "\n[%s]\n", name)
		file.WriteString("enabled  = true\n")
		for _, line := range lines {
			file.WriteString(line + "\n")
		}
		fmt.Fprintf(&file, "maxretry = %d\n", jail.MaxRetry)
		fmt.Fprintf(&file, "findtime = %d\n", jail.FindTime)
		fmt.Fprintf(&file, "bantime  = %d\n", jail.BanTime)
		fmt.Fprintf(&file, "ignoreip = %s\n", strings.Join(ignore, " "))
	}

	// Every site's access log — and only those. nginx's own access.log is
	// where the panel's pages are logged, along with phpMyAdmin behind it:
	// panel sign-ins are posts to /login, and the file manager asks for
	// .env and ../ paths, so reading that file would have the panel's own
	// use counted as an attack on it.
	nginxLogs := "logpath  = " + SiteAccessLogs

	if p.WebLogin.Enabled {
		section(JailWebLogin, p.WebLogin, "filter   = ubuntu-panel-web-login",
			"port     = http,https", nginxLogs, "backend  = auto")
	}

	if p.WebScan.Enabled {
		section(JailWebScan, p.WebScan, "filter   = ubuntu-panel-web-scan",
			"port     = http,https", nginxLogs, "backend  = auto")
	}

	if p.Mail.Enabled {
		source := []string{"backend  = systemd"}
		if env.MailLog {
			source = []string{"logpath  = /var/log/mail.log", "backend  = auto"}
		}

		section(JailMail, p.Mail, append([]string{"filter   = postfix[mode=auth]",
			"port     = smtp,465,submission"}, source...)...)
		section(JailMailIMAP, p.Mail, append([]string{"filter   = dovecot",
			"port     = pop3,pop3s,imap,imaps,submission,465,sieve"}, source...)...)
	}

	// An address banned again and again by any jail is banned from every port
	// for much longer.
	if p.Recidive.Enabled {
		source := []string{"logpath  = /var/log/fail2ban.log", "backend  = auto"}
		if !env.Fail2ban {
			source = []string{"backend  = systemd", "journalmatch = _SYSTEMD_UNIT=fail2ban.service"}
		}

		section(JailRecidive, p.Recidive, append([]string{"filter   = recidive",
			"banaction = iptables-allports", "protocol = all"}, source...)...)
	}

	return file.String()
}

// ApplyJails writes the optional jails and has fail2ban load them. When the
// result will not load, the previous files are put back and the reason given:
// a jail that takes fail2ban down takes the SSH jail with it.
func ApplyJails(p Protection, ignoreIPs []string) error {
	if !p.AnyJail() {
		if !proc.Exists(protectJailPath, true) {
			return nil
		}

		proc.Run("sudo rm -f "+protectJailPath, time.Minute)

		if result := proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute); !result.OK() {
			return fmt.Errorf("fail2ban would not reload: %s", lastLines(result.Output, 4))
		}

		return nil
	}

	if !proc.Which("fail2ban-client") {
		return errors.New("this needs fail2ban, which is not installed — the SSH protection tab installs it")
	}

	if (p.WebLogin.Enabled || p.WebScan.Enabled) && !proc.Ok("sudo sh -c 'ls "+SiteAccessLogs+"' >/dev/null 2>&1") {
		return errors.New("website protection reads the sites' access logs, and there are none yet — " +
			"add a site first, then turn it on")
	}

	if p.Mail.Enabled && !proc.Ok("command -v postfix >/dev/null 2>&1 || test -x /usr/sbin/postfix") {
		return errors.New("mail login protection needs the mail server, which is not installed")
	}

	env := JailEnv{
		IgnoreIPs: ignoreIPs,
		MailLog:   proc.Ok("sudo test -s /var/log/mail.log"),
		Fail2ban:  proc.Ok("sudo test -s /var/log/fail2ban.log"),
	}

	previous, _ := proc.ReadFile(protectJailPath, true)

	for path, contents := range map[string]string{
		webLoginFilter:    webLoginFilterFile,
		webScanFilterPath: webScanFilterFile,
		protectJailPath:   ProtectJailFile(p, env),
	} {
		if err := proc.PutFile(path, contents, true); err != nil {
			return err
		}
	}

	check := "sudo fail2ban-client reload 2>&1 && sleep 1"
	for _, jail := range p.ProtectJails() {
		check += " && sudo fail2ban-client status " + jail + " >/dev/null 2>&1"
	}

	if result := proc.Run(check, 2*time.Minute); result.OK() {
		return nil
	}

	output := proc.Run("sudo fail2ban-client -t 2>&1 | tail -n 4", time.Minute).Output

	if previous != "" {
		proc.PutFile(protectJailPath, previous, true)
	} else {
		proc.Run("sudo rm -f "+protectJailPath, time.Minute)
	}
	proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute)

	return fmt.Errorf("fail2ban would not load the new jails, so the previous ones were kept: %s",
		lastLines(output, 4))
}

// JailsCurrent is whether the files on disk are what ApplyJails would write,
// so a panel start does not reload fail2ban for nothing.
func JailsCurrent(p Protection, ignoreIPs []string) bool {
	env := JailEnv{
		IgnoreIPs: ignoreIPs,
		MailLog:   proc.Ok("sudo test -s /var/log/mail.log"),
		Fail2ban:  proc.Ok("sudo test -s /var/log/fail2ban.log"),
	}

	jail, _ := proc.ReadFile(protectJailPath, true)
	login, _ := proc.ReadFile(webLoginFilter, true)
	scan, _ := proc.ReadFile(webScanFilterPath, true)

	return jail == ProtectJailFile(p, env) && login == webLoginFilterFile && scan == webScanFilterFile
}

// ProtectStatus asks fail2ban about each optional jail that is on.
func ProtectStatus(p Protection) map[string]JailStatus {
	statuses := map[string]JailStatus{}

	for _, jail := range p.ProtectJails() {
		statuses[jail] = jailStatus(jail, protectJailPath)
	}

	return statuses
}

// UnbanFrom lets an address out of one of the optional jails.
func UnbanFrom(jail, address string) error {
	return unban(jail, address)
}

// SiteAccessLogs is the access logs the website jails read: one per site and
// per webmail host, each named after its hostname. Not nginx's own
// access.log, which holds the panel's requests.
const SiteAccessLogs = "/var/log/nginx/*-access.log"

// AccessLogs is the access logs the web jails read, for noticing a site that
// was added since fail2ban expanded the pattern.
func AccessLogs() string {
	return strings.TrimSpace(proc.Run("sudo sh -c 'ls "+SiteAccessLogs+"' 2>/dev/null | sort", 30*time.Second).Output)
}

// ReloadJails has fail2ban read the jails again, so a pattern like
// *access.log picks up the file a new site's vhost created.
func ReloadJails() {
	proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute)
}

// ------------------------------------------------------------------ nginx

// ProtectNginxPath is the nginx file this writes: in conf.d, so it is inside
// the http block every vhost is, and limits set there apply to all of them.
const ProtectNginxPath = "/etc/nginx/conf.d/panel-protect.conf"

// NginxEnv is what the nginx file needs to know.
type NginxEnv struct {
	// IgnoreIPs are never rate limited: the never-ban list.
	IgnoreIPs []string

	// ExemptHosts are never flood limited: the panel itself, and applications
	// like Nextcloud whose clients make hundreds of requests to sync.
	ExemptHosts []string
}

// staticPattern is the files nginx serves itself; they are not flood limited.
const staticPattern = `\.(?:css|js|mjs|map|png|jpe?g|gif|webp|avif|svg|ico|bmp|woff2?|ttf|otf|eot|mp4|webm|mp3|ogg|wav|pdf|zip|txt)$`

// NginxFile renders the rate limits and real-IP directives that are on, or
// "" when none is.
func NginxFile(p Protection, env NginxEnv) string {
	if !p.RealIP && !p.RateLogin.Enabled && !p.RateFlood.Enabled {
		return ""
	}

	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — the Protection tab on the Security page.\n")
	file.WriteString("# Edits made here are overwritten the next time it saves.\n")

	// With the visitor's address restored, Cloudflare's own addresses never
	// reach the limits below; without it they are what every proxied visitor
	// looks like, and are left out of the limits so one busy site behind
	// Cloudflare is not throttled as if it were one client.
	if p.RealIP {
		file.WriteString("\n# The visitor's address, from Cloudflare's header — trusted only from Cloudflare.\n")
		for _, cidr := range CloudflareRanges {
			fmt.Fprintf(&file, "set_real_ip_from %s;\n", cidr)
		}
		file.WriteString(realIPHeaderLine + "\n")
	}

	if !p.RateLogin.Enabled && !p.RateFlood.Enabled {
		return file.String()
	}

	file.WriteString("\n# Addresses that are never rate limited.\n")
	file.WriteString("geo $panel_limit_exempt {\n    default 0;\n    127.0.0.0/8 1;\n    ::1 1;\n")
	listed := map[string]bool{}
	for _, address := range env.IgnoreIPs {
		// Loopback is in the block already; nginx warns about a network twice.
		if ip := net.ParseIP(address); ip != nil && ip.IsLoopback() {
			continue
		}
		if ValidAddress(address) && !listed[address] {
			listed[address] = true
			fmt.Fprintf(&file, "    %s 1;\n", address)
		}
	}
	if !p.RealIP {
		for _, cidr := range CloudflareRanges {
			fmt.Fprintf(&file, "    %s 1;\n", cidr)
		}
	}
	file.WriteString("}\n")

	file.WriteString(limitStatusLine + "\n" + limitLogLine + "\n")

	if p.RateLogin.Enabled {
		file.WriteString("\n# Login forms and XML-RPC: a few posts a minute per address.\n")
		file.WriteString("map \"$panel_limit_exempt $request_method $uri\" $panel_limit_login {\n")
		file.WriteString("    default \"\";\n")
		file.WriteString(`    "~^0 POST /(?:wp-login\.php|xmlrpc\.php)$" $binary_remote_addr;` + "\n")
		file.WriteString(`    "~^0 POST .*/(?:login|signin|sign-in|user/login|admin/login|auth/login|administrator/index\.php)/?$" $binary_remote_addr;` + "\n")
		file.WriteString("}\n")
		fmt.Fprintf(&file, "limit_req_zone $panel_limit_login zone=panel_login:10m rate=%dr/m;\n", p.RateLogin.Rate)
		fmt.Fprintf(&file, "limit_req zone=panel_login burst=%d nodelay;\n", p.RateLogin.Burst)
	}

	if p.RateFlood.Enabled {
		file.WriteString("\n# Hostnames whose clients are busy by design.\n")
		file.WriteString("map $host $panel_limit_host_exempt {\n    default 0;\n")
		hosts := append([]string{}, env.ExemptHosts...)
		sort.Strings(hosts)
		for _, host := range hosts {
			if validHost(host) {
				fmt.Fprintf(&file, "    %s 1;\n", strings.ToLower(host))
			}
		}
		file.WriteString("}\n")

		file.WriteString("\n# Everything that reaches PHP or an app, per address.\n")
		file.WriteString("map \"$panel_limit_exempt$panel_limit_host_exempt $uri\" $panel_limit_flood {\n")
		file.WriteString("    default $binary_remote_addr;\n")
		file.WriteString(`    "~^(?:1.|.1) " "";` + "\n")
		file.WriteString(`    "~*` + staticPattern + `" "";` + "\n")
		file.WriteString("}\n")
		fmt.Fprintf(&file, "limit_req_zone $panel_limit_flood zone=panel_flood:20m rate=%dr/s;\n", p.RateFlood.Rate)
		fmt.Fprintf(&file, "limit_req zone=panel_flood burst=%d nodelay;\n", p.RateFlood.Burst)
	}

	return file.String()
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}

	for _, r := range host {
		if !(r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}

	return true
}

// sharedLines are the lines in the file nginx allows only once in the http
// block, and another file may set already: a hand-written Cloudflare include,
// a rate limit from another tool or panel. nginx refuses the whole
// configuration over a second one, so when it says one of these is a
// duplicate the file is written without it, and the setting already there is
// the one that applies. Everything else in the file is named panel_* and
// cannot clash.
var sharedLines = []string{realIPHeaderLine, limitStatusLine, limitLogLine}

const (
	realIPHeaderLine = "real_ip_header CF-Connecting-IP;"
	limitStatusLine  = "limit_req_status 429;"
	limitLogLine     = "limit_req_log_level warn;"
)

// withoutLine is contents with the whole line line removed.
func withoutLine(contents, line string) string {
	return strings.Replace(contents, line+"\n", "", 1)
}

// withoutShared is contents without any of sharedLines.
func withoutShared(contents string) string {
	for _, line := range sharedLines {
		contents = withoutLine(contents, line)
	}
	return contents
}

// duplicateShared is the line of sharedLines nginx's test output says is set
// twice, or "".
func duplicateShared(output string) string {
	for _, line := range sharedLines {
		directive := strings.Fields(line)[0]
		if strings.Contains(output, `"`+directive+`" directive is duplicate`) {
			return line
		}
	}
	return ""
}

// sameFile is whether the file on disk is the one wanted, allowing for shared
// lines left out because another file sets them.
func sameFile(onDisk, wanted string) bool {
	return onDisk == wanted || withoutShared(onDisk) == withoutShared(wanted)
}

// whereElse names what else in /etc/nginx could clash with this file: other
// files setting the shared directives, and every include of conf.d — two of
// those would load this file twice.
func whereElse() string {
	found := proc.Run(`sudo grep -RnE '^[[:space:]]*(real_ip_header|limit_req_status|limit_req_log_level|include[[:space:]]+[^;]*conf\.d)' /etc/nginx 2>/dev/null | grep -v `+proc.Quote(ProtectNginxPath)+` | head -n 8`, time.Minute)
	lines := strings.TrimSpace(found.Output)
	if lines == "" {
		return ""
	}
	return " — check these lines in nginx's configuration: " + strings.Join(strings.Split(lines, "\n"), "; ")
}

// ApplyNginx writes the nginx file, or removes it, and reloads nginx when
// nginx accepts the result. Refused, the previous file is put back.
func ApplyNginx(contents string) error {
	if !proc.Which("nginx") && !proc.Ok("test -x /usr/sbin/nginx") {
		if contents == "" {
			return nil
		}
		return errors.New("this needs nginx, which is not installed")
	}

	previous, readErr := proc.ReadFile(ProtectNginxPath, true)
	existed := readErr == nil

	if contents == "" {
		if !existed {
			return nil
		}
		proc.Run("sudo rm -f "+ProtectNginxPath, time.Minute)
	} else {
		// A file written without the shared lines, because another file sets
		// them, is still the file wanted.
		if existed && sameFile(previous, contents) {
			return nil
		}
		if err := proc.PutFile(ProtectNginxPath, contents, true); err != nil {
			return err
		}
	}

	check := proc.Run("sudo nginx -t 2>&1", time.Minute)
	for tries := 0; !check.OK() && contents != "" && tries < len(sharedLines); tries++ {
		line := duplicateShared(check.Output)
		if line == "" || !strings.Contains(contents, line+"\n") {
			break
		}
		contents = withoutLine(contents, line)
		if err := proc.PutFile(ProtectNginxPath, contents, true); err != nil {
			return err
		}
		check = proc.Run("sudo nginx -t 2>&1", time.Minute)
	}

	if !check.OK() {
		if existed {
			proc.PutFile(ProtectNginxPath, previous, true)
		} else {
			proc.Run("sudo rm -f "+ProtectNginxPath, time.Minute)
		}

		hint := ""
		if strings.Contains(check.Output, "duplicate") || strings.Contains(check.Output, "already bound") {
			hint = whereElse()
		}
		return fmt.Errorf("nginx refused the new settings, so the previous ones were kept: %s%s",
			lastLines(check.Output, 3), hint)
	}

	if result := proc.Run("sudo systemctl reload nginx 2>&1", time.Minute); !result.OK() {
		return fmt.Errorf("nginx would not reload: %s", lastLines(result.Output, 3))
	}

	return nil
}

// NginxCurrent is the file on disk, "" when there is none.
func NginxCurrent() string {
	contents, err := proc.ReadFile(ProtectNginxPath, true)
	if err != nil {
		return ""
	}

	return contents
}

// ------------------------------------------------------------------ kernel

const kernelPath = "/etc/sysctl.d/60-ubuntu-panel.conf"

// kernelSettings are what a public server should have and Ubuntu does not all
// set: SYN cookies and a deeper backlog against SYN floods, and no ICMP
// redirects or source-routed packets, which only matter to routers and are a
// way to redirect a host's traffic. Nothing here touches forwarding, so
// Docker and VPNs keep working.
var kernelSettings = [][2]string{
	{"net.ipv4.tcp_syncookies", "1"},
	{"net.ipv4.tcp_max_syn_backlog", "4096"},
	{"net.ipv4.tcp_rfc1337", "1"},
	{"net.ipv4.conf.all.accept_redirects", "0"},
	{"net.ipv4.conf.default.accept_redirects", "0"},
	{"net.ipv4.conf.all.secure_redirects", "0"},
	{"net.ipv4.conf.default.secure_redirects", "0"},
	{"net.ipv4.conf.all.send_redirects", "0"},
	{"net.ipv4.conf.default.send_redirects", "0"},
	{"net.ipv4.conf.all.accept_source_route", "0"},
	{"net.ipv4.conf.default.accept_source_route", "0"},
	{"net.ipv6.conf.all.accept_redirects", "0"},
	{"net.ipv6.conf.default.accept_redirects", "0"},
	{"net.ipv6.conf.all.accept_source_route", "0"},
	{"net.ipv6.conf.default.accept_source_route", "0"},
	{"net.ipv4.icmp_echo_ignore_broadcasts", "1"},
	{"net.ipv4.icmp_ignore_bogus_error_responses", "1"},
	{"kernel.dmesg_restrict", "1"},
	{"kernel.kptr_restrict", "1"},
}

// KernelFile renders the sysctl file.
func KernelFile() string {
	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — network hardening, from the Security page.\n")
	file.WriteString("# Turning it off there puts back the values these replaced.\n")

	for _, setting := range kernelSettings {
		fmt.Fprintf(&file, "%s = %s\n", setting[0], setting[1])
	}

	return file.String()
}

// KernelValues reads the current value of every setting the file sets.
func KernelValues() map[string]string {
	values := map[string]string{}

	for _, setting := range kernelSettings {
		output := strings.TrimSpace(proc.Run("sysctl -n "+setting[0]+" 2>/dev/null", 10*time.Second).Output)
		if output != "" && !strings.ContainsAny(output, "\n;") {
			values[setting[0]] = output
		}
	}

	return values
}

// KernelApplied is whether the file is on disk.
func KernelApplied() bool { return proc.Exists(kernelPath, true) }

// ApplyKernel writes the file and loads it, or removes it and puts back the
// values it replaced: deleting a sysctl file changes nothing until a reboot.
func ApplyKernel(on bool, previous map[string]string) error {
	if on {
		if err := proc.PutFile(kernelPath, KernelFile(), true); err != nil {
			return err
		}

		if result := proc.Run("sudo sysctl -p "+kernelPath+" 2>&1", time.Minute); !result.OK() {
			// A setting this kernel does not have (IPv6 disabled, a container)
			// is reported but the rest are applied; only a total failure is one.
			if !strings.Contains(result.Output, " = ") {
				return fmt.Errorf("the kernel refused the settings: %s", lastLines(result.Output, 3))
			}
		}

		return nil
	}

	proc.Run("sudo rm -f "+kernelPath, time.Minute)

	for _, setting := range kernelSettings {
		value, ok := previous[setting[0]]
		if !ok {
			continue
		}

		if _, err := strconv.Atoi(value); err != nil {
			continue
		}

		proc.Run(fmt.Sprintf("sudo sysctl -w %s=%s >/dev/null 2>&1", setting[0], value), 10*time.Second)
	}

	return nil
}
