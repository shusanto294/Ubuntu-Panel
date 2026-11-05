package security

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The protections that act inside each site's server block rather than in the
// http block every vhost shares: refusing attack tools, refusing countries,
// the browser check of "under attack" mode, and the WAF.
//
// nginx allows `if` and `return` only in a server or location, and conf.d is
// the http block. So every site's vhost includes GuardDir/*.conf (the sites
// package writes the line), and the panel puts what is on into one file
// there. A glob, so an empty directory — or none — is no error, and switching
// these on and off never touches a vhost. The panel's own vhost does not
// include it: whatever is refused, the owner can still reach the panel.
//
// The variables the guard file tests are mapped in the http block, in
// ProtectNginxPath, beside the rate limits.

// GuardDir is the directory every site's server block includes.
const GuardDir = "/etc/nginx/panel-guard"

// GuardInclude is the line the sites package writes into each server block.
const GuardInclude = "include " + GuardDir + "/*.conf;"

// GuardPath is the panel's file in it.
const GuardPath = GuardDir + "/guard.conf"

// guardDataDir holds what the http block includes and conf.d must not load
// on its own: the country lists.
const guardDataDir = "/etc/nginx/panel-guard-data"

// CountriesPath is the country lists, as an nginx geo block.
const CountriesPath = guardDataDir + "/countries.conf"

// SiteErrorLogs are the sites' error logs, where the WAF's refusals land.
const SiteErrorLogs = "/var/log/nginx/*-error.log"

// UFWLog is where rsyslog puts the firewall's log lines.
const UFWLog = "/var/log/ufw.log"

// ------------------------------------------------------------------ rules

// ConnLimit is how many requests one address may have open at once — not
// counting images, CSS and JS — and the short timeouts that stop a client
// holding connections open by sending slowly (Slowloris).
type ConnLimit struct {
	Enabled bool `json:"enabled"`
	Max     int  `json:"max"`
}

func (c ConnLimit) Validate() map[string]string {
	problems := map[string]string{}
	if c.Max < 5 || c.Max > 10000 {
		problems["max"] = "Allow between 5 and 10,000 open requests per address."
	}
	return problems
}

// The two ways a country list is read.
const (
	CountriesBlock = "block" // the listed countries are refused
	CountriesAllow = "allow" // only the listed countries are let in
)

// CountryRule refuses visitors by country, on the websites. Codes are ISO
// 3166 two-letter codes, lower case.
type CountryRule struct {
	Enabled bool     `json:"enabled"`
	Mode    string   `json:"mode"`
	Codes   []string `json:"codes"`
}

var countryCode = regexp.MustCompile(`^[a-z]{2}$`)

// CleanCountries is the codes from a form — commas, spaces or lines — lower
// cased, checked and deduplicated.
func CleanCountries(text string) ([]string, string) {
	seen := map[string]bool{}
	codes := []string{}

	for _, code := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		if !countryCode.MatchString(code) {
			return nil, fmt.Sprintf("%q is not a two-letter country code.", code)
		}
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}

	sort.Strings(codes)

	return codes, ""
}

func (c CountryRule) Validate() map[string]string {
	problems := map[string]string{}

	if c.Mode != CountriesBlock && c.Mode != CountriesAllow {
		problems["mode"] = "Choose whether the countries are refused or the only ones let in."
	}
	if len(c.Codes) == 0 {
		problems["codes"] = "Choose at least one country."
	}
	if len(c.Codes) > 250 {
		problems["codes"] = "That is more countries than there are."
	}

	return problems
}

// ChallengeRule is "under attack" mode: on the chosen sites, a browser has to
// run a moment of JavaScript before its first page, and is then let through
// for ChallengeLifetime. A flood from a script that does not run JavaScript
// gets a small static page from nginx and never reaches PHP. Sites are named
// by their primary domain.
type ChallengeRule struct {
	Enabled bool     `json:"enabled"`
	Sites   []string `json:"sites"`
}

