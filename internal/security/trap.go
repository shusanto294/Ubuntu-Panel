package security

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The SSH trap: an address that connects to the SSH port without being on the
// never-ban list is banned from the whole machine on its first try, whether
// SSH is on or off. With SSH on, that makes the never-ban list the only
// addresses SSH and SFTP will take a connection from.
//
// sshd's own log is no use for this: with SSH off nothing listens and there is
// no line, and with it on a line comes only once the client has spoken. A
// firewall rule logs every new connection to the SSH port with trapPrefix
// instead, and the ssh-trap jail bans the address it names after one match.
// The jail's ignore list is what lets the operator in.
const (
	// TrapJail is the jail the trap's bans live in.
	TrapJail = "ssh-trap"

	trapJailPath   = "/etc/fail2ban/jail.d/ubuntu-panel-trap.local"
	trapFilterPath = "/etc/fail2ban/filter.d/ubuntu-panel-ssh-trap.conf"

	// trapPrefix is what the kernel puts in front of the lines the rule logs,
	// and what the filter looks for.
	trapPrefix = "UP-SSH-TRAP "
)

const trapFilter = "# Managed by Ubuntu Panel: connections to the SSH port while SSH is off.\n" +
	"[Definition]\n" +
	"failregex = ^.*" + trapPrefix + ".* SRC=<ADDR> \n" +
	"ignoreregex =\n" +
	"journalmatch = _TRANSPORT=kernel\n"

// TrapJailFile renders the jail. One attempt, a permanent ban, and every port:
// the address is not let near the sites or the panel either. ignore is the same
// never-ban list the SSH jail uses.
func TrapJailFile(ignoreIPs []string, kernLog bool) string {
	ignore := append([]string{"127.0.0.1/8", "::1"}, ignoreIPs...)

	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — the SSH trap, from the Security page.\n")
	file.WriteString("# Edits made here are overwritten the next time it saves.\n\n")
	fmt.Fprintf(&file, "[%s]\n", TrapJail)
	file.WriteString("enabled  = true\n")
	file.WriteString("filter   = ubuntu-panel-ssh-trap\n")
	fmt.Fprintf(&file, "action   = iptables-allports[name=%s, protocol=all]\n", TrapJail)
	file.WriteString("maxretry = 1\n")
	file.WriteString("findtime = 600\n")
	fmt.Fprintf(&file, "bantime  = %d\n", Permanent)
	fmt.Fprintf(&file, "ignoreip = %s\n", strings.Join(ignore, " "))

	// The kernel's lines are in kern.log where rsyslog writes one, and in the
	// journal always.
	if kernLog {
		file.WriteString("logpath  = /var/log/kern.log\n")
		file.WriteString("backend  = auto\n")
	} else {
		file.WriteString("backend  = systemd\n")
	}

	return file.String()
}

// TrapReady says why the trap cannot be set, or nil.
func TrapReady() error {
	if !proc.Which("fail2ban-client") {
		return errors.New("the SSH trap needs fail2ban, which is not installed — " +
			"SSH protection installs it")
	}

	if !proc.Ok("sudo sh -c 'command -v iptables' >/dev/null 2>&1") {
		return errors.New("the SSH trap needs iptables, which is not installed")
	}

	return nil
}

// TrapFilesCurrent is whether what is on disk is what ApplyTrap would write.
func TrapFilesCurrent(ignoreIPs []string) bool {
	jail, _ := proc.ReadFile(trapJailPath, true)
	filter, _ := proc.ReadFile(trapFilterPath, true)

	return jail == TrapJailFile(ignoreIPs, trapKernLog()) && filter == trapFilter
}

func trapKernLog() bool { return proc.Ok("sudo test -s /var/log/kern.log") }

