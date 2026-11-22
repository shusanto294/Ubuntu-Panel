package mail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// How many IMAP connections the mail server can hold at once.
//
// Out of the box Dovecot runs imap-login in "high-security" mode: one process
// per connection, at most 100 of them (default_process_limit). Over TLS — port
// 993, which is every real client — that login process does not exit after the
// login: it stays for the whole session to encrypt the traffic. So 100 clients
// that stay connected, which is what IDLE is (phones, Thunderbird, and above all
// cold-email tools that keep one connection open per mailbox and check others
// on a schedule), fill every slot. Connection 101 is accepted by the kernel and
// dropped by Dovecot without a word ("process_limit (100) reached, client
// connections are being dropped"), and the client sees the TLS handshake reset:
// ECONNRESET, for every mailbox at once, with nothing wrong with any of them.
//
// This drop-in sizes Dovecot for thousands of simultaneous connections:
//
//   - imap-login in "performance" mode: a few long-lived login processes (one
//     per CPU core) each proxy thousands of TLS sessions, instead of one
//     process per session.
//   - imap sessions allowed to number in the thousands, and idle ones (IDLE
//     waiting for new mail) handed to imap-hibernate after 30 s, which holds
//     them for a fraction of a full session's memory and wakes them on new mail.
//   - no practical cap on connections per mailbox from one address, because
//     one app checking one mailbox from several workers is normal.
//   - Postfix without a per-client connection cap (its default is 50 at once
//     from one address, then "421 Too many connections"), and with room for
//     1000 simultaneous SMTP sessions instead of 100.
//   - the client and open-file limits these numbers need, so Dovecot is not
//     stopped by its own bookkeeping or by the kernel's descriptor limit.
//
// Two tiers, because Dovecot 2.4 renamed some of these settings: the full one
// is tried first, and if `doveconf` refuses it the minimal one — only settings
// both versions share — is used instead. If even that is refused the file is
// removed, so a capacity change can never be what stops the mail server.

const (
	dovecotCapacityConf = "/etc/dovecot/conf.d/99-ubuntu-panel-capacity.conf"
	dovecotLimitsDir    = "/etc/systemd/system/dovecot.service.d"
	dovecotLimitsConf   = dovecotLimitsDir + "/ubuntu-panel-limits.conf"

	// Simultaneous IMAP sessions the server is sized for.
	imapSessionLimit = 20000
	// Connections one mailbox may have open from one address: effectively
	// unlimited (Dovecot's default is 10). A number rather than 0, because 0
	// is not documented to mean "no limit" for this setting.
	imapPerUserIPLimit = 1000000
	// TLS sessions each login process proxies in performance mode.
	loginClientsPerProcess = 5000
	// Open files for the whole of Dovecot: each proxied session is a socket
	// plus a pipe to its imap process, and hibernated ones keep theirs.
	dovecotOpenFiles = 1048576
)

