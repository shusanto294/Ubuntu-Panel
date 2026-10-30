package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// sshTrapSetting is whether an address that connects to SSH without being on
// the never-ban list is banned from the machine. Off unless somebody turns it
// on.
const sshTrapSetting = "security_ssh_trap"

func (a *App) sshTrapEnabled() bool {
	return a.Store.SettingBool(sshTrapSetting, false)
}

// syncSSHTrap puts the rule that springs the trap in place while the trap is
// on, whatever SSH is doing. Called at start and when the trap is turned on;
// a firewall reload can flush the rule, which checkSSHTrap is for.
func (a *App) syncSSHTrap() {
	if !a.sshTrapEnabled() {
		return
	}

	a.setSSHTrapRule()
}

// checkSSHTrap is the every-minute check: a rule already in place is left as
// it is, at the cost of one iptables call and without reading sshd's ports.
func (a *App) checkSSHTrap() {
	if !a.sshTrapEnabled() || security.TrapRulePresent() {
		return
	}

	a.setSSHTrapRule()
}

func (a *App) setSSHTrapRule() {
	if err := security.SetTrapRule(true, security.SSHPorts()); err != nil {
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

// securitySSHTrap turns the trap on or off. Turned on with trust_me, the
// address doing it goes on the never-ban list first: with SSH on, the trap
// bans every address not on that list, and the operator's own is the one
// most likely to connect next.
func (a *App) securitySSHTrap(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	enabled := boolInput(input, "enabled", false)
	trusted := ""

	if enabled && boolInput(input, "trust_me", false) {
		trusted = a.trustAddress(session.ClientIP(r))
	}

	a.Store.SetSetting(sshTrapSetting, strconv.FormatBool(enabled))

	// The SSH jail is rewritten too when the never-ban list grew, and that
	// writes the trap along with it.
	if trusted != "" {
		err = a.applySSHProtection(nil)
	} else {
		err = security.ApplyTrap(enabled, a.jailConfig().IgnoreIPs)
	}

	if err != nil {
		a.Store.SetSetting(sshTrapSetting, strconv.FormatBool(!enabled))
		a.securityBack(w, r, err.Error(), true)
		return
	}

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

	message := "The SSH trap is on. Any address not on the never-ban list that connects to SSH " +
		"is banned from the whole server on its first try."
	if trusted != "" {
		message += " " + trusted + " was added to the never-ban list."
	}

	a.securityBack(w, r, message, false)
}

// trustAddress puts an address on the never-ban list and lets it out of both
// jails. It returns the address when it was added, and "" when it was not an
// address or was already there.
func (a *App) trustAddress(address string) string {
	if !security.ValidAddress(address) {
		return ""
	}

	config := a.jailConfig()

	for _, existing := range config.IgnoreIPs {
		if existing == address {
			return ""
		}
	}

	config.IgnoreIPs = append(config.IgnoreIPs, address)

	encoded, _ := json.Marshal(config)
	a.Store.SetSetting(jailSetting, string(encoded))

	security.Unban(address)
	security.UnbanTrap(address)

	return address
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
