package security

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// failregex turns a filter's first failregex into a Go expression, with
// <HOST> as a capture, so a sample log line can be tried against it.
func failregex(t *testing.T, filter string) *regexp.Regexp {
	t.Helper()
	for _, line := range strings.Split(filter, "\n") {
		if rest, ok := strings.CutPrefix(line, "failregex = "); ok {
			rest = strings.ReplaceAll(rest, "%%", "%")
			rest = strings.ReplaceAll(rest, "<HOST>", `(?P<host>\S+)`)
			return regexp.MustCompile(rest)
		}
	}
	t.Fatalf("no failregex in:\n%s", filter)
	return nil
}

func TestNewFiltersMatchTheirLines(t *testing.T) {
	cases := []struct {
		name, filter, line, host string
	}{
		{"flood", webFloodFilterFile(),
			`203.0.113.9 - - [] "GET /?p=1 HTTP/1.1" 429 162 "-" "Mozilla/5.0"`, "203.0.113.9"},
		{"tools", webToolsFilterFile(),
			`203.0.113.9 - - [] "GET /index.php?id=1 HTTP/1.1" 444 0 "-" "sqlmap/1.7.2#stable (https://sqlmap.org)"`, "203.0.113.9"},
		{"waf", webWAFFilterFile,
			`[error] 811#811: *5 [client 198.51.100.4] ModSecurity: Access denied with code 403 (phase 2).`, "198.51.100.4"},
		{"port scan", portScanFilterFile,
			`kernel: [UFW BLOCK] IN=eth0 OUT= MAC=00:00 SRC=192.0.2.7 DST=10.0.0.5 LEN=40 PROTO=TCP SPT=4444 DPT=23`, "192.0.2.7"},
	}

	for _, c := range cases {
		match := failregex(t, c.filter).FindStringSubmatch(c.line)
		if match == nil || match[1] != c.host {
			t.Errorf("%s: %q did not match for %s (got %v)", c.name, c.line, c.host, match)
		}
	}

	// The server's own refused outbound traffic is not a scan.
	if failregex(t, portScanFilterFile).MatchString(`[UFW BLOCK] IN= OUT=eth0 SRC=10.0.0.5 DST=192.0.2.7 PROTO=TCP`) {
		t.Error("outbound refusals count as a port scan")
	}

	// Detection-only warnings are not refusals.
	if failregex(t, webWAFFilterFile).MatchString(`[client 198.51.100.4] ModSecurity: Warning. Matched "Operator`) {
		t.Error("a detection-only warning counts as a refusal")
	}

	// curl is how monitors and webhooks talk to a site.
	if failregex(t, webToolsFilterFile()).MatchString(`203.0.113.9 - - [] "GET / HTTP/1.1" 200 5 "-" "curl/8.5.0"`) {
		t.Error("curl counts as an attack tool")
	}
}

func TestProtectJailFileRendersTheNewJails(t *testing.T) {
	p := ProtectionDefaults
	p.WebFlood.Enabled, p.Tools.Enabled, p.PortScan.Enabled = true, true, true
	p.WAF.Enabled, p.WAF.Block = true, true

	file := ProtectJailFile(p, JailEnv{IgnoreIPs: []string{"198.51.100.1"}, DefaultLog: true, IPSetAction: "iptables-ipset-proto6-allports"})

	for _, want := range []string{
		"[panel-web-flood]", "filter   = ubuntu-panel-web-flood", "maxretry = 30", "findtime = 60", "bantime  = 3600",
		"[panel-web-tools]", "[panel-web-waf]", "logpath  = " + SiteErrorLogs,
		"[panel-port-scan]", "banaction = iptables-ipset-proto6-allports", "journalmatch = _TRANSPORT=kernel",
		"fe80::/10", "198.51.100.1",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the jail file is missing %q:\n%s", want, file)
		}
	}

	if got := len(p.ProtectJails()); got != 4 {
		t.Errorf("four new jails should run, got %d: %v", got, p.ProtectJails())
	}

	// Detection only never bans.
	p.WAF.Block = false
	if strings.Contains(ProtectJailFile(p, JailEnv{}), "[panel-web-waf]") {
		t.Error("the WAF bans while it only detects")
	}
}