func (c ChallengeRule) Validate() map[string]string {
	problems := map[string]string{}
	if len(c.Sites) == 0 {
		problems["sites"] = "Choose at least one site to put under attack mode."
	}
	return problems
}

// WAFRule is the web application firewall: ModSecurity with the OWASP Core
// Rule Set, on every site. Block false is detection only — the rules log what
// they would refuse and refuse nothing, which is how a WAF should start on
// sites nobody has tested it with. Blocking, an address refused MaxRetry
// times within FindTime is banned for BanTime.
type WAFRule struct {
	Enabled  bool `json:"enabled"`
	Block    bool `json:"block"`
	MaxRetry int  `json:"max_retry"`
	FindTime int  `json:"find_time"`
	BanTime  int  `json:"ban_time"`
}

// Bans is whether the WAF's refusals are banned for: it refuses, and the ban
// threshold is not "never" (0).
func (w WAFRule) Bans() bool { return w.Enabled && w.Block && w.MaxRetry > 0 }

func (w WAFRule) Validate() map[string]string {
	if w.MaxRetry == 0 {
		return map[string]string{}
	}
	return JailRule{MaxRetry: w.MaxRetry, FindTime: w.FindTime, BanTime: w.BanTime}.Validate()
}

// ------------------------------------------------------------------ attack tools

// attackTools are the user agents of tools that only attack: SQL injection,
// vulnerability and directory scanners, password crackers. Each says who it is
// unless told not to, and a surprising share of attacks never tell it. Not
// curl, wget or python-requests: those are how monitors, webhooks and
// people's own scripts talk to a site.
var attackTools = []string{
	"sqlmap", "nikto", "Nmap Scripting Engine", "masscan", "zgrab", "WPScan", "Acunetix", "Netsparker",
	"Nessus", "OpenVAS", "DirBuster", "gobuster", "feroxbuster", "Fuzz Faster U Fool", "wfuzz",
	"dirsearch", "Nuclei", "Havij", "w3af", "WhatWeb", "Jorgee", "ZmEu", "Morfeus", "fimap", "commix",
	"Arachni", "skipfish", "XSStrike", "(Hydra)",
}

// attackToolAgents is attackTools as one regular expression, for fail2ban and
// nginx alike.
func attackToolAgents() string {
	quoted := make([]string, len(attackTools))
	for i, tool := range attackTools {
		quoted[i] = regexp.QuoteMeta(tool)
	}
	return strings.Join(quoted, "|")
}

// ------------------------------------------------------------------ filters

// webFloodFilterFile counts requests refused by the rate and connection
// limits, which answer 429. An application's own 429 counts too, which is
// what an application means by it. Useful bots are never limited, and a 429 an
// application gives one is not a ban.
func webFloodFilterFile() string {
	return "# Managed by Ubuntu Panel: addresses that keep hitting the rate limits.\n" +
		"[Definition]\n" +
		"failregex = " + accessPrefix + `[^"]*" 429 ` + "\n" +
		"ignoreregex = " + anyPrefix + `[^"]*" \d{3} \S+ "[^"]*" "[^"]*(?i:` + goodBotAgents() + `)` + "\n"
}

// webToolsFilterFile counts requests from attack tools, by user agent,
// whatever the answer.
func webToolsFilterFile() string {
	return "# Managed by Ubuntu Panel: attack tools (sqlmap, nikto, WPScan…).\n" +
		"[Definition]\n" +
		"failregex = " + accessPrefix + `[^"]*" \d{3} \S+ "[^"]*" "[^"]*(?i:` + attackToolAgents() + `)` + "\n" +
		"ignoreregex =\n"
}

// webWAFFilterFile counts ModSecurity's refusals in the sites' error logs:
//
//	2026/11/05 10:00:00 [error] 811#811: *5 [client 203.0.113.9] ModSecurity: Access denied with code 403 …
//
// Detection only logs "Warning." instead, which is never counted.
const webWAFFilterFile = "# Managed by Ubuntu Panel: requests the WAF refused.\n" +
	"[Definition]\n" +
	`failregex = \[client <HOST>\] ModSecurity: Access denied` + "\n" +
	"ignoreregex =\n"