// dovecotCapacityConfig is the drop-in for a machine with this many CPU cores.
// full = performance-mode login + hibernation (Dovecot 2.3 syntax); otherwise
// the minimal tier, which only raises limits whose names never changed.
func dovecotCapacityConfig(cores int, full bool) string {
	if cores < 1 {
		cores = 1
	}

	var b strings.Builder

	fmt.Fprintf(&b, "# Managed by %s — sized for thousands of simultaneous IMAP connections.\n", config.Name)
	b.WriteString("# Without it Dovecot drops every connection past 100 (see internal/mail/capacity.go).\n\n")
	// Raises the client limit of the services every session passes through
	// (auth, anvil, …), which otherwise warn and cap at 1000.
	fmt.Fprintf(&b, "default_client_limit = %d\n\n", imapSessionLimit)

	fmt.Fprintf(&b, "protocol imap {\n  mail_max_userip_connections = %d\n}\n\n", imapPerUserIPLimit)

	if !full {
		// High-security mode kept, with enough room: one login process per
		// session, as many sessions as the server is sized for.
		fmt.Fprintf(&b, "service imap-login {\n  process_limit = %d\n}\n\n", imapSessionLimit)
		fmt.Fprintf(&b, "service imap {\n  process_limit = %d\n}\n", imapSessionLimit)

		return b.String()
	}

	loginProcesses := cores * 2
	if loginProcesses < 4 {
		loginProcesses = 4
	}

	b.WriteString("service imap-login {\n")
	b.WriteString("  # performance mode: each login process serves many sessions\n")
	b.WriteString("  service_count = 0\n")
	fmt.Fprintf(&b, "  process_min_avail = %d\n", cores)
	fmt.Fprintf(&b, "  process_limit = %d\n", loginProcesses)
	fmt.Fprintf(&b, "  client_limit = %d\n", loginClientsPerProcess)
	b.WriteString("  vsz_limit = 1G\n")
	b.WriteString("}\n\n")

	// Hibernation: an IMAP session sitting in IDLE is moved out of its own
	// process into imap-hibernate, and moved back when mail arrives or the
	// client speaks. imap-hibernate hands sessions back through imap-master,
	// which therefore has to be reachable by Dovecot's internal user.
	b.WriteString("imap_hibernate_timeout = 30s\n\n")
	b.WriteString("service imap {\n")
	fmt.Fprintf(&b, "  process_limit = %d\n", imapSessionLimit)
	b.WriteString("  extra_groups = $default_internal_group\n")
	b.WriteString("  unix_listener imap-master {\n    user = $default_internal_user\n  }\n")
	b.WriteString("}\n\n")
	b.WriteString("service imap-hibernate {\n")
	fmt.Fprintf(&b, "  client_limit = %d\n", imapSessionLimit)
	b.WriteString("  unix_listener imap-hibernate {\n    mode = 0660\n    group = $default_internal_group\n  }\n")
	b.WriteString("}\n")

	return b.String()
}

// postfixLimits are the Postfix settings for sending from many mailboxes at
// once through one server. 0 is Postfix's own "no limit".
var postfixLimits = [][2]string{
	// Connections one client address may have open at the same time (default 50).
	{"smtpd_client_connection_count_limit", "0"},
	// New connections, messages and TLS sessions per client per minute (already
	// 0 by default; pinned so nothing else on the machine can lower them).
	{"smtpd_client_connection_rate_limit", "0"},
	{"smtpd_client_message_rate_limit", "0"},
	{"smtpd_client_new_tls_session_rate_limit", "0"},
	// Processes per service, i.e. SMTP sessions served at once (default 100;
	// the rest wait). Postfix has no "unlimited" here short of master.cf.
	{"default_process_limit", "1000"},
}

// ensurePostfixLimits applies postfixLimits, reloading Postfix only if one changed.
func ensurePostfixLimits() (string, error) {
	if !proc.Ok("command -v postconf >/dev/null") {
		return "", nil
	}

	var set []string

	for _, kv := range postfixLimits {
		current := strings.TrimSpace(proc.Run("postconf -h "+kv[0]+" 2>/dev/null", 30*time.Second).Output)
		if current != kv[1] {
			set = append(set, proc.Quote(kv[0]+" = "+kv[1]))
		}
	}

	if len(set) == 0 {
		return "", nil
	}

	if r := proc.Run("sudo postconf -e "+strings.Join(set, " ")+" && sudo systemctl reload postfix", 2*time.Minute); !r.OK() {
		return "", fmt.Errorf("could not lift Postfix's connection limits: %s", lastLine(r.Output))
	}

	return "Postfix accepts unlimited connections per client and serves up to 1000 SMTP sessions at once.\n", nil
}

// dovecotLimitsConfig raises the open-file limit systemd starts Dovecot with.
func dovecotLimitsConfig() string {
	return fmt.Sprintf("# Managed by %s — room for thousands of IMAP connections.\n[Service]\nLimitNOFILE=%d\n",
		config.Name, dovecotOpenFiles)
}

// cpuCores is how many cores the machine has, for the number of login processes.
func cpuCores() int {
	n, err := strconv.Atoi(strings.TrimSpace(proc.Run("nproc", 10*time.Second).Output))
	if err != nil || n < 1 {
		return 1
	}

	return n
}

