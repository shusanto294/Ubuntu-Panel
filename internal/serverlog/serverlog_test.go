package serverlog

import (
	"testing"
	"time"
)

func read(file File, lines ...string) []Event {
	r := &Reader{
		File:      file,
		Location:  time.UTC,
		Now:       time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		PanelUser: "ubuntupanel",
	}
	for _, line := range lines {
		r.Line(line)
	}
	return r.Events
}

func TestStamps(t *testing.T) {
	want := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC).Unix()

	cases := map[string]string{
		"nginx":    `2026/11/04 10:00:00 [error] 812#812: *41 open() "/var/www/x" failed (2: No such file), client: 203.0.113.9, server: a.example`,
		"fpm":      `[04-Nov-2026 10:00:00] WARNING: [pool www] server reached pm.max_children setting (5), consider raising it`,
		"wp":       `[04-Nov-2026 10:00:00 UTC] PHP Fatal error:  Uncaught Error: Call to undefined function x()`,
		"laravel":  `[2026-11-04 10:00:00] production.ERROR: SQLSTATE[HY000] Connection refused`,
		"fail2ban": `2026-11-04 10:00:00,123 fail2ban.actions        [811]: NOTICE  [sshd] Ban 203.0.113.9`,
		"mysql8":   `2026-11-04T10:00:00.123456Z 0 [Warning] [MY-010068] [Server] CA certificate ca.pem is self signed.`,
		"rfc3339":  `2026-11-04T16:00:00.123456+06:00 web1 systemd[1]: Started nginx.service.`,
		"syslog":   `Nov  4 10:00:00 web1 systemd[1]: Started nginx.service.`,
		"redis":    `812:M 04 Nov 2026 10:00:00.123 * Background saving terminated with success`,
	}

	for name, line := range cases {
		events := read(File{Source: SourceSystem}, line)
		if len(events) != 1 {
			t.Errorf("%s: %d events", name, len(events))
			continue
		}
		if events[0].At != want {
			t.Errorf("%s: at %v, want %v", name, time.Unix(events[0].At, 0).UTC(), time.Unix(want, 0).UTC())
		}
	}
}

func TestLevelsAndAddresses(t *testing.T) {
	events := read(File{Source: SourceNginx},
		`2026/11/04 10:00:00 [error] 812#812: *41 upstream timed out, client: 203.0.113.9, server: a.example`,
		`2026/11/04 10:00:01 [warn] 812#812: *42 limiting requests, excess: 10.5 by zone "panel_login", client: 198.51.100.7`,
		`2026/11/04 10:00:02 [notice] 812#812: using inherited sockets from "6;7;"`,
	)

	if len(events) != 3 {
		t.Fatalf("%d events", len(events))
	}

	if events[0].Level != LevelError || events[0].IP != "203.0.113.9" {
		t.Errorf("first: %+v", events[0])
	}
	if events[1].Level != LevelWarning || events[1].IP != "198.51.100.7" {
		t.Errorf("second: %+v", events[1])
	}
	if events[2].Level != LevelInfo || events[2].IP != "" {
		t.Errorf("third: %+v", events[2])
	}

	// An old worker leaving after a reload is not an emergency.
	reload := read(File{Source: SourceNginx},
		`2026/11/04 15:18:00 [alert] 258241#258241: *142713 open socket #3 left in connection 8`,
		`2026/11/04 15:18:00 [alert] 258241#258241: aborting`,
		`2026/11/04 15:18:01 [alert] 812#812: socket() failed (24: Too many open files)`,
	)
	if len(reload) != 3 || reload[0].Level != LevelWarning || reload[1].Level != LevelWarning {
		t.Errorf("reload chatter: %+v", reload)
	}
	if reload[2].Level != LevelCritical {
		t.Errorf("a real alert was downgraded: %+v", reload[2])
	}

	oom := read(File{Source: SourceKernel}, `Nov  4 10:00:00 web1 kernel: Out of memory: Killed process 4242 (php-fpm8.3)`)
	if len(oom) != 1 || oom[0].Level != LevelCritical {
		t.Errorf("OOM kill: %+v", oom)
	}
}

func TestContinuationLines(t *testing.T) {
	events := read(File{Source: SourceSite, Site: "a.example"},
		`[2026-11-04 10:00:00] production.ERROR: Something broke`,
		`#0 /var/www/a/vendor/x.php(12): thing()`,
		`#1 {main}`,
	)

	if len(events) != 1 || events[0].Site != "a.example" ||
		events[0].Message != "production.ERROR: Something broke\n#0 /var/www/a/vendor/x.php(12): thing()\n#1 {main}" {
		t.Fatalf("%+v", events)
	}

	// A read that starts mid-trace drops the lines it cannot place.
	if events := read(File{Source: SourceSite}, `#3 {main}`); len(events) != 0 {
		t.Fatalf("orphan continuation kept: %+v", events)
	}
}

