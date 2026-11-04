// Package serverlog turns the lines the machine's own programs write — nginx,
// PHP-FPM, the databases, fail2ban, sshd and sudo, the kernel, mail, and each
// site's application — into events with a time, a level and a message, so the
// Logs page can show them as one list beside what the panel did.
//
// Every program writes the time its own way, and most of them in local time
// without saying which zone. Each line is tried against the shapes below in
// turn rather than by which file it came from, so a log whose format changes
// with a distribution upgrade (rsyslog's switch to RFC 3339) is still read.
package serverlog

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// Sources, as the page filters them.
const (
	SourceNginx    = "nginx"
	SourceSite     = "site"
	SourceRequests = "requests"
	SourcePHP      = "php"
	SourceDatabase = "database"
	SourceFail2ban = "fail2ban"
	SourceAuth     = "auth"
	SourceKernel   = "kernel"
	SourceSystem   = "system"
	SourceMail     = "mail"
)

// SourceLabels are what the page calls them.
var SourceLabels = map[string]string{
	SourceNginx:    "nginx",
	SourceSite:     "Site logs",
	SourceRequests: "Notable requests",
	SourcePHP:      "PHP-FPM",
	SourceDatabase: "Databases",
	SourceFail2ban: "fail2ban",
	SourceAuth:     "Logins & sudo",
	SourceKernel:   "Kernel",
	SourceSystem:   "System",
	SourceMail:     "Mail",
}

// Levels, worst first.
const (
	LevelCritical = "critical"
	LevelError    = "error"
	LevelWarning  = "warning"
	LevelInfo     = "info"
)

// Event is one line, read.
type Event struct {
	At      int64
	Source  string
	Site    string
	Level   string
	IP      string
	Message string
}

// File is one log the collector reads.
type File struct {
	Path   string
	Source string
	Site   string

	// Access is an nginx access log: only its notable requests are kept.
	Access bool
}

// Reader turns a file's lines into events. It remembers the last event, so a
// line without a time of its own — a stack trace, a wrapped message — is
// added to the one it continues.
type Reader struct {
	File      File
	Location  *time.Location
	Now       time.Time
	PanelUser string

	Events []Event
}

// maxMessage is as much of a line as is kept. A minified SQL query in a
// MySQL error is not more useful at 40 KB than at 2.
const maxMessage = 2000

// Line reads one line.
func (r *Reader) Line(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}

	if r.File.Access {
		if event, ok := r.access(line); ok {
			r.Events = append(r.Events, event)
		}
		return
	}

	at, rest, ok := r.stamp(line)
	if !ok {
		// A continuation: kept with what it continues, or dropped when the
		// first line of the read is one.
		if n := len(r.Events); n > 0 && len(r.Events[n-1].Message) < maxMessage {
			r.Events[n-1].Message = clip(r.Events[n-1].Message + "\n" + line)
		}
		return
	}

	rest = strings.TrimSpace(rest)

	if skip(r.File.Source, rest, r.PanelUser) {
		return
	}

	r.Events = append(r.Events, Event{
		At:      at.Unix(),
		Source:  r.File.Source,
		Site:    r.File.Site,
		Level:   level(rest),
		IP:      address(rest),
		Message: clip(rest),
	})
}

func clip(text string) string {
	if len(text) > maxMessage {
		return text[:maxMessage] + "…"
	}
	return text
}

// access keeps the requests worth reading about one by one: server errors,
// requests refused for going too fast, and the ones that look like an attack.
// The rest are what the Website traffic view adds up.
func (r *Reader) access(line string) (Event, bool) {
	entry, ok := weblog.Parse(line)
	if !ok {
		return Event{}, false
	}

	threat := weblog.Threat(entry)

	var lvl, why string

	switch {
	case entry.Status >= 500:
		lvl, why = LevelError, "server error"
	case entry.Status == 429:
		lvl, why = LevelWarning, "rate limited"
	case threat != "":
		lvl, why = LevelWarning, strings.ToLower(weblog.ThreatLabels[threat])
	default:
		return Event{}, false
	}

	agent := entry.UserAgent
	if len(agent) > 120 {
		agent = agent[:120] + "…"
	}

	return Event{
		At:     entry.Time.Unix(),
		Source: r.File.Source,
		Site:   r.File.Site,
		Level:  lvl,
		IP:     entry.IP,
		Message: clip(entry.Method + " " + entry.Path + " → " + strconv.Itoa(entry.Status) +
			" (" + why + ") · " + agent),
	}, true
}

