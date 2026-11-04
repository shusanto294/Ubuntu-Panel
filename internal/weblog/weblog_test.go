package weblog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseCombined(t *testing.T) {
	line := `203.0.113.7 - - [04/Nov/2026:10:15:02 +0000] "POST /wp-login.php?redirect_to=x HTTP/1.1" 200 4521 "https://example.com/" "Mozilla/5.0 (X11; Linux x86_64)"`

	entry, ok := Parse(line)
	if !ok {
		t.Fatal("did not parse a combined line")
	}

	if entry.IP != "203.0.113.7" || entry.Method != "POST" || entry.Path != "/wp-login.php" ||
		entry.Status != 200 || entry.Bytes != 4521 || entry.UserAgent != "Mozilla/5.0 (X11; Linux x86_64)" {
		t.Fatalf("parsed wrong: %+v", entry)
	}

	if want := time.Date(2026, 11, 4, 10, 15, 2, 0, time.UTC); !entry.Time.Equal(want) {
		t.Fatalf("time = %v, want %v", entry.Time, want)
	}
}

func TestParseOddLines(t *testing.T) {
	cases := map[string]bool{
		`2001:db8::1 - - [04/Nov/2026:10:15:02 +0600] "GET / HTTP/2.0" 304 0 "-" "-"`:  true,
		`198.51.100.4 - - [04/Nov/2026:10:15:02 +0000] "\x16\x03\x01" 400 157 "-" "-"`: true,
		`198.51.100.4 - - [04/Nov/2026:10:15:02 +0000] "-" 400 0 "-" "-"`:              true,
		`198.51.100.4 - - [04/Nov/2026:10:15:02 +0000] "GET / HTTP/1.1" 200 12`:        true,
		`not an ip - - [04/Nov/2026:10:15:02 +0000] "GET / HTTP/1.1" 200 12 "-" "-"`:   false,
		`198.51.100.4 - - [garbage] "GET / HTTP/1.1" 200 12 "-" "-"`:                   false,
		``: false,
	}

	for line, want := range cases {
		if _, ok := Parse(line); ok != want {
			t.Errorf("Parse(%q) ok = %v, want %v", line, ok, want)
		}
	}
}

