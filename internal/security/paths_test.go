package security

import (
	"regexp"
	"strings"
	"testing"
)

func TestDefaultPathsAreValid(t *testing.T) {
	for _, list := range [][]string{DefaultSecretPaths, DefaultScanPaths} {
		for _, pattern := range list {
			if problem := ValidatePathPattern(pattern); problem != "" {
				t.Errorf("default %q is refused: %s", pattern, problem)
			}
		}
	}
}

func TestPathPatternsThatWouldBanVisitorsAreRefused(t *testing.T) {
	for _, pattern := range []string{
		"/", "*", "/*", ".php", "/wp-admin/", "wp-", ".css", ".well-known", "/.well", "sitemap",
		"a b", `"x"`, "/x?y=1", "/%2eenv", `\x`, "/a$b", "ab",
	} {
		if ValidatePathPattern(pattern) == "" {
			t.Errorf("%q should be refused", pattern)
		}
	}

	for _, pattern := range []string{"/.env", "/my-old-admin/", "*.bak$", "/wp-content/*/debug.log", "../"} {
		if problem := ValidatePathPattern(pattern); problem != "" {
			t.Errorf("%q should be allowed: %s", pattern, problem)
		}
	}
}

func TestCleanPathPatterns(t *testing.T) {
	got, problem := CleanPathPatterns("  /.env \n\n/.ENV\n/backup.zip$\n")
	if problem != "" || strings.Join(got, ",") != "/.env,/backup.zip$" {
		t.Errorf("got %v, %q", got, problem)
	}

	if _, problem := CleanPathPatterns("/.env\n/"); problem == "" {
		t.Error("a pattern matching every page should be refused")
	}

	if !SamePatterns([]string{"/a.b", "/c.d"}, []string{"/C.D", "/a.b"}) || SamePatterns([]string{"/a.b"}, []string{"/c.d"}) {
		t.Error("SamePatterns")
	}
}

// The owner's own patterns reach the jails, with the same rules as the
// defaults: wildcards, the end of the path, encodings, case.
func TestCustomPathsInTheFilters(t *testing.T) {
	fail := failRegexes(t, webSecretFilterFile([]string{"/old-admin/", "/wp-content/*/backup.zip$"}))

	cases := map[string]string{
		`203.0.113.9 - - [] "GET /old-admin/login HTTP/1.1" 404 1 "-" "x"`:                     "203.0.113.9",
		`203.0.113.9 - - [] "GET /OLD-ADMIN/ HTTP/1.1" 200 1 "-" "x"`:                          "203.0.113.9",
		`203.0.113.9 - - [] "GET /wp-content/uploads/backup.zip HTTP/1.1" 404 1 "-" "x"`:       "203.0.113.9",
		`203.0.113.9 - - [] "GET /wp-content/uploads/backup.zip?x=1 HTTP/1.1" 404 1 "-" "x"`:   "203.0.113.9",
		`203.0.113.9 - - [] "GET /wp-content/uploads/backup.zip.txt HTTP/1.1" 404 1 "-" "x"`:   "",
		`203.0.113.9 - - [] "GET /.env HTTP/1.1" 404 1 "-" "x"`:                                "", // not on this list
		`198.51.100.4 - - [] "GET /sites/1/files?path=/old-admin/ HTTP/1.1" 200 1 "-" "x"`:     "",
		`203.0.113.9 - - [] "GET %2fold-admin%2fx HTTP/1.1" 404 1 "-" "x"`:                     "203.0.113.9",
		`203.0.113.9 - - [04/Nov/2026:10:00:00 +0000] "GET /old-admin HTTP/1.1" 404 1 "-" "x"`: "",
	}

	for line, want := range cases {
		if got := matches(fail, line); got != want {
			t.Errorf("%s\n  matched %q, want %q", line, got, want)
		}
	}

	// An emptied list is a filter that still loads and bans nobody.
	empty := failRegexes(t, webSecretFilterFile(nil))
	if matches(empty, `203.0.113.9 - - [] "GET /.env HTTP/1.1" 404 1 "-" "x"`) != "" {
		t.Error("an empty list should match nothing")
	}
	if got := failRegexes(t, webScanFilterFile([]string{})); len(got) != 1 {
		t.Errorf("an empty scanner list leaves only the missing .xml/.txt rule, got %d", len(got))
	}
}