// --------------------------------------------------------------- the time

var (
	// 2026-11-04T10:00:00.123456+06:00, …Z, or no zone: rsyslog on Ubuntu
	// 23.10 and later, MySQL 8, MongoDB.
	isoStamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?\s*`)

	// 2026/11/04 10:00:00 — nginx's error log.
	slashStamp = regexp.MustCompile(`^(\d{4})/(\d{2})/(\d{2}) (\d{2}:\d{2}:\d{2})\s*`)

	// 2026-11-04 10:00:00,123 — fail2ban, MariaDB.
	spaceStamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})([.,]\d+)?\s*`)

	// [04-Nov-2026 10:00:00] or [04-Nov-2026 10:00:00 UTC] — PHP-FPM, PHP's
	// own error log, WordPress's debug.log.
	phpStamp = regexp.MustCompile(`^\[(\d{2}-[A-Z][a-z]{2}-\d{4} \d{2}:\d{2}:\d{2})(?: ([A-Za-z_/+-]+))?\]\s*`)

	// [2026-11-04 10:00:00] — Laravel.
	bracketStamp = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]\s*`)

	// Nov  4 10:00:00 — traditional syslog, with no year.
	syslogStamp = regexp.MustCompile(`^([A-Z][a-z]{2}) +(\d{1,2}) (\d{2}:\d{2}:\d{2})\s+`)

	// 1234:M 04 Nov 2026 10:00:00.123 — Redis.
	redisStamp = regexp.MustCompile(`^\d+:[A-Z] (\d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2})(\.\d+)?\s*`)
)

// stamp finds the line's time and returns the line without it.
func (r *Reader) stamp(line string) (time.Time, string, bool) {
	loc := r.Location
	if loc == nil {
		loc = time.Local
	}

	if m := isoStamp.FindStringSubmatch(line); m != nil {
		text := m[1] + "T" + m[2] + m[3]
		zone := m[4]

		if zone == "" {
			if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", text, loc); err == nil {
				return t, line[len(m[0]):], true
			}
		} else {
			if zone != "Z" && !strings.Contains(zone, ":") {
				zone = zone[:3] + ":" + zone[3:]
			}
			if t, err := time.Parse(time.RFC3339Nano, text+zone); err == nil {
				return t, line[len(m[0]):], true
			}
		}
	}

	if m := slashStamp.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1]+"/"+m[2]+"/"+m[3]+" "+m[4], loc); err == nil {
			return t, line[len(m[0]):], true
		}
	}

	if m := spaceStamp.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+m[2], loc); err == nil {
			return t, line[len(m[0]):], true
		}
	}

	if m := phpStamp.FindStringSubmatch(line); m != nil {
		zone := loc
		if m[2] != "" {
			if named, err := time.LoadLocation(m[2]); err == nil {
				zone = named
			}
		}
		if t, err := time.ParseInLocation("02-Jan-2006 15:04:05", m[1], zone); err == nil {
			return t, line[len(m[0]):], true
		}
	}

	if m := bracketStamp.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], loc); err == nil {
			return t, line[len(m[0]):], true
		}
	}

	if m := syslogStamp.FindStringSubmatch(line); m != nil {
		now := r.Now
		if now.IsZero() {
			now = time.Now()
		}

		text := m[1] + " " + m[2] + " " + m[3] + " " + strconv.Itoa(now.In(loc).Year())
		if t, err := time.ParseInLocation("Jan 2 15:04:05 2006", text, loc); err == nil {
			// No year in the line: one that would be in the future is from
			// last December, read in January.
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, line[len(m[0]):], true
		}
	}

	if m := redisStamp.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("02 Jan 2006 15:04:05", m[1], loc); err == nil {
			return t, line[len(m[0]):], true
		}
	}

	return time.Time{}, line, false
}

// ------------------------------------------------------------- the rest

var (
	critical = regexp.MustCompile(`(?i)\[(emerg|alert|crit)\]|\bcritical\b|\bfatal\b|\bpanic\b|out of memory|oom-kill|killed process|segfault`)
	errored  = regexp.MustCompile(`(?i)\[error\]|\berror\b|\berr\b|\bfailed\b|\bfailure\b|\bdenied\b|authentication failure|\bexception\b`)
	warned   = regexp.MustCompile(`(?i)\[warn\]|\bwarning\b|\bwarn\b|\bnotice\b.*\bban\b|\bban\b|invalid user|\brefused\b|\bdeprecated\b|max_children|limiting requests`)
)

func level(message string) string {
	switch {
	case critical.MatchString(message):
		return LevelCritical
	case errored.MatchString(message):
		return LevelError
	case warned.MatchString(message):
		return LevelWarning
	}

	return LevelInfo
}

var (
	clientField = regexp.MustCompile(`client: ([0-9a-fA-F:.]+)`)
	fromField   = regexp.MustCompile(`(?:from|rip=|\[)\s*([0-9a-fA-F:.]{7,45})`)
	ipv4        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
)

// address is the visitor or attacker a line is about, when it names one.
func address(message string) string {
	for _, expression := range []*regexp.Regexp{clientField, fromField} {
		if m := expression.FindStringSubmatch(message); m != nil {
			if candidate := strings.Trim(m[1], ".:[]"); net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}

	for _, candidate := range ipv4.FindAllString(message, 3) {
		if ip := net.ParseIP(candidate); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return candidate
		}
	}

	return ""
}

// program is the "prog[pid]:" a syslog line names after its host.
var program = regexp.MustCompile(`^\S+ ([^\s:\[]+)(?:\[\d+\])?: `)

// skip drops what is noise, or is somewhere else already:
//
//   - the panel's own sudo, which is every minute of every day;
//   - sudo and cron session open and close lines, for the same reason;
//   - sshd, whose logins and attempts the panel already keeps as SSH events;
//   - fail2ban's INFO lines, one per log line it matched, which an attack
//     turns into thousands — the bans themselves are NOTICE;
//   - in syslog, the kernel and mail lines rsyslog also writes to kern.log
//     and mail.log, which are read for themselves.
func skip(source, message, panelUser string) bool {
	switch source {
	case SourceAuth:
		if strings.Contains(message, "pam_unix(sudo:session)") ||
			strings.Contains(message, "pam_unix(cron:session)") ||
			strings.Contains(message, "pam_unix(sudo-i:session)") {
			return true
		}
		if m := program.FindStringSubmatch(message); m != nil {
			switch m[1] {
			case "sshd", "sshd-session":
				return true
			case "sudo":
				// "sudo: ubuntupanel : PWD=/ ; USER=root ; COMMAND=…", the
				// user padded with spaces on some versions.
				rest := strings.TrimSpace(message[len(m[0]):])
				if panelUser != "" && strings.HasPrefix(rest, panelUser+" ") {
					return true
				}
			}
		}

	case SourceFail2ban:
		return strings.Contains(message, " INFO ") || strings.Contains(message, "INFO    ")

	case SourceSystem:
		if m := program.FindStringSubmatch(message); m != nil {
			name := m[1]
			if name == "kernel" || strings.HasPrefix(name, "postfix/") || name == "dovecot" ||
				name == "opendkim" || name == "sudo" || name == "sshd" || name == "sshd-session" {
				return true
			}
			if name == "CRON" && strings.Contains(message, "pam_unix") {
				return true
			}
		}
	}

	return false
}