// portScanFilterFile counts inbound connections ufw refused:
//
//	[UFW BLOCK] IN=eth0 OUT= MAC=… SRC=203.0.113.9 DST=… PROTO=TCP SPT=… DPT=23 …
//
// OUT= is empty only for packets arriving, so the server's own refused
// outbound traffic is never counted.
const portScanFilterFile = "# Managed by Ubuntu Panel: port scans the firewall refused.\n" +
	"[Definition]\n" +
	`failregex = \[UFW BLOCK\] IN=\S+ OUT= .*SRC=<HOST> ` + "\n" +
	"ignoreregex =\n"

// ipsetAction is fail2ban's ipset action for every port, or "" when this
// machine cannot use it.
func ipsetAction() string {
	if !proc.Ok("command -v ipset >/dev/null 2>&1 || test -x /usr/sbin/ipset") {
		return ""
	}
	if proc.Ok("test -f /etc/fail2ban/action.d/iptables-ipset-proto6-allports.conf") {
		return "iptables-ipset-proto6-allports"
	}
	return ""
}

// UFWLogging is whether ufw logs what it refuses, which the port scan jail
// reads.
func UFWLogging() bool {
	output := proc.Run("sudo ufw status verbose 2>/dev/null", 30*time.Second).Output
	return strings.Contains(output, "Logging: on")
}

// EnableUFWLogging has ufw log refused connections, at its lowest level: the
// refusals and nothing else.
func EnableUFWLogging() error {
	if UFWLogging() {
		return nil
	}
	if result := proc.Run("sudo ufw logging low 2>&1", time.Minute); !result.OK() {
		return fmt.Errorf("ufw would not turn its log on: %s", lastLines(result.Output, 2))
	}
	return nil
}

// ------------------------------------------------------------------ guard file

// challengeMap is the variable that sends a request to the browser check.
//
// The key is everything the decision needs, so one map makes it: never-ban
// list (or Cloudflare without real addresses), useful bot, site under attack,
// the browser's pass, and the path. A pass is $secure_link: "1" is valid, "0"
// expired, empty none or forged. /.well-known/ is never checked — the check's
// own endpoint is there, and so are certificate renewals.
func challengeMap() string {
	return "\n# Under attack mode: which requests get the browser check.\n" +
		"map $host $panel_challenge_host {\n    default 0;\n    include " + guardDataDir + "/challenge-hosts.conf;\n}\n" +
		"map \"$panel_limit_exempt$panel_limit_bot$panel_challenge_host:$secure_link:$uri\" $panel_challenge {\n" +
		"    default 0;\n" +
		`    "~^001:1:" 0;` + "\n" +
		`    "~^001:[^:]*:/\.well-known/" 0;` + "\n" +
		`    "~^001:" 1;` + "\n" +
		"}\n"
}

// ChallengeHostsPath lists the hostnames under attack mode, for the map above.
const ChallengeHostsPath = guardDataDir + "/challenge-hosts.conf"

// ChallengeHostsFile renders that list.
func ChallengeHostsFile(hosts []string) string {
	var file strings.Builder
	file.WriteString("# Managed by Ubuntu Panel — under attack mode, the Protection tab.\n")

	sorted := append([]string{}, hosts...)
	sort.Strings(sorted)
	seen := map[string]bool{}
	for _, host := range sorted {
		host = strings.ToLower(host)
		if validHost(host) && !seen[host] {
			seen[host] = true
			fmt.Fprintf(&file, "%s 1;\n", host)
		}
	}

	return file.String()
}

// ChallengePath is where the check's page asks for, and hands back, its seed.
// Under /.well-known/, which the sites' own rules leave alone.
const ChallengePath = "/.well-known/panel-challenge"

// PanelChallengePath is that endpoint inside the panel, which nginx proxies
// it to.
const PanelChallengePath = "/.panel-challenge"