// ApplyTrap writes the trap's jail, or removes it, and has fail2ban load the
// result. Removing it lifts its bans from the firewall; fail2ban keeps them in
// its database, and puts them back if the trap is turned on again.
func ApplyTrap(enabled bool, ignoreIPs []string) error {
	if !enabled {
		if !proc.Exists(trapJailPath, true) {
			return nil
		}

		proc.Run("sudo rm -f "+trapJailPath, time.Minute)

		if result := proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute); !result.OK() {
			return fmt.Errorf("fail2ban would not reload without the trap: %s", lastLines(result.Output, 4))
		}

		return nil
	}

	if err := TrapReady(); err != nil {
		return err
	}

	if err := proc.PutFile(trapFilterPath, trapFilter, true); err != nil {
		return err
	}

	if err := proc.PutFile(trapJailPath, TrapJailFile(ignoreIPs, trapKernLog()), true); err != nil {
		return err
	}

	result := proc.Run("sudo fail2ban-client reload 2>&1 && sleep 1 && sudo fail2ban-client status "+TrapJail+" 2>&1",
		2*time.Minute)
	if result.OK() {
		return nil
	}

	// A jail fail2ban will not load is no trap, and must not cost the SSH jail
	// it would otherwise take down with it.
	proc.Run("sudo rm -f "+trapJailPath, time.Minute)
	proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute)

	return fmt.Errorf("fail2ban would not load the SSH trap, so it was left off: %s",
		lastLines(result.Output, 4))
}

// port is what the trap's rule watches: a number, or `ssh` when sshd's own
// configuration could not be read.
var trapPort = regexp.MustCompile(`^([0-9]{1,5}|ssh)$`)

// trapRule is the firewall rule, for iptables and ip6tables alike. New
// connections only, so one attempt is one line; limited, so a flood of them is
// not a flood of the kernel log — the first one is all the jail needs.
func trapRule(ports []string) string {
	valid := []string{}
	for _, port := range ports {
		if trapPort.MatchString(port) {
			valid = append(valid, port)
		}
	}

	if len(valid) == 0 {
		valid = []string{"ssh"}
	}

	return "INPUT -p tcp -m multiport --dports " + strings.Join(valid, ",") +
		" -m conntrack --ctstate NEW -m limit --limit 30/second --limit-burst 60" +
		" -j LOG --log-level 4 --log-prefix '" + trapPrefix + "'"
}

// SetTrapRule puts the logging rule in place, or takes it away. Idempotent:
// a rule already there is left alone, so checking every minute costs one
// `iptables -C`. A rule for different ports — sshd moved — is replaced.
func SetTrapRule(on bool, ports []string) error {
	rule := trapRule(ports)

	if on && proc.Ok("sudo iptables -C "+rule+" 2>/dev/null") {
		return nil
	}

	// Every rule carrying the prefix, whatever ports it was written for.
	for _, tool := range []string{"iptables", "ip6tables"} {
		proc.Run(fmt.Sprintf(
			"sudo %[1]s -S INPUT 2>/dev/null | grep -F -- '--log-prefix \"%[2]s\"' | sed 's/^-A /-D /' | "+
				"while read -r rule; do eval \"sudo %[1]s $rule\"; done; true",
			tool, trapPrefix), time.Minute)
	}

	if !on {
		return nil
	}

	if result := proc.Run("sudo iptables -I "+strings.Replace(rule, "INPUT", "INPUT 1", 1)+" 2>&1",
		time.Minute); !result.OK() {
		return fmt.Errorf("could not add the SSH trap's firewall rule: %s", lastLines(result.Output, 3))
	}

	// IPv6 too where the machine has it; a machine without is not a failure.
	proc.Run("sudo ip6tables -I "+strings.Replace(rule, "INPUT", "INPUT 1", 1)+" >/dev/null 2>&1", time.Minute)

	return nil
}

// TrapRulePresent is whether a trap rule is in the firewall, for whatever
// ports: the every-minute check, which need not read sshd's configuration.
func TrapRulePresent() bool {
	return proc.Ok("sudo iptables -S INPUT 2>/dev/null | grep -qF -- " + proc.Quote(`--log-prefix "`+trapPrefix+`"`))
}

// TrapStatus asks fail2ban about the trap's jail.
func TrapStatus() JailStatus {
	return jailStatus(TrapJail, trapJailPath)
}

// UnbanTrap lets an address the trap caught back in.
func UnbanTrap(address string) error {
	return unban(TrapJail, address)
}
