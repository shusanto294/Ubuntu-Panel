package security

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// SSH event kinds.
const (
	// EventAccepted is a login that worked.
	EventAccepted = "accepted"

	// EventFailed is a wrong password or a refused key for a user that exists.
	EventFailed = "failed"

	// EventInvalid is an attempt at a user this machine does not have, which
	// is nearly always a scanner working through a list.
	EventInvalid = "invalid"
)

// Event is one line of sshd's log that says somebody tried to log in.
type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	User   string    `json:"user"`
	IP     string    `json:"ip"`
	Method string    `json:"method"`
}

var (
	acceptedLine = regexp.MustCompile(`Accepted (\S+) for (\S+) from (\S+) port \d+`)
	failedLine   = regexp.MustCompile(`Failed (\S+) for (invalid user )?(\S*) from (\S+) port \d+`)
	invalidLine  = regexp.MustCompile(`Invalid user (\S*) from (\S+) port \d+`)
)

// ParseLine reads one log line, or says it was not about a login.
//
// "Invalid user x from …" and "Failed password for invalid user x from …" are
// two lines for one attempt that got as far as a password; the second is
// counted as the attempt and the first only when nothing followed it, which is
// the scanner that disconnects at the prompt. Telling those apart needs the
// lines around it, so this returns both and Summarise is what de-duplicates.
func ParseLine(line string, now time.Time) (Event, bool) {
	var event Event

	switch {
	case acceptedLine.MatchString(line):
		match := acceptedLine.FindStringSubmatch(line)
		event = Event{Kind: EventAccepted, Method: match[1], User: match[2], IP: match[3]}

	case failedLine.MatchString(line):
		match := failedLine.FindStringSubmatch(line)
		event = Event{Kind: EventFailed, Method: match[1], User: match[3], IP: match[4]}

		if match[2] != "" {
			event.Kind = EventInvalid
		}

	case invalidLine.MatchString(line):
		match := invalidLine.FindStringSubmatch(line)
		event = Event{Kind: EventInvalid, Method: "none", User: match[1], IP: match[2]}

	default:
		return Event{}, false
	}

	if !ValidIP(event.IP) {
		return Event{}, false
	}

	event.Time = parseLogTime(line, now)

	return event, true
}

// parseLogTime reads the timestamp a log line starts with, in either of the
// two shapes an Ubuntu machine writes: ISO 8601 (journalctl -o short-iso, and
// rsyslog from 24.04) or the classic "Oct 30 12:00:00", which has no year.
func parseLogTime(line string, now time.Time) time.Time {
	first, _, _ := strings.Cut(line, " ")

	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.000000-07:00"} {
		if parsed, err := time.Parse(layout, first); err == nil {
			return parsed
		}
	}

	if len(line) >= 15 {
		if parsed, err := time.ParseInLocation("Jan _2 15:04:05", line[:15], time.Local); err == nil {
			stamped := parsed.AddDate(now.Year(), 0, 0)

			// December's lines read in January belong to last year.
			if stamped.After(now.Add(24 * time.Hour)) {
				stamped = stamped.AddDate(-1, 0, 0)
			}

			return stamped
		}
	}

	return now
}

// ReadEvents reads sshd's log for the given period, newest first, at most
// limit of them.
//
// The journal first, because it is the one place every supported Ubuntu
// writes; under both identifiers, because OpenSSH 9.8 moved authentication
// into a separate sshd-session binary that logs under its own name. The file
// is the fallback for a machine whose journal is not persistent.
func ReadEvents(since time.Duration, limit int) []Event {
	window := int(since.Seconds())

	command := fmt.Sprintf(
		"sudo journalctl -t sshd -t sshd-session --since '-%ds' -o short-iso --no-pager -q 2>/dev/null "+
			"| grep -E 'Accepted |Failed |Invalid user ' | tail -n 5000", window)

	output := proc.Run(command, time.Minute).Output

	if strings.TrimSpace(output) == "" {
		output = proc.Run("sudo cat /var/log/auth.log.1 /var/log/auth.log 2>/dev/null "+
			"| grep -E 'sshd(-session)?\\[' | grep -E 'Accepted |Failed |Invalid user ' | tail -n 5000",
			time.Minute).Output
	}

	return parseEvents(output, time.Now(), since, limit)
}

func parseEvents(output string, now time.Time, since time.Duration, limit int) []Event {
	cutoff := now.Add(-since)
	events := []Event{}

	lines := strings.Split(output, "\n")

	for i, line := range lines {
		event, ok := ParseLine(line, now)
		if !ok || event.Time.Before(cutoff) {
			continue
		}

		// An "Invalid user" line followed by the "Failed password for invalid
		// user" of the same attempt: the second one is the record.
		if event.Method == "none" && i+1 < len(lines) {
			if next, ok := ParseLine(lines[i+1], now); ok && next.Kind == EventInvalid &&
				next.IP == event.IP && next.User == event.User {
				continue
			}
		}

		events = append(events, event)
	}

	// Newest first, which is the order anybody reads a security log in.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.After(events[j].Time) })

	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	return events
}

// Attacker is one address and what it has been doing.
type Attacker struct {
	IP       string    `json:"ip"`
	Attempts int       `json:"attempts"`
	Users    []string  `json:"users"`
	LastSeen time.Time `json:"last_seen"`
}

// Summary is the shape the Security page shows above the log.
type Summary struct {
	Accepted     int        `json:"accepted"`
	Failed       int        `json:"failed"`
	UniqueIPs    int        `json:"unique_ips"`
	TopAttackers []Attacker `json:"top_attackers"`
}

// Summarise counts events and ranks the addresses that failed most.
func Summarise(events []Event, top int) Summary {
	summary := Summary{TopAttackers: []Attacker{}}

	byIP := map[string]*Attacker{}
	users := map[string]map[string]bool{}

	for _, event := range events {
		if event.Kind == EventAccepted {
			summary.Accepted++
			continue
		}

		summary.Failed++

		attacker, ok := byIP[event.IP]
		if !ok {
			attacker = &Attacker{IP: event.IP}
			byIP[event.IP] = attacker
			users[event.IP] = map[string]bool{}
		}

		attacker.Attempts++

		if event.Time.After(attacker.LastSeen) {
			attacker.LastSeen = event.Time
		}

		if event.User != "" && !users[event.IP][event.User] && len(attacker.Users) < 5 {
			users[event.IP][event.User] = true
			attacker.Users = append(attacker.Users, event.User)
		}
	}

	summary.UniqueIPs = len(byIP)

	for _, attacker := range byIP {
		summary.TopAttackers = append(summary.TopAttackers, *attacker)
	}

	sort.Slice(summary.TopAttackers, func(i, j int) bool {
		if summary.TopAttackers[i].Attempts != summary.TopAttackers[j].Attempts {
			return summary.TopAttackers[i].Attempts > summary.TopAttackers[j].Attempts
		}
		return summary.TopAttackers[i].IP < summary.TopAttackers[j].IP
	})

	if len(summary.TopAttackers) > top {
		summary.TopAttackers = summary.TopAttackers[:top]
	}

	return summary
}
