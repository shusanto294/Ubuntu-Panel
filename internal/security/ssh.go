package security

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// sshdDropIn is the panel's SSH configuration.
//
// `00-` because sshd keeps the first value it reads for each keyword, and the
// drop-in directory is read in name order before the main file. Cloud images
// ship a 50-cloud-init.conf that turns password logins on; a file that sorts
// after it would be ignored for exactly the setting that matters most.
const sshdDropIn = "/etc/ssh/sshd_config.d/00-ubuntu-panel.conf"

// SSHConfig is the part of sshd's configuration the panel manages.
type SSHConfig struct {
	// PermitRootLogin is yes, prohibit-password (keys only) or no.
	PermitRootLogin string `json:"permit_root_login"`

	// PasswordAuthentication off means keys only, for everybody.
	PasswordAuthentication bool `json:"password_authentication"`

	// MaxAuthTries is how many attempts one connection gets before sshd drops
	// it. Separate from fail2ban, which counts across connections.
	MaxAuthTries int `json:"max_auth_tries"`
}

// SSHState is what sshd is actually running with, which may differ from
// anything the panel wrote: the effective value is the first one sshd read.
type SSHState struct {
	SSHConfig

	Ports   []string `json:"ports"`
	Managed bool     `json:"managed"`

	// AuthorizedKeys are the accounts that can log in with a key. Turning
	// passwords off with none of these is locking the door with the key on
	// the inside.
	AuthorizedKeys []string `json:"authorized_keys"`

	Readable bool `json:"readable"`

	Service SSHService `json:"service"`
}

// ReadSSH asks sshd for its effective configuration. `sshd -T` is the only
// honest answer: it has resolved every include, every drop-in and every
// default, which reading the files would have to reimplement.
func ReadSSH() SSHState {
	state := SSHState{Ports: []string{}, AuthorizedKeys: []string{}, Service: ReadSSHService()}

	result := proc.Run("sudo sshd -T 2>/dev/null", 30*time.Second)
	if !result.OK() {
		return state
	}

	state.Readable = true
	state.Managed = proc.Exists(sshdDropIn, true)

	for _, line := range strings.Split(result.Output, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")

		switch key {
		case "port":
			state.Ports = append(state.Ports, value)
		case "permitrootlogin":
			// sshd -T prints the older spelling of prohibit-password.
			if value == "without-password" {
				value = "prohibit-password"
			}
			state.PermitRootLogin = value
		case "passwordauthentication":
			state.PasswordAuthentication = value == "yes"
		case "maxauthtries":
			state.MaxAuthTries, _ = strconv.Atoi(value)
		}
	}

	state.AuthorizedKeys = authorizedKeys()

	return state
}

// SSHPorts is just the ports, which the fail2ban jail needs.
func SSHPorts() []string {
	ports := ReadSSH().Ports
	if len(ports) == 0 {
		return []string{"ssh"}
	}

	return ports
}

// authorizedKeys lists the accounts with at least one key installed.
func authorizedKeys() []string {
	output := proc.Run("sudo sh -c 'for f in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys; do "+
		"[ -s \"$f\" ] && grep -qE \"^(ssh-|ecdsa-|sk-)\" \"$f\" && echo \"$f\"; done' 2>/dev/null",
		30*time.Second).Output

	accounts := []string{}

	for _, path := range strings.Fields(output) {
		switch {
		case strings.HasPrefix(path, "/root/"):
			accounts = append(accounts, "root")
		case strings.HasPrefix(path, "/home/"):
			name, _, _ := strings.Cut(strings.TrimPrefix(path, "/home/"), "/")
			accounts = append(accounts, name)
		}
	}

	return accounts
}

// Validate is what the form checks.
func (c SSHConfig) Validate(state SSHState) map[string]string {
	problems := map[string]string{}

	switch c.PermitRootLogin {
	case "yes", "prohibit-password", "no":
	default:
		problems["permit_root_login"] = "Choose yes, keys only, or no."
	}

	if c.MaxAuthTries < 1 || c.MaxAuthTries > 10 {
		problems["max_auth_tries"] = "Allow between 1 and 10 attempts per connection."
	}

	if !c.PasswordAuthentication && len(state.AuthorizedKeys) == 0 {
		problems["password_authentication"] = "No account on this machine has an SSH key " +
			"installed, so turning passwords off would lock everybody out. Add a key to " +
			"~/.ssh/authorized_keys first."
	}

	return problems
}

// SSHDropIn renders the panel's sshd configuration.
func SSHDropIn(config SSHConfig) string {
	password := "no"
	if config.PasswordAuthentication {
		password = "yes"
	}

	return "# Managed by Ubuntu Panel — change these from the Security page.\n" +
		"# Read before every other file in this directory, so these values win.\n" +
		"PermitRootLogin " + config.PermitRootLogin + "\n" +
		"PasswordAuthentication " + password + "\n" +
		"KbdInteractiveAuthentication " + password + "\n" +
		"MaxAuthTries " + strconv.Itoa(config.MaxAuthTries) + "\n" +
		"PermitEmptyPasswords no\n" +
		"LoginGraceTime 30\n"
}

