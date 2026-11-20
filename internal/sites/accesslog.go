package sites

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Every server block the panel writes for a hostname logs to that hostname's
// own file. A block without an access_log of its own inherits nginx's
// /var/log/nginx/access.log — the log of the catch-all, which the Analytics
// page reads as "(no site — server IP)": requests to this machine's bare
// address and to names nobody configured. An HTTPS site's http:// redirect, a
// mail domain's webmail redirect and its autoconfig hostnames used to have no
// log of their own, so their 301s and 404s were counted as the server IP
// being answered while it was in fact refusing.

// AccessLogLine is the line a block for host logs with.
func AccessLogLine(host string) string {
	return fmt.Sprintf("    access_log /var/log/nginx/%s-access.log;\n", host)
}

var (
	serverOpen = regexp.MustCompile(`^server\s*\{\s*$`)
	serverName = regexp.MustCompile(`^\s+server_name\s+([^\s;]+)[^;]*;\s*$`)
	accessLog  = regexp.MustCompile(`^\s+access_log\s`)
)

// AddMissingAccessLogs gives each top-level server block in a file the panel
// wrote an access_log of its own, named after its first server_name, when it
// has none. Blocks for `_` — the default servers — are left on the shared log,
// which is theirs. Reports whether anything changed.
func AddMissingAccessLogs(contents string) (string, bool) {
	lines := strings.SplitAfter(contents, "\n")
	out := make([]string, 0, len(lines)+4)

	inServer, hasLog := false, false
	name, nameAt := "", -1

	for _, line := range lines {
		bare := strings.TrimRight(line, "\n")

		switch {
		case serverOpen.MatchString(bare):
			inServer, hasLog, name, nameAt = true, false, "", -1

		case inServer && bare == "}":
			if !hasLog && nameAt >= 0 && name != "_" {
				out = append(out[:nameAt+1], append([]string{AccessLogLine(name)}, out[nameAt+1:]...)...)
			}
			inServer = false

		case inServer && nameAt < 0 && serverName.MatchString(bare):
			name, nameAt = strings.ToLower(serverName.FindStringSubmatch(bare)[1]), len(out)

		case inServer && accessLog.MatchString(bare):
			hasLog = true
		}

		out = append(out, line)
	}

	updated := strings.Join(out, "")

	return updated, updated != contents
}

// EnsureOwnAccessLogs applies AddMissingAccessLogs to every vhost the panel
// manages — sites, webmail, autoconfig — and reloads nginx once. All of them
// are put back if nginx will not have the result.
func EnsureOwnAccessLogs() error {
	listing := proc.Run("ls -1 /etc/nginx/sites-available/ 2>/dev/null", time.Minute)
	if !listing.OK() {
		return nil
	}

	previous := map[string]string{}

	for _, file := range strings.Fields(listing.Output) {
		if strings.ContainsAny(file, "/\\") || file == "ubuntu-panel" {
			continue
		}

		path := "/etc/nginx/sites-available/" + file

		contents, err := proc.ReadFile(path, true)
		if err != nil || !strings.HasPrefix(strings.TrimSpace(contents), "# Managed by") {
			continue
		}

		updated, changed := AddMissingAccessLogs(contents)
		if !changed {
			continue
		}

		if err := proc.PutFile(path, updated, true); err != nil {
			for path, contents := range previous {
				proc.PutFile(path, contents, true)
			}
			return err
		}
		previous[path] = contents
	}

	if len(previous) == 0 {
		return nil
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		for path, contents := range previous {
			proc.PutFile(path, contents, true)
		}
		return fmt.Errorf("nginx refused the vhosts with their own access logs, so they were put back: %s", check.Output)
	}

	if !proc.Ok("sudo systemctl reload nginx") {
		return errors.New("nginx accepted the vhosts but would not reload")
	}

	return nil
}