// GuardFile renders the server-block file, or "" when nothing in it is on.
func GuardFile(p Protection, env NginxEnv) string {
	if !p.Tools.Enabled && !p.Countries.Enabled && !p.Challenge.Enabled && !p.WAF.Enabled {
		return ""
	}

	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — the Protection tab on the Security page.\n")
	file.WriteString("# Included in every site's server block. Edits are overwritten.\n")

	if p.Tools.Enabled {
		file.WriteString("\n# Attack tools: closed without an answer.\n")
		file.WriteString("if ($panel_attack_tool) {\n    return 444;\n}\n")
	}

	if p.Countries.Enabled {
		file.WriteString("\n# Countries this server does not serve.\n")
		file.WriteString("if ($panel_country_refused) {\n    return 403;\n}\n")
	}

	if p.Challenge.Enabled {
		// The browser's pass is two cookies — the hash and when it expires —
		// that only the panel can make: an MD5 of the expiry, the address, the
		// hostname and a secret, which nginx checks without asking anyone. A
		// pass is for one address, so a botnet cannot share one.
		file.WriteString("\n# Under attack mode: a browser check before the first page.\n")
		fmt.Fprintf(&file, "location = %s {\n", ChallengePath)
		fmt.Fprintf(&file, "    proxy_pass http://127.0.0.1:%d%s;\n", env.BindPort, PanelChallengePath)
		file.WriteString("    proxy_set_header Host $host;\n")
		file.WriteString("    proxy_set_header X-Real-IP $remote_addr;\n")
		file.WriteString("    proxy_set_header X-Forwarded-For $remote_addr;\n")
		file.WriteString("    proxy_set_header X-Forwarded-Proto $scheme;\n")
		file.WriteString("}\n")
		fmt.Fprintf(&file, "secure_link $cookie_%s,$cookie_%s;\n", ChallengeCookie, ChallengeExpiresCookie)
		fmt.Fprintf(&file, "secure_link_md5 \"$secure_link_expires$remote_addr$host %s\";\n", env.ChallengeSecret)

		// A named location, because an error_page to a named location does
		// not go through the server's rewrite phase again — where the check
		// would send it straight back here.
		file.WriteString("if ($panel_challenge) {\n    return 418;\n}\n")
		file.WriteString("error_page 418 = @panel_challenge;\n")
		file.WriteString("location @panel_challenge {\n")
		file.WriteString("    default_type text/html;\n")
		file.WriteString("    add_header Cache-Control \"no-store\" always;\n")
		file.WriteString("    add_header Retry-After \"5\" always;\n")
		file.WriteString("    return 503 " + nginxQuote(challengePage) + ";\n")
		file.WriteString("}\n")
	}

	if p.WAF.Enabled {
		file.WriteString("\n# The web application firewall.\n")
		file.WriteString("modsecurity on;\n")
		file.WriteString("modsecurity_rules_file " + WAFRulesPath + ";\n")
	}

	return file.String()
}

