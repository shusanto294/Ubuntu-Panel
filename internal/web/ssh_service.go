package web

import (
	"net"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// sshUnitsSetting is what was enabled before SSH was turned off, so turning
// it on puts back socket activation or the plain service, whichever the
// machine had.
const sshUnitsSetting = "security_ssh_units"

// sshServiceProps is the switch's state for the Security page.
func (a *App) sshServiceProps(service security.SSHService) map[string]any {
	return map[string]any{
		"running": service.Running,
		"masked":  service.Masked,
	}
}

// securitySSHService turns SSH off or on. Either lasts until it is changed
// here: off survives reboots and upgrades, and on stays on.
func (a *App) securitySSHService(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	switch input["action"] {
	case "off":
		// Through an SSH tunnel the panel's own page is carried by SSH; turn
		// it off and the connection carrying this request is the next to go.
		if ip := net.ParseIP(session.ClientIP(r)); ip != nil && ip.IsLoopback() {
			a.securityBack(w, r, "You are reaching the panel through this machine itself — "+
				"probably an SSH tunnel — so turning SSH off would cut you off. Open the panel "+
				"on its public address or hostname first.", true)
			return
		}

		// Everybody logged in is disconnected too: SSH off means nobody is
		// in over SSH, not just that nobody new can get in.
		units, err := security.StopSSH(true)
		if len(units) > 0 && a.Store.Setting(sshUnitsSetting, "") == "" {
			a.Store.SetSetting(sshUnitsSetting, strings.Join(units, ","))
		}

		if err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.Log.Info("SSH turned off", "ip", session.ClientIP(r))
		a.securityBack(w, r, "SSH is off and everyone connected was disconnected. It stays "+
			"off, through reboots, until you turn it on here.", false)

	case "on":
		if err := a.startSSH(); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.Log.Info("SSH turned on", "ip", session.ClientIP(r))
		a.securityBack(w, r, "SSH is on.", false)

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
