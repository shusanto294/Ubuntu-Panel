package security

import (
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// autoUpgradesPath is where apt is told to run unattended-upgrades daily. The
// same file `dpkg-reconfigure unattended-upgrades` writes, so the two agree.
const autoUpgradesPath = "/etc/apt/apt.conf.d/20auto-upgrades"

// Updates is what apt knows about patches waiting for this machine.
type Updates struct {
	// Pending is every upgradable package; Security is the part of that from
	// a -security pocket, which is the number that matters.
	Pending  int  `json:"pending"`
	Security int  `json:"security"`
	Reboot   bool `json:"reboot_required"`

	// Automatic says whether unattended-upgrades is installed and switched
	// on, which is what installs the security part without anybody asking.
	Automatic bool `json:"automatic"`
}

// ReadUpdates asks apt, from its cache. It does not refresh the cache — that
// is a network round trip per mirror, and the daily apt timer does it anyway.
func ReadUpdates() Updates {
	output := proc.Run("apt list --upgradable 2>/dev/null | grep -v '^Listing'", time.Minute).Output

	updates := Updates{}

	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		updates.Pending++

		if strings.Contains(line, "-security") {
			updates.Security++
		}
	}

	updates.Reboot = proc.Ok("test -f /var/run/reboot-required")
	updates.Automatic = AutomaticUpdates()

	return updates
}

// AutomaticUpdates is whether unattended-upgrades is installed and the apt
// timer is told to run it.
func AutomaticUpdates() bool {
	if !proc.Ok("dpkg-query -W -f='${Status}' unattended-upgrades 2>/dev/null | grep -q 'install ok installed'") {
		return false
	}

	contents, err := proc.ReadFile(autoUpgradesPath, false)
	if err != nil {
		return false
	}

	return aptPeriodic(contents, "Unattended-Upgrade") > 0
}

// aptPeriodic reads an APT::Periodic value out of an apt.conf file.
func aptPeriodic(contents, key string) int {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "APT::Periodic::"+key+" ") &&
			!strings.HasPrefix(line, "APT::Periodic::"+key+"\"") {
			continue
		}

		_, value, found := strings.Cut(line, "\"")
		if !found {
			continue
		}

		value, _, _ = strings.Cut(value, "\"")
		number, _ := strconv.Atoi(value)

		return number
	}

	return 0
}

// autoUpgradesFile switches on the daily package list refresh and the daily
// unattended upgrade. Ubuntu's default unattended-upgrades configuration only
// takes packages from the -security pocket, so this installs patches and
// nothing that changes what the machine runs.
const autoUpgradesFile = "// Managed by Ubuntu Panel: install security updates automatically.\n" +
	"APT::Periodic::Update-Package-Lists \"1\";\n" +
	"APT::Periodic::Unattended-Upgrade \"1\";\n" +
	"APT::Periodic::AutocleanInterval \"7\";\n"

// AutomaticUpdateSteps installs unattended-upgrades if it is missing and
// switches it on.
func AutomaticUpdateSteps(list *tasks.List, enable bool) {
	if !enable {
		list.AddFn("Switch automatic security updates off", func(runner *tasks.Runner) (string, error) {
			off := strings.ReplaceAll(autoUpgradesFile, "\"1\"", "\"0\"")
			off = strings.Replace(off, "install security updates automatically", "automatic updates are off", 1)

			if err := proc.PutFile(autoUpgradesPath, off, true); err != nil {
				return "", err
			}

			return "Security updates will no longer be installed without being asked.", nil
		})

		return
	}

	list.Add("Install unattended-upgrades",
		"sudo apt-get update -q",
		"sudo apt-get install -y -q unattended-upgrades")

	list.AddFn("Switch automatic security updates on", func(runner *tasks.Runner) (string, error) {
		if err := proc.PutFile(autoUpgradesPath, autoUpgradesFile, true); err != nil {
			return "", err
		}

		return "Security updates are now installed daily.", nil
	})

	list.Add("Check the configuration", "sudo unattended-upgrade --dry-run 2>&1 | tail -n 5 || true").Optional()
}

// InstallSecurityUpdateSteps installs what is waiting from the -security
// pocket now, rather than when the timer next runs.
func InstallSecurityUpdateSteps(list *tasks.List) {
	list.Add("Refresh the package lists", "sudo apt-get update -q")
	list.Add("Install security updates",
		"if command -v unattended-upgrade >/dev/null 2>&1; then "+
			"sudo unattended-upgrade -v; "+
			"else sudo apt-get install -y -q unattended-upgrades && sudo unattended-upgrade -v; fi")
	list.Add("What is left", "apt list --upgradable 2>/dev/null | grep -c -- '-security' || true").Optional()
}

// Fail2banSteps installs fail2ban, which the panel's SSH protection is.
func Fail2banSteps(list *tasks.List, configure func(runner *tasks.Runner) (string, error)) {
	list.Add("Install fail2ban",
		"sudo apt-get update -q",
		"sudo apt-get install -y -q fail2ban")

	list.AddFn("Protect SSH", configure)
}