// nginxQuote is text as an nginx double-quoted string. A $ would start a
// variable and cannot be escaped, so the page has none.
func nginxQuote(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

// challengePage is what a browser under attack mode sees for a second or two:
// it asks the panel for a seed, waits, hands it back for its pass, and loads
// the page it asked for. Written without a $ (see nginxQuote) and with only
// single quotes, so it reads in the configuration as it is.
const challengePage = `<!doctype html><html lang='en'><head><meta charset='utf-8'>` +
	`<meta name='viewport' content='width=device-width,initial-scale=1'><meta name='robots' content='noindex'>` +
	`<title>Checking your browser</title><style>` +
	`body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;` +
	`font-family:system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;background:#f8fafc;color:#0f172a}` +
	`main{max-width:28rem;padding:2rem 1rem;text-align:center}` +
	`.s{width:2.5rem;height:2.5rem;margin:0 auto 1.5rem;border:3px solid #cbd5e1;border-top-color:#2563eb;` +
	`border-radius:50%;animation:r 1s linear infinite}@keyframes r{to{transform:rotate(360deg)}}` +
	`h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:0;color:#475569;font-size:.95rem;line-height:1.5}` +
	`@media (prefers-color-scheme:dark){body{background:#0f172a;color:#f1f5f9}p{color:#94a3b8}` +
	`.s{border-color:#334155;border-top-color:#60a5fa}}</style></head><body><main><div class='s'></div>` +
	`<h1>Checking your browser</h1><p id='m'>This site is busy. You will be let through in a moment.</p>` +
	`<noscript><p>Turn on JavaScript to continue.</p></noscript></main><script>(function(){` +
	`var m=document.getElementById('m'),u='` + ChallengePath + `',k='panel_challenge_tries',n=0;` +
	`function f(t){m.textContent=t}function no(){f('Your browser could not be checked. Reload the page to try again.')}` +
	`try{n=+(sessionStorage.getItem(k)||0);sessionStorage.setItem(k,n+1)}catch(e){}` +
	`if(n>2){try{sessionStorage.removeItem(k)}catch(e){}` +
	`f('Your browser did not keep the cookie this site needs. Allow cookies for it and reload.');return}` +
	`fetch(u,{credentials:'same-origin',cache:'no-store'}).then(function(r){if(!r.ok)throw 0;return r.json()})` +
	`.then(function(j){setTimeout(function(){fetch(u,{method:'POST',credentials:'same-origin',cache:'no-store',` +
	`headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'seed='+encodeURIComponent(j.seed)})` +
	`.then(function(r){if(!r.ok)throw 0;location.reload()}).catch(no)},j.wait)}).catch(no)})();</script></body></html>`

// ------------------------------------------------------------------ challenge passes

// The pass's cookies, and how long one lasts.
const (
	ChallengeCookie        = "panel_hv"
	ChallengeExpiresCookie = "panel_he"
	ChallengeLifetime      = 12 * time.Hour
)

// The seed a browser is handed, and the least and most time between being
// handed it and handing it back. The wait is the price: one script asking
// for passes for every address it has must wait as long for each.
const (
	ChallengeWait   = 1500 * time.Millisecond
	challengeMaxAge = 5 * time.Minute
)

// ChallengeToken is the hash nginx's secure_link_md5 expects for a pass:
// base64url of MD5("<expires><address><host> <secret>").
func ChallengeToken(secret, address, host string, expires int64) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%d%s%s %s", expires, address, strings.ToLower(host), secret)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ChallengeSeed is a seed for one address and hostname, issued at now:
// "<unix ms>.<signature>". Nothing is stored; the signature is the record.
func ChallengeSeed(secret, address, host string, now time.Time) string {
	stamp := strconv.FormatInt(now.UnixMilli(), 10)
	return stamp + "." + seedSignature(secret, address, host, stamp)
}

// ValidChallengeSeed is whether a seed handed back at now was issued by this
// panel, for this address and hostname, long enough ago and not too long.
func ValidChallengeSeed(secret, address, host, seed string, now time.Time) bool {
	stamp, signature, found := strings.Cut(seed, ".")
	if !found {
		return false
	}

	issued, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return false
	}

	age := now.Sub(time.UnixMilli(issued))
	if age < ChallengeWait-250*time.Millisecond || age > challengeMaxAge {
		return false
	}

	return hmac.Equal([]byte(signature), []byte(seedSignature(secret, address, host, stamp)))
}