func TestPathPatternRegexpMatchesDecodedPaths(t *testing.T) {
	re := regexp.MustCompile(PathPatternRegexp("/WP-content/*/debug.log$"))
	if !re.MatchString("/wp-content/a/b/debug.log") || re.MatchString("/wp-content/a/debug.log.txt") {
		t.Error("PathPatternRegexp")
	}
}

func TestProtectionUsesTheDefaultsUntilEdited(t *testing.T) {
	var p Protection
	if len(p.Secrets()) != len(DefaultSecretPaths) || len(p.Scans()) != len(DefaultScanPaths) {
		t.Error("nil lists are the defaults")
	}

	p.ScanPaths = []string{}
	if len(p.Scans()) != 0 {
		t.Error("an emptied list stays empty")
	}
}

func TestWebJailsAskBeforeBanningACrawler(t *testing.T) {
	p := ProtectionDefaults
	p.WebLogin.Enabled, p.WebScan.Enabled, p.Recidive.Enabled = true, true, true

	file := ProtectJailFile(p, JailEnv{BotCheck: true})
	for _, jail := range []string{JailWebLogin, JailWebScan, JailWebSecret} {
		_, section, _ := strings.Cut(file, "["+jail+"]")
		section, _, _ = strings.Cut(section, "\n[")
		if !strings.Contains(section, "ignorecommand = "+BotCheckBinary+" --verified-bot <ip>") {
			t.Errorf("%s should ask before banning a crawler:\n%s", jail, section)
		}
	}

	_, recidive, _ := strings.Cut(file, "[recidive]")
	if strings.Contains(recidive, "ignorecommand") {
		t.Error("only the website jails ask")
	}

	if strings.Contains(ProtectJailFile(p, JailEnv{}), "ignorecommand") {
		t.Error("no binary, no ignorecommand")
	}
}

func TestFloodLimitLetsUsefulBotsThrough(t *testing.T) {
	p := ProtectionDefaults
	p.RateFlood.Enabled = true

	file := NginxFile(p, NginxEnv{})
	if !strings.Contains(file, "map $http_user_agent $panel_limit_bot {") ||
		!strings.Contains(file, `$panel_limit_exempt$panel_limit_host_exempt$panel_limit_bot $uri`) {
		t.Fatalf("the flood limit should exempt useful bots:\n%s", file)
	}

	// The bots' expression, as nginx (PCRE, case-insensitive) would run it.
	m := regexp.MustCompile(`"~\*(\(\?:[^"]+\))" 1;`).FindStringSubmatch(file)
	if m == nil {
		t.Fatalf("no bot expression in:\n%s", file)
	}
	bots := regexp.MustCompile("(?i)" + m[1])
	for _, agent := range []string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
		"Mozilla/5.0 (compatible; ClaudeBot/1.0; +claudebot@anthropic.com)",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"PerplexityBot/1.0", "facebookexternalhit/1.1", "Mozilla/5.0 (compatible; UptimeRobot/2.0)",
	} {
		if !bots.MatchString(agent) {
			t.Errorf("%q should not be flood limited", agent)
		}
	}
	if bots.MatchString("Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0") || bots.MatchString("python-requests/2.31") {
		t.Error("browsers and scripts are flood limited")
	}

	// Each of the three exemptions lifts the limit; none leaves it.
	key := regexp.MustCompile(`^(?:1..|.1.|..1) `)
	for value, exempt := range map[string]bool{"000 /": false, "100 /": true, "010 /": true, "001 /": true} {
		if key.MatchString(value) != exempt {
			t.Errorf("%q exempt = %v", value, !exempt)
		}
	}
}

func TestCloudflareIsNeverDeniedByNginx(t *testing.T) {
	file := NginxBansFile([]string{"203.0.113.9", "162.158.1.2", "2606:4700::1"})
	if !strings.Contains(file, "deny 203.0.113.9;") || strings.Contains(file, "162.158.1.2") ||
		strings.Contains(file, "2606:4700::1") {
		t.Errorf("Cloudflare's addresses must never be denied:\n%s", file)
	}
}