// ApplySSH writes the configuration and reloads sshd, and only reloads it once
// `sshd -t` has accepted the result. A reload of a configuration sshd cannot
// parse leaves the running daemon as it was — but the next restart, or the
// next boot, would not start it at all, and that is a machine nobody can log
// in to.
//
// A reload rather than a restart: existing sessions, including the one the
// operator may be watching this from, are left alone.
func ApplySSH(config SSHConfig) error {
	previous, _ := proc.ReadFile(sshdDropIn, true)

	if !proc.Ok("sudo test -d /etc/ssh/sshd_config.d && sudo grep -qiE '^\\s*Include\\s+/etc/ssh/sshd_config.d' /etc/ssh/sshd_config") {
		return errors.New("this machine's sshd_config does not include /etc/ssh/sshd_config.d, " +
			"so the panel has nowhere to write its settings without editing the main file")
	}

	if err := proc.PutFile(sshdDropIn, SSHDropIn(config), true); err != nil {
		return err
	}

	if check := proc.Run("sudo sshd -t 2>&1", 30*time.Second); !check.OK() {
		if previous != "" {
			proc.PutFile(sshdDropIn, previous, true)
		} else {
			proc.Run("sudo rm -f "+sshdDropIn, time.Minute)
		}

		return fmt.Errorf("sshd refused the configuration, so it was not applied: %s",
			lastLines(check.Output, 4))
	}

	// SSH turned off: the file is read when it is turned back on.
	if !SSHRunning() {
		return nil
	}

	if result := proc.Run("sudo systemctl try-reload-or-restart ssh 2>/dev/null || sudo systemctl try-reload-or-restart sshd",
		time.Minute); !result.OK() {
		return fmt.Errorf("the configuration is valid but sshd would not reload: %s",
			lastLines(result.Output, 3))
	}

	return nil
}

// sshUnits are the systemd units that can accept an SSH connection. Ubuntu
// 22.10 and later start sshd from ssh.socket when somebody connects; earlier
// releases keep ssh.service running. Either one listening means SSH is on.
var sshUnits = []string{"ssh.socket", "ssh.service"}

// SSHService is whether anything is listening for SSH, and what starts at boot.
type SSHService struct {
	Running bool `json:"running"`
	Masked  bool `json:"masked"`

	// Enabled are the units that start at boot, which is what turning SSH
	// back on restores.
	Enabled []string `json:"enabled"`
}

// ReadSSHService asks systemd, one unit at a time: a unit that does not exist
// on this release would make a combined query fail for both.
func ReadSSHService() SSHService {
	service := SSHService{Enabled: []string{}}

	for _, unit := range sshUnits {
		if proc.Ok("systemctl is-active --quiet " + unit) {
			service.Running = true
		}

		switch strings.TrimSpace(proc.Run("systemctl is-enabled "+unit+" 2>/dev/null", 30*time.Second).Output) {
		case "enabled", "enabled-runtime", "static", "indirect":
			service.Enabled = append(service.Enabled, unit)
		case "masked", "masked-runtime":
			service.Masked = true
		}
	}

	return service
}

// SSHRunning is whether a reload has anything to reload.
func SSHRunning() bool {
	return ReadSSHService().Running
}

// StopSSH turns SSH off: nothing listens on the port, and nothing will start
// it again — not a reboot, not socket activation, not an openssh-server
// upgrade re-enabling its units, which is why the units are masked as well as
// disabled.
//
// Sessions already open survive the daemon stopping (the units kill only the
// listener), unless endSessions is set. It returns the units that were
// enabled, for StartSSH to put back.
func StopSSH(endSessions bool) ([]string, error) {
	before := ReadSSHService()

	for _, unit := range sshUnits {
		proc.Run("sudo systemctl disable --now "+unit+" 2>&1", time.Minute)
	}

	for _, unit := range sshUnits {
		if proc.Ok("systemctl cat " + unit + " >/dev/null 2>&1") {
			if result := proc.Run("sudo systemctl mask "+unit+" 2>&1", time.Minute); !result.OK() {
				return before.Enabled, fmt.Errorf("could not mask %s: %s", unit, lastLines(result.Output, 3))
			}
		}
	}

	if SSHRunning() {
		return before.Enabled, errors.New("sshd is still running after being stopped")
	}

	if endSessions {
		// With the listener gone, every sshd left is somebody's session.
		proc.Run("sudo pkill -x sshd-session; sudo pkill -x sshd; true", time.Minute)
	}

	return before.Enabled, nil
}

// StartSSH turns SSH back on with the units that were enabled before it was
// turned off, or ssh.service — which every release has — when that is not
// known.
func StartSSH(units []string) error {
	if len(units) == 0 {
		units = []string{"ssh.service"}
	}

	for _, unit := range sshUnits {
		proc.Run("sudo systemctl unmask "+unit+" 2>&1", time.Minute)
	}

	// A configuration sshd cannot parse would not start, and would say so
	// only in the journal.
	if check := proc.Run("sudo sshd -t 2>&1", 30*time.Second); !check.OK() {
		return fmt.Errorf("sshd's configuration is broken, so it cannot start: %s", lastLines(check.Output, 4))
	}

	for _, unit := range units {
		if !validUnit(unit) {
			continue
		}

		if result := proc.Run("sudo systemctl enable --now "+unit+" 2>&1", time.Minute); !result.OK() {
			return fmt.Errorf("%s would not start: %s", unit, lastLines(result.Output, 3))
		}
	}

	if !SSHRunning() {
		return errors.New("sshd did not start")
	}

	return nil
}

func validUnit(unit string) bool {
	for _, known := range sshUnits {
		if unit == known {
			return true
		}
	}

	return false
}