func TestNginxFileConnectionLimitAndGuardVariables(t *testing.T) {
	p := Protection{}
	p.ConnLimit = ConnLimit{Enabled: true, Max: 40}
	p.Tools.Enabled = true
	p.Countries = CountryRule{Enabled: true, Mode: CountriesAllow, Codes: []string{"us"}}
	p.Challenge = ChallengeRule{Enabled: true, Sites: []string{"example.com"}}

	file := NginxFile(p, NginxEnv{ExemptHosts: []string{"panel.example.com"}})

	for _, want := range []string{
		"limit_conn_zone $panel_limit_conn zone=panel_conn:10m;", "limit_conn panel_conn 40;",
		`map "$panel_limit_exempt$panel_limit_host_exempt$panel_limit_bot $uri" $panel_limit_conn`,
		"client_header_timeout 10s;", "limit_conn_status 429;",
		"map $host $panel_limit_host_exempt", "panel.example.com 1;",
		"$panel_attack_tool", "sqlmap",
		"include " + CountriesPath + ";", "$panel_country_refused", "    000 1;",
		"map $http_user_agent $panel_limit_bot",
		"$panel_challenge", "include " + ChallengeHostsPath + ";",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the nginx file is missing %q:\n%s", want, file)
		}
	}

	// Without real addresses Cloudflare is exempt from everything, and the
	// rate limits' shared lines are not written for a connection limit alone.
	if !strings.Contains(file, "173.245.48.0/20 1;") || strings.Contains(file, limitStatusLine) {
		t.Errorf("exemptions or shared lines are wrong:\n%s", file)
	}

	// The block mode refuses listed countries.
	p.Countries.Mode = CountriesBlock
	if !strings.Contains(NginxFile(p, NginxEnv{}), "    001 1;") {
		t.Error("block mode does not refuse the listed countries")
	}

	// The WAF alone needs nothing in the http block but the header.
	if file := NginxFile(Protection{WAF: WAFRule{Enabled: true}}, NginxEnv{}); strings.Contains(file, "geo ") {
		t.Errorf("the WAF alone writes maps it does not use:\n%s", file)
	}
}

func TestGuardFile(t *testing.T) {
	if GuardFile(Protection{}, NginxEnv{}) != "" {
		t.Error("a guard file with nothing on")
	}

	p := Protection{}
	p.Tools.Enabled = true
	p.Countries.Enabled = true
	p.Challenge = ChallengeRule{Enabled: true, Sites: []string{"example.com"}}
	p.WAF.Enabled = true

	file := GuardFile(p, NginxEnv{BindPort: 8088, ChallengeSecret: "s3cret"})

	for _, want := range []string{
		"if ($panel_attack_tool) {\n    return 444;", "if ($panel_country_refused) {\n    return 403;",
		"location = " + ChallengePath + " {", "proxy_pass http://127.0.0.1:8088" + PanelChallengePath + ";",
		"secure_link $cookie_panel_hv,$cookie_panel_he;",
		`secure_link_md5 "$secure_link_expires$remote_addr$host s3cret";`,
		"error_page 418 = @panel_challenge;", "return 503 \"<!doctype html>",
		"modsecurity on;", "modsecurity_rules_file " + WAFRulesPath + ";",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the guard file is missing %q:\n%s", want, file)
		}
	}
}

func TestChallengePageHasNoVariables(t *testing.T) {
	// A $ in nginx's return text is a variable, and cannot be escaped.
	if strings.Contains(challengePage, "$") {
		t.Error("the challenge page contains a $")
	}
	if strings.Contains(challengePage, `"`) {
		t.Error("the challenge page contains a double quote")
	}
	if !strings.Contains(challengePage, ChallengePath) {
		t.Error("the challenge page does not call the endpoint")
	}
}

func TestChallengeSeedsAndTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	seed := ChallengeSeed("secret", "203.0.113.9", "Example.com", now)

	if !ValidChallengeSeed("secret", "203.0.113.9", "example.com", seed, now.Add(2*time.Second)) {
		t.Error("a seed handed back after the wait is refused")
	}
	if ValidChallengeSeed("secret", "203.0.113.9", "example.com", seed, now.Add(100*time.Millisecond)) {
		t.Error("a seed handed back at once is accepted")
	}
	if ValidChallengeSeed("secret", "203.0.113.9", "example.com", seed, now.Add(10*time.Minute)) {
		t.Error("a stale seed is accepted")
	}
	if ValidChallengeSeed("secret", "198.51.100.1", "example.com", seed, now.Add(2*time.Second)) {
		t.Error("a seed works for another address")
	}
	if ValidChallengeSeed("other", "203.0.113.9", "example.com", seed, now.Add(2*time.Second)) {
		t.Error("a seed signed with another secret is accepted")
	}

	// What nginx computes for secure_link_md5 "$secure_link_expires$remote_addr$host secret":
	// md5("1800043200203.0.113.9example.com secret"), base64url without padding.
	token := ChallengeToken("secret", "203.0.113.9", "Example.com", 1_800_043_200)
	if len(token) != 22 || strings.ContainsAny(token, "+/=") {
		t.Errorf("the token %q is not base64url MD5", token)
	}
	if token != ChallengeToken("secret", "203.0.113.9", "example.com", 1_800_043_200) ||
		token == ChallengeToken("secret", "203.0.113.10", "example.com", 1_800_043_200) {
		t.Error("the token is not bound to the address alone")
	}
}

func TestCleanCountries(t *testing.T) {
	codes, problem := CleanCountries("CN, ru\nkp cn")
	if problem != "" || strings.Join(codes, ",") != "cn,kp,ru" {
		t.Errorf("got %v %q", codes, problem)
	}
	if _, problem := CleanCountries("china"); problem == "" {
		t.Error("a country name was taken as a code")
	}
}

