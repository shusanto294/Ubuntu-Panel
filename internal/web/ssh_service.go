package web

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// Settings for SSH turned off.
const (
	// sshUnitsSetting is what was enabled before SSH was turned off, so
	// turning it on puts back socket activation or the plain service,
	// whichever the machine had.
	sshUnitsSetting = "security_ssh_units"

	// sshCloseAtSetting is when SSH, opened for a while, turns itself off.
	sshCloseAtSetting = "security_ssh_close_at"
)

// sshOpenFor is how long the panel will open SSH for, at most.
const sshOpenFor = 24 * time.Hour

// sshServiceProps is the switch's state for the Security page.
func (a *App) sshServiceProps(service security.SSHService) map[string]any {
	closeAt, _ := strconv.ParseInt(a.Store.Setting(sshCloseAtSetting, ""), 10, 64)
	if !service.Running {
		closeAt = 0
	}

	return map[string]any{
		"running":  service.Running,
		"masked":   service.Masked,
		"close_at": closeAt,
	}
}

// securitySSHService turns SSH off, on, or on for a while.
func (a *App) securitySSHService(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	switch input["action"] {
	case "off":
		// Through an SSH tunnel the panel's own page is carried by SSH; turn
		// it off and the next dropped connection is the last one.
		if ip := net.ParseIP(session.ClientIP(r)); ip != nil && ip.IsLoopback() {
			a.securityBack(w, r, "You are reaching the panel through this machine itself — "+
				"probably an SSH tunnel — so turning SSH off could cut you off. Open the panel "+
				"on its public address or hostname first.", true)
			return
		}

		units, err := security.StopSSH(boolInput(input, "end_sessions", false))
		if len(units) > 0 && a.Store.Setting(sshUnitsSetting, "") == "" {
			a.Store.SetSetting(sshUnitsSetting, strings.Join(units, ","))
		}
		a.Store.ForgetSetting(sshCloseAtSetting)

		if err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.Log.Info("SSH turned off", "ip", session.ClientIP(r))
		a.securityBack(w, r, "SSH is off. Nothing listens on its port, and it stays off "+
			"after a reboot until you turn it on here.", false)

	case "on", "open":
		var closeAt time.Time

		if input["action"] == "open" {
			minutes := time.Duration(intInput(input, "minutes", 60)) * time.Minute
			if minutes < 5*time.Minute || minutes > sshOpenFor {
				a.securityBack(w, r, "Open SSH for between 5 minutes and 24 hours.", true)
				return
			}
			closeAt = time.Now().Add(minutes)
		}

		if err := a.startSSH(); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		if closeAt.IsZero() {
			a.Store.ForgetSetting(sshCloseAtSetting)
			a.Log.Info("SSH turned on", "ip", session.ClientIP(r))
			a.securityBack(w, r, "SSH is on.", false)
			return
		}

		a.Store.SetSetting(sshCloseAtSetting, strconv.FormatInt(closeAt.Unix(), 10))
		a.Log.Info("SSH opened for a while", "ip", session.ClientIP(r), "until", closeAt)
		a.securityBack(w, r, "SSH is on until "+closeAt.Format("15:04")+
			". Sessions you open before then stay connected after it closes.", false)

	default:
		inertia.Back(w, r, "/security")
	}
}

func (a *App) startSSH() error {
	var units []string
	if saved := a.Store.Setting(sshUnitsSetting, ""); saved != "" {
		units = strings.Split(saved, ",")
	}

	if err := security.StartSSH(units); err != nil {
		return err
	}

	a.Store.ForgetSetting(sshUnitsSetting)

	return nil
}

// closeSSHWhenDue turns SSH back off once the time it was opened for is up.
// Checked every minute, from the setting rather than a timer, so a restart of
// the panel in between does not leave it open.
func (a *App) closeSSHWhenDue(ctx context.Context) {
	closeAt, err := strconv.ParseInt(a.Store.Setting(sshCloseAtSetting, ""), 10, 64)
	if err != nil || closeAt == 0 || time.Now().Unix() < closeAt {
		return
	}

	units, err := security.StopSSH(false)
	if len(units) > 0 && a.Store.Setting(sshUnitsSetting, "") == "" {
		a.Store.SetSetting(sshUnitsSetting, strings.Join(units, ","))
	}

	if err != nil {
		// Left in place, so the next minute tries again.
		a.Log.Error("could not turn SSH back off", "error", err)
		return
	}

	a.Store.ForgetSetting(sshCloseAtSetting)
	securityCache.forget()
	a.Log.Info("SSH turned back off; the time it was opened for is up")
}