func TestNoiseIsSkipped(t *testing.T) {
	auth := read(File{Source: SourceAuth},
		`Nov  4 10:00:00 web1 sudo: ubuntupanel : PWD=/ ; USER=root ; COMMAND=/usr/bin/tail -n 5 /var/log/x`,
		`Nov  4 10:00:00 web1 sudo: pam_unix(sudo:session): session opened for user root(uid=0) by (uid=998)`,
		`Nov  4 10:00:00 web1 sshd[99]: Failed password for root from 203.0.113.9 port 4242 ssh2`,
		`Nov  4 10:00:01 web1 sudo:    alice : TTY=pts/0 ; PWD=/home/alice ; USER=root ; COMMAND=/bin/bash`,
	)
	if len(auth) != 1 || auth[0].Message != "web1 sudo:    alice : TTY=pts/0 ; PWD=/home/alice ; USER=root ; COMMAND=/bin/bash" {
		t.Fatalf("auth kept %+v", auth)
	}

	ban := read(File{Source: SourceFail2ban},
		`2026-11-04 10:00:00,123 fail2ban.filter         [811]: INFO    [sshd] Found 203.0.113.9 - 2026-11-04 10:00:00`,
		`2026-11-04 10:00:01,123 fail2ban.actions        [811]: NOTICE  [sshd] Ban 203.0.113.9`,
	)
	if len(ban) != 1 || ban[0].Level != LevelWarning || ban[0].IP != "203.0.113.9" {
		t.Fatalf("fail2ban kept %+v", ban)
	}

	system := read(File{Source: SourceSystem},
		`Nov  4 10:00:00 web1 kernel: [UFW BLOCK] IN=eth0 SRC=203.0.113.9`,
		`Nov  4 10:00:00 web1 postfix/smtpd[1]: connect from unknown[203.0.113.9]`,
		`Nov  4 10:00:00 web1 systemd[1]: Started Daily apt download activities.`,
	)
	if len(system) != 1 {
		t.Fatalf("syslog kept %+v", system)
	}
}

func TestAccessKeepsOnlyNotableRequests(t *testing.T) {
	events := read(File{Source: SourceRequests, Site: "a.example", Access: true},
		`203.0.113.9 - - [04/Nov/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 512 "-" "Mozilla"`,
		`203.0.113.9 - - [04/Nov/2026:10:00:01 +0000] "GET /checkout HTTP/1.1" 502 157 "-" "Mozilla"`,
		`203.0.113.9 - - [04/Nov/2026:10:00:02 +0000] "POST /wp-login.php HTTP/1.1" 429 0 "-" "python"`,
		`203.0.113.9 - - [04/Nov/2026:10:00:03 +0000] "GET /.env HTTP/1.1" 404 0 "-" "zgrab"`,
	)

	if len(events) != 3 {
		t.Fatalf("%d events: %+v", len(events), events)
	}

	if events[0].Level != LevelError || events[0].Site != "a.example" || events[0].IP != "203.0.113.9" {
		t.Errorf("502: %+v", events[0])
	}
	if events[1].Level != LevelWarning || events[2].Level != LevelWarning {
		t.Errorf("429 and scan: %+v %+v", events[1], events[2])
	}
}

// What the panel caused is in its own log, not the Server logs tab; what the
// machine says about itself stays.
func TestPanelCausedLinesAreLeftOut(t *testing.T) {
	hidden := map[string]string{
		`web1 ubuntu-panel[812]: time=2026-11-05T10:00:00.000+06:00 level=INFO msg="protection changed"`:         SourceSystem,
		`web1 ubuntu-panel[812]: time=2026-11-05T10:00:00.000+06:00 level=ERROR msg="could not apply"`:           SourceSystem,
		`web1 systemd[1]: Started ubuntu-panel.service - Ubuntu Panel.`:                                          SourceSystem,
		`web1 systemd[1]: ubuntu-panel.service: Consumed 1min 2.345s CPU time.`:                                  SourceSystem,
		`web1 systemd[1]: Started ubuntu-panel-update.service - Ubuntu Panel update.`:                            SourceSystem,
		`web1 systemd[1]: Reloading nginx.service - A high performance web server and a reverse proxy server...`: SourceSystem,
		`web1 systemd[1]: Reloaded nginx.service - A high performance web server and a reverse proxy server.`:    SourceSystem,
		`web1 systemd[1]: Reloading fail2ban.service - Fail2Ban Service...`:                                      SourceSystem,
		`[notice] 4021#4021: signal process started`:                                                             SourceNginx,
	}
	for message, source := range hidden {
		if !skip(source, message, "ubuntupanel") {
			t.Errorf("%s should be left out", message)
		}
	}

	kept := map[string]string{
		`web1 systemd[1]: Reload failed for nginx.service - A high performance web server.`:             SourceSystem,
		`web1 systemd[1]: nginx.service: Main process exited, code=exited, status=1/FAILURE`:            SourceSystem,
		`web1 systemd[1]: Started php8.3-fpm.service - The PHP 8.3 FastCGI Process Manager.`:            SourceSystem,
		`[error] 4021#4021: *1 connect() failed (111: Connection refused) while connecting to upstream`: SourceNginx,
		`web1 sudo:    alice : TTY=pts/0 ; PWD=/home/alice ; USER=root ; COMMAND=/bin/bash`:             SourceAuth,
	}
	for message, source := range kept {
		if skip(source, message, "ubuntupanel") {
			t.Errorf("%s should be kept", message)
		}
	}
}

// A scanner asking for a WordPress include file directly is not a crisis.
func TestIncludeFilesRequestedDirectlyAreNotCritical(t *testing.T) {
	scanner := `[error] 555377#555377: *17865 FastCGI sent in stderr: "PHP message: PHP Fatal error:  Uncaught Error: ` +
		`Undefined constant "ABSPATH" in /var/www/example.com/wp-settings.php:34`
	if got := level(scanner); got != LevelWarning {
		t.Errorf("an out-of-context include is %s", got)
	}

	plugin := `FastCGI sent in stderr: "PHP message: PHP Fatal error:  Uncaught Error: Call to undefined function add_action() in /var/www/example.com/wp-content/plugins/x/x.php:12`
	if got := level(plugin); got != LevelWarning {
		t.Errorf("a plugin file requested directly is %s", got)
	}

	real := `FastCGI sent in stderr: "PHP message: PHP Fatal error:  Allowed memory size of 134217728 bytes exhausted`
	if got := level(real); got != LevelCritical {
		t.Errorf("a real fatal error is %s", got)
	}
}