func TestCountriesFile(t *testing.T) {
	file := CountriesFile(map[string][]string{"ru": {"5.8.0.0/19"}, "cn": {"1.0.1.0/24", "2001:250::/35"}})
	if !strings.Contains(file, countriesHeader+"cn ru\n") || !strings.Contains(file, "    2001:250::/35 1;") ||
		!strings.HasPrefix(strings.Split(file, "\n")[2], "geo $panel_country_listed {") {
		t.Errorf("unexpected countries file:\n%s", file)
	}
}

func TestWAFFile(t *testing.T) {
	file := WAFFile(WAFRule{Enabled: true}, WAFEnv{IgnoreIPs: []string{"198.51.100.1", "bad value"},
		ExemptHosts: []string{"cloud.example.com"}, Setup: "/etc/modsecurity/crs/crs-setup.conf"})

	for _, want := range []string{
		"SecRuleEngine DetectionOnly", `"@ipMatch 127.0.0.1,::1,198.51.100.1"`, `cloud\.example\.com`,
		"tx.crs_exclusions_wordpress=1", "Include /etc/modsecurity/crs/crs-setup.conf",
		"Include " + crsRules + "/*.conf",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the WAF file is missing %q:\n%s", want, file)
		}
	}
	if strings.Contains(file, "bad value") {
		t.Error("an invalid address reached ModSecurity's configuration")
	}

	if !strings.Contains(WAFFile(WAFRule{Enabled: true, Block: true}, WAFEnv{Setup: "x"}), "SecRuleEngine On\n") {
		t.Error("blocking mode does not switch the engine on")
	}
}

func TestSuspiciousProcesses(t *testing.T) {
	listing := strings.Join([]string{
		"1\troot\tsystemd\t/usr/lib/systemd/systemd",
		"811\twww-data\tphp-fpm8.3\t/usr/sbin/php-fpm8.3 (deleted)",
		"4242\twww-data\tkdevtmpfsi\t/tmp/kdevtmpfsi",
		"4300\tsite1\t.x\t/dev/shm/.x/run (deleted)",
		"4400\tsite2\tupd\t/home/site2/.cache/upd (deleted)",
	}, "\n")

	found := ParseProcesses(listing)
	if len(found) != 3 {
		t.Fatalf("expected the miner, the /dev/shm program and the hidden deleted one, got %+v", found)
	}
	if found[0].PID != 4242 || !strings.Contains(found[0].Reason, "miner") {
		t.Errorf("the miner is not first: %+v", found[0])
	}
	if !strings.Contains(found[1].Reason, "/dev/shm") || !strings.Contains(found[1].Reason, "deleted") {
		t.Errorf("unexpected reason: %+v", found[1])
	}

	pools := ParsePoolConnections("0 0 10.0.0.5:51234 203.0.113.9:3333 users:((\"xmrig\",pid=4242,fd=12))\n" +
		"0 0 10.0.0.5:51235 203.0.113.10:443 users:((\"curl\",pid=99,fd=3))")
	if len(pools) != 1 || pools[0].PID != 4242 || pools[0].Name != "xmrig" {
		t.Errorf("unexpected pool connections: %+v", pools)
	}
}

func TestExposedServices(t *testing.T) {
	listening := "LISTEN 0 511 0.0.0.0:6379 0.0.0.0:*\n" +
		"LISTEN 0 80 127.0.0.1:3306 0.0.0.0:*\n" +
		"LISTEN 0 4096 [::]:27017 [::]:*\n" +
		"LISTEN 0 511 0.0.0.0:443 0.0.0.0:*"

	if got := exposed(listening, Firewall{}); strings.Join(got, ",") != "MongoDB (port 27017),Redis (port 6379)" {
		t.Errorf("with the firewall off, got %v", got)
	}

	on := Firewall{Active: true, Rules: []Rule{{To: "6379/tcp", Action: "ALLOW IN", From: "Anywhere"}}}
	if got := exposed(listening, on); strings.Join(got, ",") != "Redis (port 6379)" {
		t.Errorf("with the firewall on and Redis allowed, got %v", got)
	}
}

// Every limit and refusal a visitor can meet lets useful bots through.
func TestUsefulBotsAreExemptFromEveryLimit(t *testing.T) {
	p := Protection{}
	p.RateFlood.Enabled = true
	p.ConnLimit = ConnLimit{Enabled: true, Max: 50}
	p.Countries = CountryRule{Enabled: true, Mode: CountriesBlock, Codes: []string{"us"}}
	p.Challenge = ChallengeRule{Enabled: true, Sites: []string{"example.com"}}

	file := NginxFile(p, NginxEnv{})
	for _, variable := range []string{"$panel_limit_flood", "$panel_limit_conn", "$panel_country_refused", "$panel_challenge"} {
		for _, line := range strings.Split(file, "\n") {
			if strings.HasPrefix(line, "map ") && strings.HasSuffix(line, variable+" {") && !strings.Contains(line, "$panel_limit_bot") {
				t.Errorf("%s does not let useful bots through: %s", variable, line)
			}
		}
	}

	for _, bot := range []string{"googlebot", "bingbot", "gptbot", "claudebot", "perplexitybot", "applebot", "uptimerobot"} {
		if !strings.Contains(strings.ToLower(goodBotAgents()), bot) {
			t.Errorf("%s is not a useful bot", bot)
		}
	}
}
