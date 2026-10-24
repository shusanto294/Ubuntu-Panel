package catalog

import (
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// The PHP recipe, in one place and parameterised by version.
//
// It used to live inside the `php` cases of preSteps, packagesFor and
// configureSteps, reading the panel's default version out of the settings
// table — which was the whole of it while a machine had exactly one PHP. It
// has more than one now: the Apps page installs the default, a panel update
// adds the newest the panel knows about, and a site's PHP tab can be pointed
// at any version on the box. All three want the same packages, the same
// extensions and the same tuning, so the recipe takes the version as an
// argument and the callers decide which one they mean.

// phpExtensions are the modules every PHP this panel installs gets. They are
// named one by one rather than as `php*`, because a glob installs whatever
// else the release happens to ship and takes minutes doing it.
var phpExtensions = []string{"fpm", "cli", "common", "mysql", "pgsql", "curl", "mbstring",
	"xml", "zip", "gd", "bcmath", "intl", "redis"}

// phpOptionalExtensions are the two that are not always packaged. imagick has
// been missing from a new Ubuntu on release day more than once and soap is not
// always built, and neither is worth failing PHP over.
var phpOptionalExtensions = []string{"imagick", "soap"}

// PHPPackages is the apt line for one PHP version.
func PHPPackages(version string) string {
	names := make([]string, 0, len(phpExtensions))

	for _, extension := range phpExtensions {
		names = append(names, "php"+version+"-"+extension)
	}

	return strings.Join(names, " ")
}

// NewestPHP is the newest version the panel installs — the first of the list
// the create form and the Settings page offer, which is kept newest first.
func NewestPHP() string {
	if len(config.PHPVersions) == 0 {
		return ""
	}

	return config.PHPVersions[0]
}

// phpSource adds the step that decides where this version comes from.
//
// The ondrej PPA does not publish for every Ubuntu release, and a source that
// 404s breaks every later `apt-get update`. The distro's own packages are
// preferred; the PPA is added only when it actually publishes for this release
// — which for a version Ubuntu does not ship at all is the only way to get it.
func phpSource(list *tasks.List, version, group string) {
	list.Add(fmt.Sprintf("Choose a source for PHP %s", version), fmt.Sprintf(
		"wanted=%s; "+
			"if apt-cache policy \"php${wanted}-fpm\" 2>/dev/null | grep -q 'Candidate:' && "+
			"   ! apt-cache policy \"php${wanted}-fpm\" 2>/dev/null | grep -q 'Candidate: (none)'; then "+
			"  echo \"php${wanted}-fpm is available from the configured repositories; no PPA needed.\"; "+
			"else "+
			"  codename=$(lsb_release -cs 2>/dev/null || . /etc/os-release && echo \"$VERSION_CODENAME\"); "+
			"  if curl -fsI --max-time 15 "+
			"\"https://ppa.launchpadcontent.net/ondrej/php/ubuntu/dists/${codename}/Release\" >/dev/null; then "+
			"    echo 'Adding ppa:ondrej/php.'; sudo add-apt-repository -y ppa:ondrej/php; "+
			"    sudo DEBIAN_FRONTEND=noninteractive apt-get update -y; "+
			"  else "+
			"    echo \"ppa:ondrej/php does not publish for ${codename}; using the distro's own PHP packages.\"; "+
			"    sudo rm -f /etc/apt/sources.list.d/ondrej-ubuntu-php-*.sources "+
			"/etc/apt/sources.list.d/ondrej-ubuntu-php-*.list; "+
			"  fi; "+
			"fi", version)).Group(group)
}

// phpOptional adds the extensions that are allowed to be missing. PHP is a
// core service, so failing it over imagick makes a working install look broken.
func phpOptional(list *tasks.List, version, group string) {
	names := make([]string, 0, len(phpOptionalExtensions))

	for _, extension := range phpOptionalExtensions {
		names = append(names, "php"+version+"-"+extension)
	}

	list.Add(fmt.Sprintf("Install the optional PHP %s extensions", version),
		fmt.Sprintf("sudo DEBIAN_FRONTEND=noninteractive apt-get install -y %s || "+
			"echo \"one or more optional extensions are not packaged for this release; continuing\"",
			strings.Join(names, " "))).Optional().Group(group)
}

// phpTune writes php.ini, sizes the FPM pool to this machine and starts the
// service for one version.
func phpTune(list *tasks.List, version, group string) {
	ram := system.MemoryMB()

	// memory_limit is a ceiling per request, not an allocation — but on a
	// 1 GB machine a ceiling of 512M means one runaway page can take the
	// box with it. 256M is above anything WordPress or Laravel needs.
	limit := int64(512)
	if ram > 0 && ram < 2048 {
		limit = 256
	}

	restart := fmt.Sprintf("sudo systemctl restart php%s-fpm", version)

	list.Add(fmt.Sprintf("Tune PHP %s and start its PHP-FPM", version),
		fmt.Sprintf("sudo sed -i \"s/^upload_max_filesize = .*/upload_max_filesize = 128M/; "+
			"s/^post_max_size = .*/post_max_size = 128M/; "+
			"s/^memory_limit = .*/memory_limit = %dM/; "+
			"s/^max_execution_time = .*/max_execution_time = 300/\" /etc/php/%s/fpm/php.ini",
			limit, version),
		fmt.Sprintf("sudo systemctl enable --now php%s-fpm", version),
		restart,
		fmt.Sprintf("php%s -v", version)).Group(group)

	// Ubuntu ships pm = dynamic with five children that stay resident. At
	// 40-80 MB each for WordPress that is most of a 1 GB machine held
	// against requests that are not arriving. `ondemand` spawns per request
	// and lets workers exit again. Above 2 GB there is room to keep them
	// warm, so the default stands and only the ceiling is sized.
	if ram <= 0 {
		return
	}

	children := ram / 150 // ~60 MB a worker, 40% of RAM

	if children < 2 {
		children = 2
	}
	if children > 12 {
		children = 12
	}

	pool := fmt.Sprintf(
		"sudo sed -i \"s/^pm.max_children = .*/pm.max_children = %d/; "+
			"s/^;*pm.max_requests = .*/pm.max_requests = 500/\" /etc/php/%s/fpm/pool.d/www.conf",
		children, version)

	if ram < 2048 {
		pool = fmt.Sprintf(
			"sudo sed -i \"s/^pm = .*/pm = ondemand/; "+
				"s/^pm.max_children = .*/pm.max_children = %d/; "+
				"s/^;*pm.process_idle_timeout = .*/pm.process_idle_timeout = 10s/; "+
				"s/^;*pm.max_requests = .*/pm.max_requests = 500/\" /etc/php/%s/fpm/pool.d/www.conf",
			children, version)
	}

	// Optional and separate: a pool file this cannot match is a PHP-FPM
	// that keeps its own defaults, not one that fails to start.
	list.Add(fmt.Sprintf("Size the PHP %s pool to this machine", version), pool, restart).
		Optional().Group(group)
}

// PHPVersionSteps is the whole of putting one PHP version on the machine: the
// source, the packages, the extensions that may be missing, and a tuned
// PHP-FPM listening on /run/php/php<version>-fpm.sock.
//
// Used for a version that is not the one the Apps page installed — the newest
// one a panel update adds, and the one a site's PHP tab asks to be moved to.
// The apt install is its own step here rather than part of the shared
// transaction InstallQueued builds, because there is nothing to share it with.
func PHPVersionSteps(list *tasks.List, version, group string) {
	waitForApt(list)

	phpSource(list, version, group)

	list.Add(fmt.Sprintf("Install PHP %s", version),
		"sudo DEBIAN_FRONTEND=noninteractive apt-get update -y || "+
			"echo \"apt-get update reported errors; continuing\"",
		"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "+PHPPackages(version)).
		Group(group)

	phpOptional(list, version, group)
	phpTune(list, version, group)
}
