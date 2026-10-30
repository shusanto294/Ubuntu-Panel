// Package security is what the panel does to keep this machine from being
// broken into, and what it reads to tell the operator who has been trying.
//
// Four parts, each a program the machine already runs rather than one the
// panel reimplements: fail2ban for SSH brute force, sshd's own configuration
// for how SSH may be used at all, ufw for which ports answer, and apt for the
// patches. The panel writes their configuration, reads their state and
// translates both into something a page can show.
package security

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

const (
	// JailPath is the panel's fail2ban jail. `.local` rather than `.conf`: it
	// is the suffix fail2ban reads after the packaged files, so the panel's
	// values win over the distribution's defaults without editing them.
	JailPath = "/etc/fail2ban/jail.d/ubuntu-panel.local"

	// daemonPath keeps the ban database long enough for a permanent ban to
	// stay permanent. fail2ban restores bans from its database on restart, and
	// the default purge age of a day means a ban that was meant to be forever
	// quietly lapses at the first reboot after it.
	daemonPath = "/etc/fail2ban/fail2ban.d/ubuntu-panel.local"

	// Jail is the one jail the panel manages.
	Jail = "sshd"

	// Permanent is fail2ban's value for a ban that never expires.
	Permanent = -1
)

// Defaults are what a machine gets before anybody visits the Security page:
// three wrong attempts inside a day and the address is never let back.
var Defaults = JailConfig{MaxRetry: 3, FindTime: 86400, BanTime: Permanent}

// JailConfig is the panel's SSH jail.
type JailConfig struct {
	// MaxRetry is how many failures an address is allowed.
	MaxRetry int `json:"max_retry"`

	// FindTime is the window, in seconds, the failures are counted over.
	FindTime int `json:"find_time"`

	// BanTime is how long, in seconds, an address stays banned. Permanent is
	// forever.
	BanTime int `json:"ban_time"`

	// IgnoreIPs are addresses and ranges that are never banned: the office,
	// the VPN, the operator's own connection.
	IgnoreIPs []string `json:"ignore_ips"`
}

// Validate is what the form checks before the file is written.
func (c JailConfig) Validate() map[string]string {
	problems := map[string]string{}

	if c.MaxRetry < 1 || c.MaxRetry > 100 {
		problems["max_retry"] = "Allow between 1 and 100 attempts."
	}
	if c.FindTime < 60 || c.FindTime > 30*86400 {
		problems["find_time"] = "Count attempts over a window between a minute and thirty days."
	}
	if c.BanTime != Permanent && (c.BanTime < 60 || c.BanTime > 365*86400) {
		problems["ban_time"] = "Ban for between a minute and a year, or permanently."
	}

	for _, address := range c.IgnoreIPs {
		if !ValidAddress(address) {
			problems["ignore_ips"] = fmt.Sprintf("%q is not an IP address or a range like 203.0.113.0/24.", address)
			break
		}
	}

	return problems
}

// ValidAddress is an IP address or a CIDR range, which is everything fail2ban
// and ufw are handed. Validated rather than quoted: these end up in a
// configuration file as well as on a command line.
func ValidAddress(text string) bool {
	if net.ParseIP(text) != nil {
		return true
	}

	_, _, err := net.ParseCIDR(text)

	return err == nil
}

// ValidIP is a single address, which is all a ban takes.
func ValidIP(text string) bool { return net.ParseIP(text) != nil }

// JailFile renders the jail. sshPorts are the ports sshd listens on, which the
// ban applies to — and only those: an address banned for guessing SSH
// passwords can still load the sites on this machine and reach the panel,
// which is also where it gets unbanned.
func JailFile(config JailConfig, sshPorts []string, journal bool) string {
	ports := "ssh"
	if len(sshPorts) > 0 {
		ports = strings.Join(sshPorts, ",")
	}

	ignore := append([]string{"127.0.0.1/8", "::1"}, config.IgnoreIPs...)

	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — change these from the Security page.\n")
	file.WriteString("# Edits made here are overwritten the next time it saves.\n\n")
	file.WriteString("[sshd]\n")
	file.WriteString("enabled  = true\n")
	fmt.Fprintf(&file, "port     = %s\n", ports)

	// Aggressive also counts the attempts that never reach a password prompt
	// — an invalid user disconnected before authenticating, a key offered and
	// refused — which is what most scanners actually do.
	file.WriteString("mode     = aggressive\n")
	fmt.Fprintf(&file, "maxretry = %d\n", config.MaxRetry)
	fmt.Fprintf(&file, "findtime = %d\n", config.FindTime)
	fmt.Fprintf(&file, "bantime  = %d\n", config.BanTime)
	fmt.Fprintf(&file, "ignoreip = %s\n", strings.Join(ignore, " "))

	// A machine with no /var/log/auth.log logs SSH to the journal alone, and
	// fail2ban's default backend would then be watching a file nothing writes.
	if journal {
		file.WriteString("backend  = systemd\n")
	}

	return file.String()
}

