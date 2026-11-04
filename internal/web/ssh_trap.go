package web

import (
	"context"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
)

// sshTrapSetting was whether the SSH trap was on. The trap is gone (see
// security/trap.go); the setting is read once more, at start, to know a
// machine had it, and then deleted.
const sshTrapSetting = "security_ssh_trap"

// RemoveSSHTrap takes the SSH trap off a machine an earlier version turned it
// on for: its firewall rule, its fail2ban jail, and its bans — so whoever it
// caught, the owner included, can reach the server again. A machine that
// never had it costs a few checks at start.
func (a *App) RemoveSSHTrap(ctx context.Context) {
	wasOn := a.Store.SettingBool(sshTrapSetting, false)

	if !wasOn && !security.TrapLeftovers() {
		return
	}

	if err := security.RemoveTrap(); err != nil {
		a.Log.Error("could not remove the SSH trap", "error", err)
		return
	}

	a.Store.SetSetting(sshTrapSetting, "false")
	securityCache.forget()

	a.Log.Info("SSH trap removed; the addresses it banned can reach the server again")
}