func seedSignature(secret, address, host, stamp string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "|" + address + "|" + strings.ToLower(host)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// SecureLinkAvailable is whether this nginx was built with the module the
// browser check's passes are checked by. Ubuntu's is.
func SecureLinkAvailable() bool {
	return proc.Ok("nginx -V 2>&1 | grep -q http_secure_link_module")
}

// ------------------------------------------------------------------ countries

// countryLists is where each country's address ranges come from: ipdeny.com's
// aggregated zones, refreshed weekly, one CIDR a line.
var countryLists = []string{
	"https://www.ipdeny.com/ipblocks/data/aggregated/%s-aggregated.zone",
	"https://www.ipdeny.com/ipv6/ipaddresses/aggregated/%s-aggregated.zone",
}

// FetchCountries downloads the ranges of every country. A country with no
// IPv6 list is fine; one with no IPv4 list is a code nobody publishes ranges
// for, and refused.
func FetchCountries(codes []string) (map[string][]string, error) {
	client := &http.Client{Timeout: 45 * time.Second}
	ranges := map[string][]string{}

	for _, code := range codes {
		for family, pattern := range countryLists {
			list, err := fetchCountryList(client, fmt.Sprintf(pattern, code))
			if err != nil {
				if family == 0 {
					return nil, fmt.Errorf("could not download the address ranges for %s: %w", strings.ToUpper(code), err)
				}
				continue
			}
			ranges[code] = append(ranges[code], list...)
		}
	}

	return ranges, nil
}

func fetchCountryList(client *http.Client, url string) ([]string, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, response.Status)
	}

	list := []string{}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 32<<20))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if _, _, err := net.ParseCIDR(line); err == nil {
			list = append(list, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errors.New("the list was empty")
	}

	return list, nil
}

