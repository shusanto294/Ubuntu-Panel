package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// sshTrapSetting is whether an address trying SSH while it is off is banned
// from the machine. Off unless somebody turns it on.
const sshTrapSetting = "security_ssh_trap"

func (a *App) sshTrapEnabled() bool {
	return a.Store.SettingBool(sshTrapSetting, false)
}

// syncSSHTrap makes the firewall agree with SSH: the rule that springs the
// trap is there only while SSH is off. Called when SSH changes, at start, and
// every minute — a firewall reload can flush the rule, and a check that finds
// it in place costs one iptables call. Nothing at all while the trap is off.
func (a *App) syncSSHTrap() {
	if !a.sshTrapEnabled() {
		return
	}

	a.setSSHTrapRule(!security.SSHRunning())
}

// checkSSHTrap is the every-minute version: a rule already in place while SSH
// is off is left as it is, without reading sshd's ports again.
func (a *App) checkSSHTrap() {
	if !a.sshTrapEnabled() {
		return
	}

	off := !security.SSHRunning()

	if off == security.TrapRulePresent() {
		return
	}

	a.setSSHTrapRule(off)
}

func (a *App) setSSHTrapRule(on bool) {
	if err := security.SetTrapRule(on, security.SSHPorts()); err != nil {
		a.Log.Error("could not set the SSH trap's firewall rule", "error", err)
	}
}

// EnsureSSHTrap rewrites the trap's jail at start when it is not what the
// panel would write — after an update that changed it, or a never-ban list
// changed while fail2ban was not there — and puts the rule back.
func (a *App) EnsureSSHTrap(ctx context.Context) {
	if !a.sshTrapEnabled() {
		return
	}

	ignore := a.jailConfig().IgnoreIPs

	if !security.TrapFilesCurrent(ignore) {
		if err := security.ApplyTrap(true, ignore); err != nil {
			a.Log.Error("could not apply the SSH trap", "error", err)
			return
		}
	}

	a.syncSSHTrap()
}

// securitySSHTrap turns the trap on or off.
func (a *App) securitySSHTrap(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	enabled := boolInput(input, "enabled", false)

	if err := security.ApplyTrap(enabled, a.jailConfig().IgnoreIPs); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Store.SetSetting(sshTrapSetting, strconv.FormatBool(enabled))

	if enabled {
		a.syncSSHTrap()
	} else {
		security.SetTrapRule(false, nil)
	}

	a.Log.Info("SSH trap changed", "enabled", enabled, "ip", session.ClientIP(r))

	if !enabled {
		a.securityBack(w, r, "The SSH trap is off, and the addresses it caught can reach "+
			"the server again.", false)
		return
	}

	message := "The SSH trap is on. While SSH is off, an address that tries to connect is " +
		"banned from the whole server on its first try."
	if security.SSHRunning() {
		message += " SSH is on right now, so it waits until you turn SSH off."
	}

	a.securityBack(w, r, message, false)
}

// securitySSHTrapUnban lets an address the trap caught back in.
func (a *App) securitySSHTrapUnban(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := input["ip"]

	if err := security.UnbanTrap(address); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("unbanned from the SSH trap", "ip", address)
	a.securityBack(w, r, address+" can reach the server again.", false)
}
