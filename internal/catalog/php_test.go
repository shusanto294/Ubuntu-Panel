package catalog

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

func testService(t *testing.T) *Service {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return New(st)
}

// stepText is every command and step name in a list, as one string to search.
func stepText(list *tasks.List) string {
	var text strings.Builder

	for _, step := range list.Steps() {
		text.WriteString(step.Name)
		text.WriteString("\n")

		for _, command := range step.Commands {
			text.WriteString(command)
			text.WriteString("\n")
		}
	}

	return text.String()
}

// Every extension the panel installs is named for the version asked for. A
// bare `php*` would be a different and much larger install.
func TestPHPPackagesNamesEveryExtensionForTheVersionGiven(t *testing.T) {
	line := PHPPackages("8.4")

	for _, extension := range phpExtensions {
		if !strings.Contains(line, "php8.4-"+extension) {
			t.Errorf("php8.4-%s is missing from %q", extension, line)
		}
	}

	if strings.Contains(line, "php8.3-") {
		t.Errorf("the line names another version as well: %q", line)
	}
}

// The newest version is the one the create form and the Settings page show
// first, and it has to be one the validators accept — a panel update installs
// it and then makes it the default for new sites.
func TestNewestPHPIsAVersionThePanelAccepts(t *testing.T) {
	newest := NewestPHP()

	if newest == "" {
		t.Fatal("the panel offers no PHP versions at all")
	}

	if newest != config.PHPVersions[0] {
		t.Errorf("NewestPHP() = %q, want the first of %v", newest, config.PHPVersions)
	}

	if !config.IsPHPVersion(newest) {
		t.Errorf("%q is not a version IsPHPVersion accepts", newest)
	}
}

// The whole of what the refactor had to keep true: the recipe installs the
// version it is handed, not whichever one the settings table calls the default.
//
// They used to be the same thing, because a machine had one PHP. A site's PHP
// tab can now ask for another, and a step that quietly installed the default
// instead would leave the site pointed at a socket that is not there — which
// is a 502 on every request.
func TestPHPVersionStepsInstallTheVersionAskedFor(t *testing.T) {
	service := testService(t)
	service.Store.SetSetting("php_version", "8.1")

	list := &tasks.List{}
	PHPVersionSteps(list, "8.5", "")

	text := stepText(list)

	for _, want := range []string{"php8.5-fpm", "php8.5-imagick", "/etc/php/8.5/fpm/php.ini",
		"systemctl enable --now php8.5-fpm"} {
		if !strings.Contains(text, want) {
			t.Errorf("the steps never mention %s", want)
		}
	}

	if strings.Contains(text, "8.1") {
		t.Errorf("the steps name the panel's default instead of the version asked for:\n%s", text)
	}
}

// Optional where it has to be: imagick and soap are not packaged for every
// release, and PHP is a core app — failing it over an extension makes a
// working install look broken.
func TestPHPOptionalExtensionsAreAllowedToBeMissing(t *testing.T) {
	list := &tasks.List{}
	phpOptional(list, "8.4", "php")

	steps := list.Steps()

	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}

	if !steps[0].Optional {
		t.Error("the optional extensions step is not optional")
	}

	for _, extension := range phpOptionalExtensions {
		if !strings.Contains(strings.Join(steps[0].Commands, " "), "php8.4-"+extension) {
			t.Errorf("php8.4-%s is missing from the optional step", extension)
		}
	}
}