// CountriesFile renders the ranges as the geo block the http block includes:
// 1 for an address in a listed country.
func CountriesFile(ranges map[string][]string) string {
	var file strings.Builder

	codes := make([]string, 0, len(ranges))
	for code := range ranges {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	file.WriteString("# Managed by Ubuntu Panel — country rules, from ipdeny.com. Refreshed weekly.\n")
	file.WriteString(countriesHeader + strings.Join(codes, " ") + "\n")
	file.WriteString("geo $panel_country_listed {\n    default 0;\n")

	seen := map[string]bool{}
	for _, code := range codes {
		fmt.Fprintf(&file, "    # %s\n", strings.ToUpper(code))
		for _, cidr := range ranges[code] {
			if !seen[cidr] {
				seen[cidr] = true
				fmt.Fprintf(&file, "    %s 1;\n", cidr)
			}
		}
	}

	file.WriteString("}\n")

	return file.String()
}

// countriesHeader is the line naming the countries a file holds.
const countriesHeader = "# Countries: "

// CountriesOnDisk are the countries the file on disk holds, nil for none.
func CountriesOnDisk() []string {
	head := proc.Run("sudo head -n 3 "+CountriesPath+" 2>/dev/null", 30*time.Second).Output
	for _, line := range strings.Split(head, "\n") {
		if rest, found := strings.CutPrefix(line, countriesHeader); found {
			return strings.Fields(rest)
		}
	}
	return nil
}

// WriteCountries downloads the countries' ranges and writes the file. Not
// loaded until nginx is reloaded; the caller decides when.
func WriteCountries(codes []string) error {
	ranges, err := FetchCountries(codes)
	if err != nil {
		return err
	}
	return proc.PutFile(CountriesPath, CountriesFile(ranges), true)
}

// RefreshCountries downloads the lists again and has nginx load them: the
// weekly refresh. The old file is kept if the download or nginx fails.
func RefreshCountries(codes []string) error {
	previous, readErr := proc.ReadFile(CountriesPath, true)

	if err := WriteCountries(codes); err != nil {
		return err
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		if readErr == nil {
			proc.PutFile(CountriesPath, previous, true)
		}
		return fmt.Errorf("nginx refused the refreshed country lists: %s", lastLines(check.Output, 3))
	}

	proc.Run("sudo systemctl reload nginx 2>&1", time.Minute)

	return nil
}

// countryMap decides from the lists who is refused. Never the never-ban list,
// never Cloudflare without real addresses (it would be a country's worth of
// everybody), never a useful bot — a site refusing the country Google crawls
// from leaves Google.
func countryMap(mode string) string {
	refused := "001"
	if mode == CountriesAllow {
		refused = "000"
	}

	return "\n# Countries: who is refused.\n" +
		"include " + CountriesPath + ";\n" +
		"map \"$panel_limit_exempt$panel_limit_bot$panel_country_listed\" $panel_country_refused {\n" +
		"    default 0;\n" +
		"    " + refused + " 1;\n" +
		"}\n"
}

// ------------------------------------------------------------------ WAF

// WAFRulesPath is ModSecurity's configuration, which the guard file names.
// Not in conf.d: it is ModSecurity's language, not nginx's.
const WAFRulesPath = "/etc/nginx/panel-waf/main.conf"

// crsRules is where Ubuntu's modsecurity-crs package puts the rules.
const crsRules = "/usr/share/modsecurity-crs/rules"

// crsSetups are where the package has put the rule set's setup file, newest
// packaging first.
var crsSetups = []string{
	"/etc/modsecurity/crs/crs-setup.conf",
	"/usr/share/modsecurity-crs/crs-setup.conf",
	"/etc/modsecurity/crs-setup.conf",
}

// WAFInstalled is whether nginx's ModSecurity module and the Core Rule Set
// are on the machine.
func WAFInstalled() bool {
	return proc.Ok("ls /etc/nginx/modules-enabled/ 2>/dev/null | grep -qi modsecurity") &&
		proc.Ok("test -f "+crsRules+"/REQUEST-901-INITIALIZATION.conf") && crsSetup() != ""
}

func crsSetup() string {
	for _, path := range crsSetups {
		if proc.Ok("test -f " + path) {
			return path
		}
	}
	return ""
}

// WAFEnv is what ModSecurity's configuration needs to know.
type WAFEnv struct {
	IgnoreIPs   []string // the never-ban list: never inspected
	ExemptHosts []string // Nextcloud and n8n, whose sync traffic trips generic rules
	Setup       string   // the rule set's setup file
}

// WAFFile renders ModSecurity's configuration: the engine's settings, the
// panel's exceptions, then the OWASP Core Rule Set at paranoia level 1 — the
// level meant to run on any site without tuning — with its WordPress
// exclusions on.
func WAFFile(w WAFRule, env WAFEnv) string {
	var file strings.Builder

	engine := "DetectionOnly"
	if w.Block {
		engine = "On"
	}

	file.WriteString("# Managed by Ubuntu Panel — the WAF on the Protection tab. Edits are overwritten.\n")
	fmt.Fprintf(&file, "SecRuleEngine %s\n", engine)
	file.WriteString("SecRequestBodyAccess On\n")
	file.WriteString("SecRequestBodyLimit 33554432\n")
	file.WriteString("SecRequestBodyNoFilesLimit 1048576\n")
	file.WriteString("SecRequestBodyLimitAction ProcessPartial\n")
	file.WriteString("SecResponseBodyAccess Off\n")
	file.WriteString("SecArgumentSeparator &\n")
	file.WriteString("SecCookieFormat 0\n")
	file.WriteString("SecTmpDir /tmp/\n")
	file.WriteString("SecDataDir /tmp/\n")
	file.WriteString("SecAuditEngine Off\n")

	file.WriteString("\n# Bodies parsed as what they say they are.\n")
	file.WriteString(`SecRule REQUEST_HEADERS:Content-Type "^(?:application(?:/soap\+|/)|text/)xml" ` +
		`"id:1000001,phase:1,t:none,t:lowercase,pass,nolog,ctl:requestBodyProcessor=XML"` + "\n")
	file.WriteString(`SecRule REQUEST_HEADERS:Content-Type "^application/json" ` +
		`"id:1000002,phase:1,t:none,t:lowercase,pass,nolog,ctl:requestBodyProcessor=JSON"` + "\n")

	ips := []string{"127.0.0.1", "::1"}
	for _, address := range env.IgnoreIPs {
		if ValidAddress(address) && address != "127.0.0.1" && address != "::1" {
			ips = append(ips, address)
		}
	}
	file.WriteString("\n# Never the addresses on the never-ban list.\n")
	fmt.Fprintf(&file, `SecRule REMOTE_ADDR "@ipMatch %s" "id:1000010,phase:1,pass,nolog,ctl:ruleEngine=Off"`+"\n",
		strings.Join(ips, ","))

	hosts := []string{}
	for _, host := range env.ExemptHosts {
		if validHost(host) {
			hosts = append(hosts, regexp.QuoteMeta(strings.ToLower(host)))
		}
	}
	if len(hosts) > 0 {
		sort.Strings(hosts)
		file.WriteString("\n# Nor the applications whose clients sync.\n")
		fmt.Fprintf(&file, `SecRule REQUEST_HEADERS:Host "@rx ^(?:%s)(?::[0-9]+)?$" `+
			`"id:1000011,phase:1,t:none,t:lowercase,pass,nolog,ctl:ruleEngine=Off"`+"\n", strings.Join(hosts, "|"))
	}

	file.WriteString("\n# WordPress's admin, editor and REST API, which the generic rules misread.\n")
	file.WriteString(`SecAction "id:1000020,phase:1,pass,nolog,t:none,setvar:tx.crs_exclusions_wordpress=1"` + "\n")

	file.WriteString("\n# The OWASP Core Rule Set.\n")
	fmt.Fprintf(&file, "Include %s\n", env.Setup)
	fmt.Fprintf(&file, "Include %s/*.conf\n", crsRules)

	return file.String()
}

// WAFEnvFor is the environment with the setup file found on this machine.
func WAFEnvFor(ignoreIPs, exemptHosts []string) (WAFEnv, error) {
	setup := crsSetup()
	if setup == "" {
		return WAFEnv{}, errors.New("the OWASP Core Rule Set is not installed — install the WAF first")
	}
	return WAFEnv{IgnoreIPs: ignoreIPs, ExemptHosts: exemptHosts, Setup: setup}, nil
}

// ------------------------------------------------------------------ ping

const pingPath = "/etc/sysctl.d/61-ubuntu-panel-ping.conf"

var pingSettings = [][2]string{
	{"net.ipv4.icmp_echo_ignore_all", "1"},
	{"net.ipv6.icmp.echo_ignore_all", "1"},
}

// PingValues are the current values of what the ping switch sets.
func PingValues() map[string]string {
	values := map[string]string{}
	for _, setting := range pingSettings {
		output := strings.TrimSpace(proc.Run("sysctl -n "+setting[0]+" 2>/dev/null", 10*time.Second).Output)
		if _, err := strconv.Atoi(output); err == nil {
			values[setting[0]] = output
		}
	}
	return values
}

// PingApplied is whether the file is on disk.
func PingApplied() bool { return proc.Exists(pingPath, true) }

// ApplyNoPing has the kernel stop answering ping, or puts back what it
// answered with before. ICMP's other messages — the ones path MTU discovery
// and error reporting need — are untouched.
func ApplyNoPing(on bool, previous map[string]string) error {
	if on {
		var file strings.Builder
		file.WriteString("# Managed by Ubuntu Panel — the server does not answer ping. From the Security page.\n")
		for _, setting := range pingSettings {
			fmt.Fprintf(&file, "%s = %s\n", setting[0], setting[1])
		}
		if err := proc.PutFile(pingPath, file.String(), true); err != nil {
			return err
		}
		if result := proc.Run("sudo sysctl -p "+pingPath+" 2>&1", time.Minute); !result.OK() &&
			!strings.Contains(result.Output, " = ") {
			return fmt.Errorf("the kernel refused the setting: %s", lastLines(result.Output, 3))
		}
		return nil
	}

	proc.Run("sudo rm -f "+pingPath, time.Minute)

	for _, setting := range pingSettings {
		value := previous[setting[0]]
		if _, err := strconv.Atoi(value); err != nil {
			value = "0"
		}
		proc.Run(fmt.Sprintf("sudo sysctl -w %s=%s >/dev/null 2>&1", setting[0], value), 10*time.Second)
	}

	return nil
}
