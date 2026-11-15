package catalog

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// installed marks an app as present without touching the machine.
func installed(t *testing.T, service *Service, keys ...string) {
	t.Helper()

	for _, key := range keys {
		service.Store.SetServiceStatus(key, store.ServiceInstalled, "test", "")
	}
}

// The update asks apt for the newest of what the panel installed, and nothing
// else: a server's other packages are the operator's business.
func TestUpgradablePackagesCoversOnlyWhatIsInstalled(t *testing.T) {
	service := testService(t)
	installed(t, service, "nginx", "mysql")

	line := service.upgradablePackages([]string{"nginx", "mysql"}, nil)

	for _, want := range []string{"nginx", "mariadb-server"} {
		if !strings.Contains(line, want) {
			t.Errorf("%s is missing from %q", want, line)
		}
	}

	// Redis is in the catalogue and not on this machine, so it is not asked
	// for — `--only-upgrade` would skip it, but naming it would still be the
	// update claiming to look after something it never installed.
	if strings.Contains(line, "redis") {
		t.Errorf("an app that is not installed was named: %q", line)
	}
}

// A name asked for twice is asked for once. Several entries share packages —
// and PHP's list is built per version, so a machine with two of them would
// otherwise repeat every shared name.
func TestUpgradablePackagesDeduplicates(t *testing.T) {
	service := testService(t)

	line := service.upgradablePackages([]string{"nginx", "nginx", "certbot"}, nil)

	seen := map[string]int{}
	for _, name := range strings.Fields(line) {
		seen[name]++
	}

	for name, count := range seen {
		if count > 1 {
			t.Errorf("%s appears %d times in %q", name, count, line)
		}
	}
}

// PHP's packages are named per version, and a machine with no FPM socket yet
// still has a default worth upgrading — otherwise the one app the panel is
// most often asked about would be left out of its own update.
func TestUpgradablePackagesFallsBackToTheDefaultPHP(t *testing.T) {
	service := testService(t)
	service.Store.SetSetting("php_version", "8.2")

	line := service.upgradablePackages([]string{"php"}, nil)

	if !strings.Contains(line, "php8.2-fpm") {
		t.Errorf("the default PHP was not named: %q", line)
	}
}

// Nothing of the panel's on the machine is nothing to update. A box somebody
// set up by hand is theirs, and apt on it is not the panel's to run.
func TestUpgradeInstalledDoesNothingOnAMachineThePanelHasNotTouched(t *testing.T) {
	service := testService(t)

	if line := service.upgradablePackages(nil, nil); line != "" {
		t.Errorf("expected no packages, got %q", line)
	}
}

// A machine with two PHPs has both of them upgraded. Each one is serving
// something — the version the Apps page installed, the newest a previous
// update added, and whatever a site was moved to — so leaving either on last
// year's patch is leaving a site on it.
func TestUpgradablePackagesCoversEveryPHPOnTheMachine(t *testing.T) {
	service := testService(t)
	service.Store.SetSetting("php_version", "8.3")

	line := service.upgradablePackages([]string{"php"}, []string{"8.4", "8.2"})

	for _, want := range []string{"php8.4-fpm", "php8.2-fpm"} {
		if !strings.Contains(line, want) {
			t.Errorf("%s is missing from %q", want, line)
		}
	}
}

// An update makes the newest PHP the default for new sites and installs
// nothing: the first site made on it installs it.
func TestUpgradeOffersTheNewestPHPWithoutInstallingIt(t *testing.T) {
	service := testService(t)
	service.Store.SetSetting("php_version", "8.1")

	service.offerNewestPHP([]string{"php", "nginx"})

	if got := service.Store.PHPVersion(); got != NewestPHP() {
		t.Errorf("default PHP = %s, want %s", got, NewestPHP())
	}
}

// A machine without PHP is not told it has a default for it.
func TestUpgradeLeavesThePHPDefaultWithoutPHP(t *testing.T) {
	service := testService(t)
	service.Store.SetSetting("php_version", "8.1")

	service.offerNewestPHP([]string{"nginx"})

	if got := service.Store.PHPVersion(); got != "8.1" {
		t.Errorf("default PHP = %s, want 8.1", got)
	}
}
