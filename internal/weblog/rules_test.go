package weblog

import (
	"testing"
	"time"
)

// What an ordinary visitor or a crawler does is never a threat.
func TestOrdinaryRequestsAreNotProbes(t *testing.T) {
	for _, entry := range []Entry{
		{Method: "GET", Path: "/shellfish-recipes", Status: 404},
		{Method: "GET", Path: "/alfa-romeo-review", Status: 301},
		{Method: "GET", Path: "/system/requirements", Status: 301},
		{Method: "GET", Path: "/admin.php", Status: 302},
		{Method: "GET", Path: "/install.php", Status: 302},
		{Method: "GET", Path: "/autodiscover/autodiscover.xml", Status: 404, UserAgent: "Microsoft Office/16.0 (Windows NT 10.0; Microsoft Outlook 16.0)"},
		{Method: "GET", Path: "/config.json", Status: 304},
		{Method: "GET", Path: "/cgi-bin/app", Status: 304},
		{Method: "GET", Path: "/static/main.env.js", Status: 200},
		{Method: "GET", Path: "/blog/my-sql-tips/", Status: 301},
	} {
		if got := Threat(entry); got != "" {
			t.Errorf("Threat(%s %s %d) = %q, want none", entry.Method, entry.Path, entry.Status, got)
		}
	}

	if got := Threat(Entry{Method: "GET", Path: "/phpmyadmin/index.php", Status: 404}); got != ThreatProbe {
		t.Errorf("a missing phpMyAdmin is still a probe, got %q", got)
	}
	if got := Threat(Entry{Method: "GET", Path: "/backups/site.sql", Status: 404}); got != ThreatProbe {
		t.Errorf(".sql at the end is a probe, got %q", got)
	}
}

// A crawler following links to odd paths is not banned for it; one that only
// says it is a crawler is judged again without the claim.
func TestUsefulBotsFollowingOddLinks(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a := New(now.Add(-time.Hour), now, 12)
	google := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

	for i, path := range []string{"/vendor/phpunit/x", "/cgi-bin/a", "/solr/admin", "/phpinfo.php", "/.svn/entries", "/HNAP1"} {
		a.Line("blog.example", line("66.249.66.1", now.Add(-time.Duration(i)*time.Second), "GET", path, 404, google))
	}

	for _, ip := range a.Report().TopIPs {
		if ip.IP != "66.249.66.1" {
			continue
		}
		if ip.Danger {
			t.Errorf("a crawler following dead links was flagged: %s", ip.Verdict)
		}
		if judged := Reassess(ip, 60); !judged.Danger || judged.Attack != "scan" {
			t.Errorf("without its claim it is a scanner: %+v", judged)
		}
	}
}

func TestOwnerPathsReplaceTheDefaults(t *testing.T) {
	defer SetPaths(patternRegexps(DefaultSecretPaths), patternRegexps(DefaultScanPaths))

	SetPaths(patternRegexps([]string{"/private-area/"}), patternRegexps([]string{"/old-api/"}))

	if !IsSecret("/Private-Area/x") || IsSecret("/.env") {
		t.Error("the owner's secret list is the one in force")
	}
	if got := Threat(Entry{Method: "GET", Path: "/old-api/v1", Status: 404}); got != ThreatProbe {
		t.Errorf("the owner's scanner path should count, got %q", got)
	}
	if got := Threat(Entry{Method: "GET", Path: "/vendor/phpunit/x", Status: 404}); got != "" {
		t.Errorf("a default the owner removed should not count, got %q", got)
	}
}

// Through Cloudflare, volume alone is said but not banned; what no visitor
// does still is. Straight to the server, volume is banned as before.
func TestVolumeThroughCloudflareIsLeftToCloudflare(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a := New(now.Add(-time.Hour), now, 12)
	a.Proxied = map[string]bool{"shop.example": true}
	browser := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0"

	for i := 0; i < 400; i++ {
		at := now.Add(-time.Duration(i%30) * time.Second)
		a.Line("shop.example", line("198.51.100.20", at, "GET", "/product/", 200, browser)) // a busy office
		a.Line("(bare IP)", line("203.0.113.30", at, "GET", "/index.php", 200, browser))    // a flood, direct
	}
	a.Line("shop.example", line("198.51.100.20", now, "GET", "/.env", 404, browser))

	b := New(now.Add(-time.Hour), now, 12)
	b.Proxied = map[string]bool{"shop.example": true}
	for i := 0; i < 400; i++ {
		b.Line("shop.example", line("198.51.100.21", now.Add(-time.Duration(i%30)*time.Second), "GET", "/product/", 200, browser))
	}

	got := map[string]IPStat{}
	for _, r := range []*Analyzer{a, b} {
		for _, ip := range r.Report().TopIPs {
			got[ip.IP] = ip
		}
	}

	if ip := got["198.51.100.21"]; ip.Danger || ip.Verdict == "" {
		t.Errorf("volume through Cloudflare should be said, not banned: %+v", ip)
	}
	if ip := got["203.0.113.30"]; !ip.Danger || ip.Attack != "flood" {
		t.Errorf("a direct flood is still banned: %+v", ip)
	}
	if ip := got["198.51.100.20"]; !ip.Danger || ip.Attack != "scan" {
		t.Errorf("asking for .env through Cloudflare is still banned: %+v", ip)
	}
}
