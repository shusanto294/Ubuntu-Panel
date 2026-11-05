package security

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// What a compromised server looks like from the inside. The usual end of a
// WordPress plugin exploit, an open Redis or a stolen password is not a
// defaced page but a cryptocurrency miner: a binary dropped in /tmp, run from
// there (often deleted straight after so `ls` shows nothing), connecting to a
// mining pool. The panel looks for those three marks every few minutes and
// alerts; it kills nothing, because a false alarm that kills a process is an
// outage and one that sends an email is a minute of somebody's time.

// SuspiciousProcess is one program that looks like it should not be running.
type SuspiciousProcess struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	User   string `json:"user"`
	Reason string `json:"reason"`
}

// minerNames are the process names of common miners and of the malware
// families that drop them.
var minerNames = map[string]bool{
	"xmrig": true, "xmrig-notls": true, "xmr-stak": true, "xmr-stak-cpu": true, "minerd": true,
	"cpuminer": true, "cpuminer-multi": true, "nbminer": true, "t-rex": true, "lolminer": true,
	"phoenixminer": true, "ethminer": true, "kdevtmpfsi": true, "kinsing": true, "sysrv": true,
	"kthreaddi": true, "kthreaddk": true, "dbused": true, "solrd": true, "networkservice": true,
	"sysupdate": true, "kworkerds": true, "watchbog": true, "skidmap": true,
}

// poolPorts are the ports mining pools speak Stratum on that nothing else
// usually does.
var poolPorts = map[int]bool{3333: true, 3334: true, 5730: true, 14433: true, 14444: true, 20580: true,
	45560: true, 45700: true}

// scratchDirs are where nothing that is installed runs from.
var scratchDirs = []string{"/tmp/", "/var/tmp/", "/dev/shm/", "/run/shm/"}

// processListing prints one line per process: pid, user, name and the
// program it runs. As root, because another user's /proc/<pid>/exe cannot be
// read otherwise.
const processListing = `sudo sh -c 'for p in /proc/[0-9]*; do e=$(readlink "$p/exe" 2>/dev/null) || continue; ` +
	`u=$(stat -c %U "$p" 2>/dev/null); c=$(cat "$p/comm" 2>/dev/null); ` +
	`printf "%s\t%s\t%s\t%s\n" "${p#/proc/}" "$u" "$c" "$e"; done'`

// ScanProcesses lists what looks like it should not be running.
func ScanProcesses() []SuspiciousProcess {
	found := ParseProcesses(proc.Run(processListing, time.Minute).Output)
	found = append(found, ParsePoolConnections(proc.Run("sudo ss -tnpH state established 2>/dev/null", 30*time.Second).Output)...)

	sort.Slice(found, func(i, j int) bool { return found[i].PID < found[j].PID })

	return found
}

// ParseProcesses reads processListing's lines.
func ParseProcesses(output string) []SuspiciousProcess {
	found := []SuspiciousProcess{}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		user, name, path := fields[1], fields[2], fields[3]
		deleted := strings.HasSuffix(path, " (deleted)")
		clean := strings.TrimSuffix(path, " (deleted)")

		reason := ""
		switch {
		case minerNames[strings.ToLower(name)]:
			reason = "named like a cryptocurrency miner"
		case inScratch(clean):
			reason = "running from " + scratchOf(clean) + ", where nothing installed lives"
			if deleted {
				reason += ", and its file was deleted after it started"
			}
		case deleted && strings.Contains(clean, "/."):
			reason = "running from a hidden directory, and its file was deleted after it started"
		}

		if reason != "" {
			found = append(found, SuspiciousProcess{PID: pid, Name: name, Path: clean, User: user, Reason: reason})
		}
	}

	return found
}

// ParsePoolConnections reads `ss -tnpH` for connections out to mining pool
// ports.
//
//	0 0 10.0.0.5:51234 203.0.113.9:3333 users:(("xmrig",pid=4242,fd=12))
func ParsePoolConnections(output string) []SuspiciousProcess {
	found := []SuspiciousProcess{}
	seen := map[int]bool{}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		_, port, err := net.SplitHostPort(fields[3])
		if err != nil {
			continue
		}
		number, _ := strconv.Atoi(port)
		if !poolPorts[number] {
			continue
		}

		pid, name := 0, ""
		if len(fields) >= 5 {
			if _, rest, ok := strings.Cut(fields[4], `(("`); ok {
				name, rest, _ = strings.Cut(rest, `"`)
				if _, after, ok := strings.Cut(rest, "pid="); ok {
					digits, _, _ := strings.Cut(after, ",")
					pid, _ = strconv.Atoi(digits)
				}
			}
		}

		if pid != 0 && seen[pid] {
			continue
		}
		seen[pid] = true

		found = append(found, SuspiciousProcess{PID: pid, Name: name,
			Reason: fmt.Sprintf("connected to %s, a mining pool port", fields[3])})
	}

	return found
}

func inScratch(path string) bool { return scratchOf(path) != "" }

func scratchOf(path string) string {
	for _, dir := range scratchDirs {
		if strings.HasPrefix(path, dir) {
			return strings.TrimSuffix(dir, "/")
		}
	}
	return ""
}
