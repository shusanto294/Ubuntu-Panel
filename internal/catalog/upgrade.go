package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// UpgradeInstalled brings what is already on this machine up to date, as one
// task, and puts the newest PHP the panel knows about on a machine that does
// not have it yet.
//
// This is what a panel update runs after it has swapped the binary. Updating
// the panel used to mean updating only the panel: a server installed a year
// ago kept the PHP, the MariaDB and the nginx of a year ago, including the
// security fixes published since, because nothing ever asked apt for them
// again. The panel is the thing that put them there, so the panel is the thing
// that keeps them current.
//
// What it does *not* do is move a site. The newest PHP is installed and
// becomes the default for sites made from now on; every site already here
// keeps the version it was built against, because a site that works on 8.3 is
// not something to silently put on 8.5 while nobody is watching. Each site's
// PHP tab moves that one, when whoever owns it decides to.
//
// Only what is installed, and only the panel's own packages: `apt-get
// upgrade` on somebody's server is a much larger promise than this panel is in
// a position to make.
func (s *Service) UpgradeInstalled(ctx context.Context, userID int64) error {
	s.SyncRows()

	// Probed rather than read off the rows: the table only learns an app is
	// there when the panel installed it, and this runs on machines that have
	// been looked after by hand. Fresh rather than unconditional, so a caller
	// that has just probed — which the installer has — is not charged for two
	// shell calls per app to be told the same thing.
	s.DetectFresh(nil, time.Minute)

	installed := []string{}

	for _, entry := range Entries {
		if s.Store.ServiceInstalled(entry.Key) {
			installed = append(installed, entry.Key)
		}
	}

	if len(installed) == 0 {
		return nil
	}

	taskID, err := s.Store.CreateTask(ctx, "provision", "services.upgrade", userID, 0,
		"Updating the server's apps")
	if err != nil {
		return err
	}

	list := &tasks.List{}

	waitForApt(list)

	list.Add("Update package lists",
		"sudo DEBIAN_FRONTEND=noninteractive apt-get update -y || "+
			"echo \"apt-get update reported errors; continuing\"")

	// Read once and passed down rather than probed inside each step builder:
	// what follows is a recipe, and a recipe that re-reads the machine between
	// its own lines is one that can disagree with itself.
	running := system.InstalledPHPVersions()

	if packages := s.upgradablePackages(installed, running); packages != "" {
		// --only-upgrade rather than a plain install: a name in this list that
		// is not on the machine is skipped with a line saying so instead of
		// being pulled in. That matters for PHP, where the list is built from
		// the versions found on disk, and for an entry whose packages a
		// release renamed.
		//
		// Node stays on the major it is on. The NodeSource repository is
		// pinned to one, and moving a machine between majors underneath a
		// Next.js site that was built against the old one is a deployment, not
		// an update.
		list.Add("Upgrade the installed packages",
			"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade "+packages)
	}

	s.newestPHPSteps(ctx, list, installed, running)

	for _, key := range installed {
		s.upgradeSteps(list, key)
	}

	list.Add("Remove what nothing needs any more",
		"sudo DEBIAN_FRONTEND=noninteractive apt-get autoremove -y").Optional()

	ok := tasks.New(s.Store, taskID).Run(list)

	// The versions on the Apps page are read from the machine, so they are
	// only right once something has looked again.
	s.Detect(nil)

	if !ok {
		return errors.New("part of the update did not succeed")
	}

	return nil
}

// upgradablePackages is one apt line for everything installed that came from
// apt.
//
// PHP is the exception: its packages are named per version, and a machine can
// have several — the one the Apps page installed, the newest a previous update
// added, and whatever a site was moved to. Every one of them is upgraded,
// because every one of them is serving something.
func (s *Service) upgradablePackages(installed, running []string) string {
	seen := map[string]bool{}
	names := []string{}

	add := func(line string) {
		for _, name := range strings.Fields(line) {
			if name == "" || seen[name] {
				continue
			}

			seen[name] = true
			names = append(names, name)
		}
	}

	for _, key := range installed {
		if key == "php" {
			for _, version := range running {
				add(PHPPackages(version))
			}

			// A PHP whose FPM socket is not up yet still has packages worth
			// upgrading, and the default is the one it would be.
			if len(running) == 0 {
				add(PHPPackages(s.Store.PHPVersion()))
			}

			continue
		}

		add(s.packagesFor(key))
	}

	return strings.Join(names, " ")
}

// newestPHPSteps installs the newest PHP the panel knows about, when PHP is on
// this machine and that version is not.
//
// The packages, the extensions and a tuned PHP-FPM — the same recipe the Apps
// page runs — and then the default for new sites, which is the only thing
// about an existing server this changes.
func (s *Service) newestPHPSteps(ctx context.Context, list *tasks.List,
	installed, running []string) {
	php := false

	for _, key := range installed {
		if key == "php" {
			php = true
		}
	}

	newest := NewestPHP()

	if !php || newest == "" {
		return
	}

	for _, version := range running {
		if version == newest {
			// Already here. The apt step above has it at its latest patch.
			return
		}
	}

	previous := s.Store.PHPVersion()

	PHPVersionSteps(list, newest, "php")

	list.AddFn(fmt.Sprintf("Make PHP %s the default for new sites", newest),
		func(runner *tasks.Runner) (string, error) {
			if !proc.Ok("test -S /run/php/php" + newest + "-fpm.sock") {
				return "", fmt.Errorf("PHP %s did not come up, so the default stays at PHP %s",
					newest, previous)
			}

			s.Store.SetSetting("php_version", newest)

			return fmt.Sprintf("New sites get PHP %s. The %d site(s) already here keep the "+
				"version they were built against — the PHP tab on each one moves it.",
				newest, s.Store.SiteCount(ctx)), nil
		}).Optional().Group("php")
}

// upgradeSteps are the updates that do not go through apt, for the apps whose
// install did not either.
//
// phpMyAdmin and Roundcube are not here: the panel installs a pinned tarball of
// each, so "update" would mean re-downloading the same version. They move when
// the panel's own pin does, which is a release of the panel and an Apps page
// reinstall.
func (s *Service) upgradeSteps(list *tasks.List, key string) {
	switch key {
	case "composer":
		list.Add("Update Composer",
			"sudo composer self-update --no-interaction 2>&1 || "+
				"echo \"Composer could not update itself; the installed version stays\"",
			"composer --version").Optional().Group(key)

	case "wpcli":
		list.Add("Update WP-CLI",
			"sudo wp cli update --yes --allow-root 2>&1 || "+
				"echo \"WP-CLI could not update itself; the installed version stays\"",
			"wp --version --allow-root").Optional().Group(key)

	case "pm2":
		list.Add("Update PM2",
			"sudo npm install -g pm2@latest 2>&1 || "+
				"echo \"PM2 could not be updated; the installed version stays\"",
			"pm2 --version | tail -1").Optional().Group(key)
	}
}

// UpgradeSummary is what was on the machine afterwards, for the installer to
// print. Shaped like a line per app rather than a report, because it is read
// scrolling past.
func (s *Service) UpgradeSummary(ctx context.Context) []string {
	rows, err := s.Store.Services(ctx)
	if err != nil {
		return nil
	}

	lines := []string{}

	for _, entry := range Entries {
		row := rows[entry.Key]

		if row == nil || row.Status != store.ServiceInstalled {
			continue
		}

		version := strings.TrimSpace(row.Version)
		if version == "" {
			version = "installed"
		}

		lines = append(lines, fmt.Sprintf("%s: %s", entry.Label, version))
	}

	return lines
}