func TestThreat(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{"POST", "/wp-login.php", ThreatWPLogin},
		{"GET", "/wp-login.php", ""},
		{"POST", "/xmlrpc.php", ThreatXMLRPC},
		{"GET", "/.env", ThreatSecret},
		{"GET", "/%2eenv", ThreatSecret},
		{"GET", "/%2f%2eaws%2fcredentials", ThreatSecret},
		{"GET", "/website/.env.bak", ThreatSecret},
		{"GET", "/.git/config", ThreatSecret},
		{"GET", "/.github/workflows/ci.yml", ThreatProbe}, // a probe, not a secret
		{"GET", "/environment-tips/", ""},
		{"GET", "/phpmyadmin/index.php", ""}, // answered: the panel's own phpMyAdmin
		{"GET", "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", ThreatProbe},
		{"GET", "/static/../../etc/passwd", ThreatProbe},
		{"POST", "/user/login", ThreatLogin},
		{"GET", "/login", ""},
		{"GET", "/blog/hello-world/", ""},
		{"POST", "/wp-admin/admin-ajax.php", ""},
	}

	for _, c := range cases {
		status := 404
		if strings.HasPrefix(c.path, "/phpmyadmin") {
			status = 200
		}
		if got := Threat(Entry{Method: c.method, Path: c.path, Status: status}); got != c.want {
			t.Errorf("Threat(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// A secret file counts whatever the answer was; a probe counts when it was
// refused, missing or redirected — a scanner on http:// never follows the
// redirect to https://.
func TestThreatAnswers(t *testing.T) {
	if Threat(Entry{Method: "GET", Path: "/.env", Status: 200}) != ThreatSecret {
		t.Error("a secret file served is still an attacker asking for it")
	}
	if Threat(Entry{Method: "GET", Path: "/vendor/phpunit/x", Status: 301}) != ThreatProbe {
		t.Error("a redirected probe should count")
	}
	if Threat(Entry{Method: "GET", Path: "/vendor/phpunit/x", Status: 200}) != "" {
		t.Error("a probe path the site has is not a probe")
	}
}

func TestSecretVerdict(t *testing.T) {
	ip := IPStat{Requests: 3, Threats: map[string]int{ThreatSecret: 1}}
	verdictText, danger := verdict(ip, 60)
	if !danger || !strings.Contains(verdictText, "secret files") || attackKind(ip) != "scan" {
		t.Errorf("one request for a secret file is an attacker: %q %v %q", verdictText, danger, attackKind(ip))
	}

	// Not even a useful bot asks for .env.
	ip.GoodBot = &GoodBot{}
	if _, danger := verdict(ip, 60); !danger {
		t.Error("a useful bot's name does not excuse asking for secrets")
	}
}

func TestStaticAndBot(t *testing.T) {
	if !Static("/wp-content/themes/x/style.CSS") || Static("/index.php") || Static("/") {
		t.Error("Static classified wrong")
	}

	if !Bot("Googlebot/2.1") || !Bot("") || !Bot("python-requests/2.31") || Bot("Mozilla/5.0 (Macintosh) Safari/605") {
		t.Error("Bot classified wrong")
	}
}

func line(ip string, at time.Time, method, path string, status int, agent string) string {
	return fmt.Sprintf(`%s - - [%s] "%s %s HTTP/1.1" %d 100 "-" "%s"`,
		ip, at.Format(timeLayout), method, path, status, agent)
}

func TestReportFindsTheBruteForce(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a := New(now.Add(-15*time.Minute), now, 15)

	browser := "Mozilla/5.0 (Windows NT 10.0) Chrome/130"

	// The attacker: 200 login posts in five minutes, all on one site.
	for i := 0; i < 200; i++ {
		a.Line("shop.example", line("203.0.113.9", now.Add(-5*time.Minute+time.Duration(i)*time.Second), "POST", "/wp-login.php", 200, "python-requests/2.31"))
	}

	// Ordinary visitors, on two sites, with their static files.
	for i := 0; i < 30; i++ {
		at := now.Add(-time.Duration(i) * 20 * time.Second)
		a.Line("blog.example", line(fmt.Sprintf("198.51.100.%d", i), at, "GET", "/post/", 200, browser))
		a.Line("blog.example", line(fmt.Sprintf("198.51.100.%d", i), at, "GET", "/style.css", 200, browser))
		a.Line("shop.example", line(fmt.Sprintf("192.0.2.%d", i), at, "GET", "/product/", 200, browser))
	}

	// Outside the window, and a line that is not a request.
	a.Line("blog.example", line("198.51.100.1", now.Add(-time.Hour), "GET", "/", 200, browser))
	a.Line("blog.example", "garbage")

	report := a.Report()

	if report.Requests != 290 {
		t.Fatalf("requests = %d, want 290", report.Requests)
	}
	if report.Static != 30 || report.Dynamic != 260 {
		t.Fatalf("static/dynamic = %d/%d, want 30/260", report.Static, report.Dynamic)
	}
	if report.Skipped != 1 || report.Scanned != 292 {
		t.Fatalf("scanned/skipped = %d/%d", report.Scanned, report.Skipped)
	}

	if report.Sites[0].Domain != "shop.example" || report.Sites[0].Dynamic != 230 {
		t.Fatalf("busiest site = %+v", report.Sites[0])
	}

	first := report.TopIPs[0]
	if first.IP != "203.0.113.9" || !first.Danger || !strings.Contains(first.Verdict, "brute force") {
		t.Fatalf("top address = %+v", first)
	}
	for _, ip := range report.TopIPs[1:] {
		if ip.Danger {
			t.Fatalf("a visitor was flagged: %+v", ip)
		}
	}

	if len(report.Threats) != 1 || report.Threats[0].Kind != ThreatWPLogin || report.Threats[0].Requests != 200 {
		t.Fatalf("threats = %+v", report.Threats)
	}

	if report.Paths[0].Path != "/wp-login.php" || report.Paths[0].Threat != ThreatWPLogin {
		t.Fatalf("top path = %+v", report.Paths[0])
	}

	total := 0
	for _, bucket := range report.Timeline {
		total += bucket.Requests
	}
	if total != report.Requests || len(report.Timeline) != 15 {
		t.Fatalf("timeline holds %d of %d in %d buckets", total, report.Requests, len(report.Timeline))
	}
}

func TestTrustedAddressesAreNeverFlagged(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a := New(now.Add(-time.Hour), now, 30)
	a.Trusted = map[string]bool{"103.91.52.3": true}

	for i := 0; i < 50; i++ {
		at := now.Add(-time.Duration(i) * time.Second)
		a.Line("(server)", line("103.91.52.3", at, "GET", "/.env", 404, "Mozilla"))
		a.Line("(server)", line("203.0.113.9", at, "GET", "/.env", 404, "zgrab"))
	}

	report := a.Report()

	for _, ip := range report.TopIPs {
		switch ip.IP {
		case "103.91.52.3":
			if ip.Danger || ip.Verdict != "" || len(ip.Threats) != 0 {
				t.Errorf("the trusted address was flagged: %+v", ip)
			}
		case "203.0.113.9":
			if !ip.Danger {
				t.Errorf("the scanner was not flagged: %+v", ip)
			}
		}
	}

	if len(report.Threats) != 1 || report.Threats[0].IPs != 1 {
		t.Fatalf("threats = %+v", report.Threats)
	}
}

func TestGoodBotsAreNotFlaggedForCrawling(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a := New(now.Add(-time.Hour), now, 30)

	google := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	gpt := "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot"

	// Busy, and mostly following links to pages that are gone.
	for i := 0; i < 400; i++ {
		at := now.Add(-time.Duration(i%60) * time.Second)
		a.Line("blog.example", line("66.249.66.1", at, "GET", fmt.Sprintf("/old-post-%d/", i), 404, google))
		a.Line("blog.example", line("20.171.207.1", at, "GET", fmt.Sprintf("/page-%d/", i), 200, gpt))
	}

	// Calling itself Googlebot, and asking for secrets: not Google.
	for i := 0; i < 10; i++ {
		a.Line("blog.example", line("203.0.113.66", now.Add(-time.Duration(i)*time.Second), "GET", "/.env", 404, google))
	}

	for _, ip := range a.Report().TopIPs {
		switch ip.IP {
		case "66.249.66.1", "20.171.207.1":
			if ip.Danger || ip.GoodBot == nil {
				t.Errorf("a useful bot was flagged: %+v", ip)
			}
		case "203.0.113.66":
			if !ip.Danger {
				t.Errorf("a scanner using Googlebot's name was not flagged: %+v", ip)
			}
		}
	}

	if bot := FindGoodBot("Mozilla/5.0 (Macintosh) AppleWebKit (KHTML, like Gecko) Applebot-Extended/0.1"); bot == nil || bot.Kind != "ai" {
		t.Errorf("the most specific match should win: %+v", bot)
	}
	if FindGoodBot("Mozilla/5.0 (Windows NT 10.0) Chrome/130") != nil || FindGoodBot("python-requests/2.31") != nil {
		t.Error("a browser or a script is not a useful bot")
	}
}
