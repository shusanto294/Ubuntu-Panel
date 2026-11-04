package security

import (
	"regexp"
	"strings"
	"testing"
)

// failRegexes turns a filter file's failregex lines into Go expressions the
// way fail2ban would use them: <HOST> is the address, and the date has been
// cut out of the line, leaving the brackets empty.
func failRegexes(t *testing.T, filter string) []*regexp.Regexp {
	t.Helper()

	var expressions []*regexp.Regexp

	inside := false
	for _, line := range strings.Split(filter, "\n") {
		switch {
		case strings.HasPrefix(line, "failregex = "):
			inside = true
			line = strings.TrimPrefix(line, "failregex = ")
		case inside && strings.HasPrefix(line, "            "):
			line = strings.TrimSpace(line)
		default:
			inside = false
			continue
		}

		expression := strings.ReplaceAll(line, "<HOST>", `(?P<host>\S+)`)
		expression = strings.ReplaceAll(expression, "%%", "%")
		compiled, err := regexp.Compile(expression)
		if err != nil {
			t.Fatalf("failregex does not compile: %v\n%s", err, line)
		}
		expressions = append(expressions, compiled)
	}

	if len(expressions) == 0 {
		t.Fatal("no failregex in the filter")
	}

	return expressions
}

func matches(expressions []*regexp.Regexp, line string) string {
	for _, expression := range expressions {
		if m := expression.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

func TestWebLoginFilter(t *testing.T) {
	expressions := failRegexes(t, webLoginFilterFile)

	cases := map[string]string{
		`203.0.113.9 - - [] "POST /wp-login.php HTTP/1.1" 200 4521 "-" "python"`:                     "203.0.113.9",
		`203.0.113.9 - - [04/Nov/2026:10:00:00 +0000] "POST /wp-login.php HTTP/2.0" 200 45 "-" "x"`:  "203.0.113.9",
		`203.0.113.9 - - [] "POST /wp-login.php HTTP/1.1" 302 0 "-" "Mozilla"`:                       "", // a right password
		`203.0.113.9 - - [] "GET /wp-login.php HTTP/1.1" 200 4521 "-" "Mozilla"`:                     "",
		`2001:db8::9 - - [] "POST /xmlrpc.php HTTP/1.1" 403 0 "-" "x"`:                               "2001:db8::9",
		`198.51.100.4 - - [] "POST /user/login?destination=x HTTP/1.1" 302 0 "-" "x"`:                "198.51.100.4",
		`198.51.100.4 - - [] "POST /wp-admin/admin-ajax.php HTTP/1.1" 200 10 "-" "Mozilla"`:          "",
		`198.51.100.4 - - [] "POST /blog/how-to-login-faster/comment HTTP/1.1" 200 10 "-" "Mozilla"`: "",
	}

	for line, want := range cases {
		if got := matches(expressions, line); got != want {
			t.Errorf("%s\n  matched %q, want %q", line, got, want)
		}
	}
}

func TestWebScanFilter(t *testing.T) {
	expressions := failRegexes(t, webScanFilterFile)

	cases := map[string]string{
		`203.0.113.9 - - [] "GET /.env HTTP/1.1" 404 162 "-" "zgrab"`:                                         "203.0.113.9",
		`203.0.113.9 - - [] "GET /vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php HTTP/1.1" 404 1 "-" "x"`: "203.0.113.9",
		`203.0.113.9 - - [] "GET /.git/config HTTP/1.1" 403 1 "-" "x"`:                                        "203.0.113.9",
		`203.0.113.9 - - [] "GET /static/../../etc/passwd HTTP/1.1" 400 1 "-" "x"`:                            "203.0.113.9",
		`203.0.113.9 - - [] "GET /.well-known/acme-challenge/abc HTTP/1.1" 200 1 "-" "x"`:                     "",
		`203.0.113.9 - - [] "GET /blog/environment-tips/ HTTP/1.1" 404 1 "-" "x"`:                             "",
		`203.0.113.9 - - [] "GET /.env HTTP/1.1" 200 1 "-" "x"`:                                               "", // a file it has
		`203.0.113.9 - - [] "GET /%2e%2e/etc/passwd HTTP/1.1" 400 1 "-" "x"`:                                  "203.0.113.9",
	}

	for line, want := range cases {
		if got := matches(expressions, line); got != want {
			t.Errorf("%s\n  matched %q, want %q", line, got, want)
		}
	}
}

func TestProtectJailFile(t *testing.T) {
	if ProtectJailFile(ProtectionDefaults, JailEnv{}) != "" {
		t.Fatal("nothing is on, so there should be no file")
	}

	p := ProtectionDefaults
	p.WebLogin.Enabled = true
	p.Mail.Enabled = true
	p.Recidive.Enabled = true

	file := ProtectJailFile(p, JailEnv{IgnoreIPs: []string{"203.0.113.10"}, MailLog: true, Fail2ban: true})

	for _, want := range []string{
		"[panel-web-login]", "filter   = ubuntu-panel-web-login", "port     = http,https",
		"logpath  = /var/log/nginx/*-access.log",
		"[panel-mail-smtp]", "filter   = postfix[mode=auth]", "[panel-mail-imap]", "filter   = dovecot",
		"logpath  = /var/log/mail.log",
		"[recidive]", "banaction = iptables-allports", "maxretry = 5", "findtime = 600", "bantime  = -1",
		"203.0.113.10", "173.245.48.0/20",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("jail file is missing %q:\n%s", want, file)
		}
	}

	// nginx's own access.log is the panel's: its sign-ins and file manager
	// must never count as an attack.
	if strings.Contains(file, "/var/log/nginx/*access.log") || strings.Contains(file, "nginx/access.log") {
		t.Error("the website jails read the log the panel's own pages are written to")
	}

	if strings.Contains(file, "[panel-web-scan]") {
		t.Error("the scanner jail is off and should not be in the file")
	}

	if got := p.ProtectJails(); len(got) != 4 {
		t.Errorf("jails = %v", got)
	}
}

func TestNginxFile(t *testing.T) {
	if NginxFile(ProtectionDefaults, NginxEnv{}) != "" {
		t.Fatal("nothing is on, so there should be no file")
	}

	p := ProtectionDefaults
	p.RateLogin.Enabled = true
	p.RateFlood.Enabled = true

	file := NginxFile(p, NginxEnv{IgnoreIPs: []string{"203.0.113.10", "bad; value"}, ExemptHosts: []string{"cloud.example.com", "x;y"}})

	for _, want := range []string{
		"geo $panel_limit_exempt", "203.0.113.10 1;", "173.245.48.0/20 1;",
		"zone=panel_login:10m rate=10r/m", "limit_req zone=panel_login burst=10 nodelay;",
		"zone=panel_flood:20m rate=20r/s", "cloud.example.com 1;", "limit_req_status 429;",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("nginx file is missing %q:\n%s", want, file)
		}
	}

	for _, unwanted := range []string{"bad; value", "x;y", "real_ip_header"} {
		if strings.Contains(file, unwanted) {
			t.Errorf("nginx file should not contain %q", unwanted)
		}
	}

	// With the real address restored, Cloudflare is trusted for the header and
	// no longer exempt from the limits.
	p.RealIP = true
	file = NginxFile(p, NginxEnv{})

	if !strings.Contains(file, "real_ip_header CF-Connecting-IP;") || !strings.Contains(file, "set_real_ip_from 173.245.48.0/20;") {
		t.Error("real IP directives are missing")
	}
	if strings.Contains(file, "173.245.48.0/20 1;") {
		t.Error("Cloudflare should not be exempt once the real address is restored")
	}
}

func TestValidation(t *testing.T) {
	if len(ProtectionDefaults.WebLogin.Validate()) != 0 || len(ProtectionDefaults.RateFlood.Validate(true)) != 0 {
		t.Fatal("the defaults should be valid")
	}

	if len((JailRule{MaxRetry: 0, FindTime: 10, BanTime: 5}).Validate()) != 3 {
		t.Fatal("every field of a bad jail should be refused")
	}
}