// daemonFile keeps bans in the database for ten years.
const daemonFile = "# Managed by Ubuntu Panel: permanent bans survive a restart.\n" +
	"[DEFAULT]\n" +
	"dbpurgeage = 315360000\n"

// Apply writes the jail and has fail2ban load it. The previous file is put
// back if fail2ban will not start with the new one: a broken jail is no
// protection at all, and the old one at least was working.
func Apply(config JailConfig, sshPorts []string) error {
	if !proc.Which("fail2ban-client") {
		return errors.New("fail2ban is not installed")
	}

	journal := !proc.Ok("sudo test -s /var/log/auth.log")

	previous, _ := proc.ReadFile(JailPath, true)

	if err := proc.PutFile(JailPath, JailFile(config, sshPorts, journal), true); err != nil {
		return err
	}

	if err := proc.PutFile(daemonPath, daemonFile, true); err != nil {
		return err
	}

	if result := proc.Run("sudo systemctl enable fail2ban >/dev/null 2>&1; "+
		"sudo systemctl restart fail2ban && sleep 1 && sudo fail2ban-client status "+Jail,
		2*time.Minute); !result.OK() {
		if previous != "" {
			proc.PutFile(JailPath, previous, true)
		} else {
			proc.Run("sudo rm -f "+JailPath, time.Minute)
		}

		proc.Run("sudo systemctl restart fail2ban", 2*time.Minute)

		return fmt.Errorf("fail2ban would not start with the new jail, so the previous one "+
			"was put back: %s", lastLines(result.Output, 6))
	}

	return nil
}

// JailStatus is what fail2ban says about the SSH jail right now.
type JailStatus struct {
	Installed       bool     `json:"installed"`
	Running         bool     `json:"running"`
	Managed         bool     `json:"managed"`
	CurrentlyFailed int      `json:"currently_failed"`
	TotalFailed     int      `json:"total_failed"`
	CurrentlyBanned int      `json:"currently_banned"`
	TotalBanned     int      `json:"total_banned"`
	Banned          []string `json:"banned"`
}

// Status asks fail2ban.
func Status() JailStatus {
	status := JailStatus{Installed: proc.Which("fail2ban-client"), Banned: []string{}}

	if !status.Installed {
		return status
	}

	status.Managed = proc.Exists(JailPath, true)

	result := proc.Run("sudo fail2ban-client status "+Jail+" 2>&1", 30*time.Second)
	if !result.OK() {
		return status
	}

	status.Running = true
	parseJailStatus(result.Output, &status)

	return status
}

var jailLine = regexp.MustCompile(`(?m)^[\s|` + "`" + `-]*(Currently failed|Total failed|Currently banned|Total banned|Banned IP list):\s*(.*)$`)

func parseJailStatus(output string, status *JailStatus) {
	for _, match := range jailLine.FindAllStringSubmatch(output, -1) {
		value := strings.TrimSpace(match[2])
		number, _ := strconv.Atoi(value)

		switch match[1] {
		case "Currently failed":
			status.CurrentlyFailed = number
		case "Total failed":
			status.TotalFailed = number
		case "Currently banned":
			status.CurrentlyBanned = number
		case "Total banned":
			status.TotalBanned = number
		case "Banned IP list":
			for _, address := range strings.Fields(value) {
				if ValidIP(address) {
					status.Banned = append(status.Banned, address)
				}
			}
		}
	}

	sort.Strings(status.Banned)
}

// Ban bans an address from SSH now, whether or not it has failed anything.
func Ban(address string) error {
	if !ValidIP(address) {
		return errors.New("that is not an IP address")
	}

	if result := proc.Run("sudo fail2ban-client set "+Jail+" banip "+proc.Quote(address),
		30*time.Second); !result.OK() {
		return fmt.Errorf("fail2ban refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// Unban lets an address back in.
func Unban(address string) error {
	if !ValidIP(address) {
		return errors.New("that is not an IP address")
	}

	if result := proc.Run("sudo fail2ban-client set "+Jail+" unbanip "+proc.Quote(address),
		30*time.Second); !result.OK() {
		return fmt.Errorf("fail2ban refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}

	return strings.Join(lines, " ")
}