// EnsureCapacity sizes Dovecot for thousands of simultaneous IMAP connections.
//
// Safe to run on every start: it compares what is on disk with what it would
// write and touches nothing when they match, so Dovecot is only reloaded — or,
// for a new open-file limit, restarted — when something actually changed.
func (s *Service) EnsureCapacity(ctx context.Context) (string, error) {
	if !s.Store.ServiceInstalled("mail") {
		return "", nil
	}

	var log strings.Builder

	// Postfix first and on its own: a Dovecot problem below must not keep
	// sending capped.
	postfixOut, postfixErr := ensurePostfixLimits()
	log.WriteString(postfixOut)

	if postfixErr != nil {
		fmt.Fprintf(&log, "%s\n", postfixErr)
	}

	if !proc.Ok("command -v doveconf >/dev/null") {
		return log.String(), postfixErr
	}

	// The open-file limit first: the drop-in below promises numbers that need it.
	limitsChanged := false
	if current, _ := proc.ReadFile(dovecotLimitsConf, true); current != dovecotLimitsConfig() {
		if r := proc.Run("sudo mkdir -p "+dovecotLimitsDir, 30*time.Second); !r.OK() {
			return "", fmt.Errorf("could not create %s: %s", dovecotLimitsDir, r.Output)
		}

		if err := proc.PutFile(dovecotLimitsConf, dovecotLimitsConfig(), true); err != nil {
			return "", err
		}

		proc.Run("sudo systemctl daemon-reload", time.Minute)

		limitsChanged = true
	}

	previous, _ := proc.ReadFile(dovecotCapacityConf, true)
	cores := cpuCores()

	// The first tier this Dovecot accepts. Each is written and checked with
	// doveconf before anything is reloaded: a Dovecot that refuses its
	// configuration does not come back, and then no mail can be read.
	chosen := ""
	for _, full := range []bool{true, false} {
		contents := dovecotCapacityConfig(cores, full)

		if err := proc.PutFile(dovecotCapacityConf, contents, true); err != nil {
			return "", err
		}

		if check := proc.Run("sudo doveconf -n > /dev/null", time.Minute); check.OK() {
			chosen = contents

			break
		} else {
			fmt.Fprintf(&log, "Dovecot refused the %s capacity settings: %s\n", tierName(full), lastLine(check.Output))
		}
	}

	if chosen == "" {
		proc.Run("sudo rm -f "+dovecotCapacityConf, 30*time.Second)

		if limitsChanged {
			proc.Run("sudo systemctl restart dovecot", 2*time.Minute)
		}

		return log.String(), fmt.Errorf("Dovecot kept its default limit of 100 IMAP connections")
	}

	if chosen == previous && !limitsChanged {
		return log.String(), postfixErr
	}

	// A new open-file limit only reaches a freshly started master; anything
	// else is picked up by a reload, which keeps the connections already open.
	action := "reload"
	if limitsChanged {
		action = "restart"
	}

	if r := proc.Run("sudo systemctl "+action+" dovecot", 2*time.Minute); !r.OK() {
		// Put the previous configuration back rather than leave IMAP down.
		if previous == "" {
			proc.Run("sudo rm -f "+dovecotCapacityConf, 30*time.Second)
		} else {
			_ = proc.PutFile(dovecotCapacityConf, previous, true)
		}

		proc.Run("sudo systemctl restart dovecot", 2*time.Minute)

		return log.String(), fmt.Errorf("Dovecot would not %s with the new limits: %s", action, lastLine(r.Output))
	}

	if chosen == dovecotCapacityConfig(cores, true) {
		fmt.Fprintf(&log, "Dovecot holds up to %d IMAP connections (%d login processes in performance mode, idle sessions hibernated).\n",
			imapSessionLimit, max(cores*2, 4))
	} else {
		fmt.Fprintf(&log, "Dovecot holds up to %d IMAP connections.\n", imapSessionLimit)
	}

	return log.String(), postfixErr
}

func tierName(full bool) string {
	if full {
		return "full"
	}

	return "minimal"
}

func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	return lines[len(lines)-1]
}
