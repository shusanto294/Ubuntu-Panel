package security

import (
	"fmt"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The SSH trap used to ban, from the whole machine and permanently, any
// address that connected to the SSH port without being on the never-ban list.
// It was removed: one connection from an address the owner forgot to list —
// a new phone network, a home connection whose address changed — locked them
// out of their own panel. Turning SSH off closes the port, which is what the
// trap was for, without that risk.
//
// What is left is taking it off machines where it was on: a fail2ban jail and
// filter, a firewall rule logging every connection to the SSH port, and the
// bans the jail holds.
const (
	trapJail       = "ssh-trap"
	trapJailPath   = "/etc/fail2ban/jail.d/ubuntu-panel-trap.local"
	trapFilterPath = "/etc/fail2ban/filter.d/ubuntu-panel-ssh-trap.conf"
	trapPrefix     = "UP-SSH-TRAP "
)

// TrapLeftovers is whether anything of the trap is still on this machine.
func TrapLeftovers() bool {
	return proc.Exists(trapJailPath, true) || proc.Exists(trapFilterPath, true) ||
		proc.Ok("sudo iptables -S INPUT 2>/dev/null | grep -qF -- "+proc.Quote(`--log-prefix "`+trapPrefix+`"`)) ||
		proc.Ok("sudo ip6tables -S INPUT 2>/dev/null | grep -qF -- "+proc.Quote(`--log-prefix "`+trapPrefix+`"`))
}

// RemoveTrap takes the trap off the machine and lets every address it caught
// back in. Safe to run when there is nothing to remove.
func RemoveTrap() error {
	// The firewall rule first, so nothing new is caught while the rest goes.
	for _, tool := range []string{"iptables", "ip6tables"} {
		proc.Run(fmt.Sprintf(
			"sudo %[1]s -S INPUT 2>/dev/null | grep -F -- '--log-prefix \"%[2]s\"' | sed 's/^-A /-D /' | "+
				"while read -r rule; do eval \"sudo %[1]s $rule\"; done; true",
			tool, trapPrefix), time.Minute)
	}

	if !proc.Exists(trapJailPath, true) && !proc.Exists(trapFilterPath, true) {
		return nil
	}

	// Unbanned before the jail goes: fail2ban keeps a jail's bans in its
	// database and would put them back if a jail of that name ever returned.
	if proc.Which("fail2ban-client") {
		for _, address := range jailStatus(trapJail, trapJailPath).Banned {
			unban(trapJail, address)
		}
	}

	proc.Run("sudo rm -f "+trapJailPath+" "+trapFilterPath, time.Minute)

	if !proc.Which("fail2ban-client") {
		return nil
	}

	if result := proc.Run("sudo fail2ban-client reload 2>&1", 2*time.Minute); !result.OK() {
		return fmt.Errorf("fail2ban would not reload without the SSH trap: %s", lastLines(result.Output, 4))
	}

	return nil
}
