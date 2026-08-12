package web

import (
	"context"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
)

// bootstrapSetting is how the panel remembers it has done this. It is a
// setting rather than "are any apps installed?" so that removing every app
// afterwards is a decision the panel respects instead of undoing on the next
// restart.
const bootstrapSetting = "apps_bootstrapped"

// EnsureMailConfigured sets the mail server up when it needs it, without
// anybody asking.
//
// Two occasions: its packages are on the machine but nothing has configured
// them, and the panel has been updated since the configuration was written. The
// second is the one that used to need a button — an update that changes what
// Postfix or Dovecot expects leaves the files on disk describing the old
// arrangement, and the symptom is a webmail that cannot send with a server that
// is working perfectly.
func (a *App) EnsureMailConfigured() {
	if !a.Mail.NeedsConfiguring() {
		return
	}

	if _, err := a.Queue.PushID(queue.MailConfigure, "user", 0); err != nil {
		a.Log.Error("could not queue the mail configuration", "error", err)

		return
	}

	a.Log.Info("configuring the mail server", "reason",
		"its configuration was written by a different version of the panel, or not at all")
}

// BootstrapApps installs the software a server needs, on a panel that has never
// installed any.
//
// A control panel whose first screen is a list of things you have to install
// before you can do anything is a control panel that has handed the work back.
// This queues them on the first start and lets the ordinary installer run:
// same task, same console, same page — the operator opens Apps and watches it
// happen rather than starting it.
//
// PANEL_INSTALL_APPS decides how much: `all` is the catalogue, `core` is what
// no site can run without, `none` is nothing.
func (a *App) BootstrapApps(ctx context.Context) {
	choice := a.Config.InstallApps

	if choice == "" {
		choice = "all"
	}

	if choice == "none" || a.Store.SettingBool(bootstrapSetting, false) {
		return
	}

	// Only ever on a machine the panel has not installed anything on. An
	// operator who removed MongoDB is not asking for it back every restart.
	if len(a.Store.InstalledServiceKeys(ctx)) > 0 || a.Store.ServicesPending() {
		a.Store.SetSetting(bootstrapSetting, "1")

		return
	}

	keys := []string{}

	for _, entry := range catalog.Entries {
		if choice == "all" || entry.Core {
			keys = append(keys, entry.Key)
		}
	}

	queued := a.Catalog.Queue(keys, false)

	if queued == 0 {
		a.Store.SetSetting(bootstrapSetting, "1")

		return
	}

	// The install belongs to whoever set the panel up. On a first start nobody
	// has an account yet, so it belongs to nobody and the task simply has no
	// owner — which is right: it is the panel installing its own dependencies.
	if _, err := a.Queue.PushID(queue.ServicesInstall, "user", 0); err != nil {
		a.Log.Error("could not queue the first-run install", "error", err)

		return
	}

	a.Store.SetSetting(bootstrapSetting, "1")

	a.Log.Info("installing the apps a server needs", "count", queued, "selection", choice,
		"watch", "the Apps page, or journalctl -u ubuntu-panel -f")
}

// EnsurePanelVhostCurrent rewrites the panel's own nginx vhost when it is
// missing something the panel has since learned to serve.
//
// phpMyAdmin is why. Its location lives in the panel's vhost, and a machine
// whose vhost was written before the panel emitted that block has no route to
// it — nginx passes /phpmyadmin straight to the panel, which answers "No route
// matches this URL", which is exactly what it is. Installing phpMyAdmin
// rewrites the vhost, but that is no help to a machine where it was installed
// long ago: the button appears, and the page behind it is the panel's own 404.
//
// So it is checked on every start instead. Nothing is rewritten unless the
// block is genuinely absent, the file is one the panel or its installer wrote,
// and nginx accepts the result — and the old file goes back if it does not.
func (a *App) EnsurePanelVhostCurrent() {
	if !a.Store.ServiceInstalled("phpmyadmin") {
		return
	}

	current := proc.Run("sudo cat /etc/nginx/sites-available/ubuntu-panel 2>/dev/null",
		time.Minute).Output

	if strings.Contains(current, sites.PhpMyAdminPath) {
		return
	}

	if err := a.refreshPanelVhost(nil); err != nil {
		a.Log.Warn("could not add phpMyAdmin to the panel's nginx vhost", "error", err)

		return
	}

	a.Log.Info("the panel's nginx vhost now serves phpMyAdmin at " + sites.PhpMyAdminPath)
}
