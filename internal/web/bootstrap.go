package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/catalog"
	"github.com/shusanto294/Ubuntu-Panel/internal/databases"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
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
// them, and the panel has since changed what it writes into those files. The
// second is the one that used to need a button — a change to what Postfix or
// Dovecot is told leaves the files on disk describing the old arrangement, and
// the symptom is a webmail that cannot send with a server that is working
// perfectly.
//
// An ordinary update is not one of those occasions. It used to be, because the
// comparison was against the panel's version number, which moves on every
// release: every update restarted the mail server and wrote a task nobody had
// asked for. The comparison is now against mail.ConfigRevision, which moves
// when the mail configuration does.
func (a *App) EnsureMailConfigured() {
	if !a.Mail.NeedsConfiguring() {
		return
	}

	if _, err := a.Queue.PushID(queue.MailConfigure, "user", 0); err != nil {
		a.Log.Error("could not queue the mail configuration", "error", err)

		return
	}

	a.Log.Info("configuring the mail server", "reason",
		"nothing has configured it, or the panel now writes it differently")
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
// On a machine set up by install.sh this finds the work already done:
// InstallAppsNow runs before the service is started, so the panel is finished
// the first time anybody signs in. This stays for every other way the panel
// gets started — a binary run by hand, a container, a machine whose first
// install was interrupted before the apps step.
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

// EnsureNginxHelpers keeps the shared nginx configuration in step with the
// panel that renders vhosts against it.
//
// The conf.d file the panel writes declares the maps every site vhost uses —
// $panel_https, and now $panel_proto. It is written when nginx is installed and
// never again, so a panel that starts referencing a new one would render a
// vhost naming a variable nothing declares. nginx does not skip that file: it
// refuses the whole configuration, for every site on the machine and for the
// panel itself. So the file is checked on every start, and rewritten when this
// build's version of it differs from what is on disk.
//
// Reloaded only when it changed, and only when nginx accepts the result: a
// panel restart should not restart nginx, and a bad configuration should not be
// loaded over a working one.
func (a *App) EnsureNginxHelpers() {
	if !a.Store.ServiceInstalled("nginx") {
		return
	}

	const path = "/etc/nginx/conf.d/panel-proxy.conf"

	if current, err := proc.ReadFile(path, true); err == nil && current == sites.HelperConfig() {
		return
	}

	if err := a.Sites.WriteSharedConfig(nil); err != nil {
		a.Log.Warn("could not write the shared nginx configuration", "error", err)

		return
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		a.Log.Error("nginx refused the configuration after the shared file was rewritten",
			"output", check.Output)

		return
	}

	if proc.Ok("sudo systemctl reload nginx") {
		a.Log.Info("the shared nginx configuration was brought up to date")
	}
}

// InstallAppsNow installs the same set BootstrapApps queues, in the foreground,
// and does not return until the machine has it.
//
// This is what the installer runs before it starts the service, so that the
// first sign-in lands on a server that is already finished. Queueing the work
// meant the panel came up in a few seconds and then spent the next ten minutes
// installing nginx, PHP and MariaDB underneath whoever was clicking around in
// it — every page qualified by "installing…", and a site created in that window
// refused for a missing app that was on its way.
//
// Same catalogue, same steps, same task row, so the Apps page afterwards shows
// exactly what happened and the console is there to read. The difference is
// only who waits: the installer, once, instead of the operator, every time.
//
// progress is given a line per step as it lands, because this runs on somebody's
// terminal and ten silent minutes reads as a hang.
func (a *App) InstallAppsNow(ctx context.Context, choice string, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}

	choice = strings.ToLower(strings.TrimSpace(choice))

	if choice == "" {
		choice = a.Config.InstallApps
	}

	if choice == "" {
		choice = "all"
	}

	if choice == "none" {
		progress("Nothing to install: the panel is configured to install no apps.")

		return nil
	}

	a.Catalog.SyncRows()

	// The same guard BootstrapApps has, for the same reason: an operator who
	// removed MongoDB is not asking for it back because they re-ran the
	// installer.
	if a.Store.SettingBool(bootstrapSetting, false) || len(a.Store.InstalledServiceKeys(ctx)) > 0 {
		a.Store.SetSetting(bootstrapSetting, "1")
		progress("This machine already has apps on it; leaving them as they are.")

		return nil
	}

	keys := []string{}

	for _, entry := range catalog.Entries {
		if choice == "all" || entry.Core {
			keys = append(keys, entry.Key)
		}
	}

	queued := a.Catalog.Queue(keys, false)

	// Written before the work rather than after it: a half-finished install is
	// still an install, and the service starting behind it must not queue the
	// whole catalogue a second time.
	a.Store.SetSetting(bootstrapSetting, "1")

	if queued == 0 {
		return nil
	}

	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, catalog.Label(key))
	}

	progress(fmt.Sprintf("Installing %d apps: %s", queued, strings.Join(labels, ", ")))

	done := make(chan error, 1)

	go func() { done <- a.Catalog.InstallQueued(ctx, 0) }()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	seen := map[string]bool{}

	for {
		select {
		case err := <-done:
			a.mirrorSteps(ctx, seen, progress)
			a.reportApps(ctx, progress)

			return err

		case <-ticker.C:
			a.mirrorSteps(ctx, seen, progress)
		}
	}
}

// mirrorSteps prints the steps that have finished since the last look. The task
// row is the same one the Apps page reads, so this is a view of it rather than a
// second account of what happened.
func (a *App) mirrorSteps(ctx context.Context, seen map[string]bool, progress func(string)) {
	id := a.Store.LatestTaskByAction(ctx, "provision", "services.install", false)
	if id <= 0 {
		return
	}

	task, err := a.Store.FindTask(ctx, id)
	if err != nil || strings.TrimSpace(task.Steps) == "" {
		return
	}

	var steps []store.TaskStep

	if err := json.Unmarshal([]byte(task.Steps), &steps); err != nil {
		return
	}

	for _, step := range steps {
		if step.Status == "pending" || step.Status == "running" || seen[step.Name] {
			continue
		}

		seen[step.Name] = true

		switch step.Status {
		case "failed":
			progress("! " + step.Name)
		case "skipped":
			continue
		default:
			progress("+ " + step.Name)
		}
	}
}

// reportApps is the last word: what is on the machine, and what is not.
func (a *App) reportApps(ctx context.Context, progress func(string)) {
	services, err := a.Store.Services(ctx)
	if err != nil {
		return
	}

	installed, failed := []string{}, []string{}

	for key, service := range services {
		switch service.Status {
		case store.ServiceInstalled:
			installed = append(installed, catalog.Label(key))
		case store.ServiceFailed:
			failed = append(failed, catalog.Label(key))
		}
	}

	sort.Strings(installed)
	sort.Strings(failed)

	if len(installed) > 0 {
		progress(fmt.Sprintf("Installed: %s", strings.Join(installed, ", ")))
	}

	if len(failed) > 0 {
		progress(fmt.Sprintf("Did not install: %s — the Apps page has the console, "+
			"and a Retry button beside each", strings.Join(failed, ", ")))
	}
}

// mysqlAccountsOpened records that every MariaDB account the panel created has
// been rebuilt at every host, so the sweep below runs once.
const mysqlAccountsOpened = "mysql_accounts_opened"

// EnsureMySQLReachable makes the credentials the panel prints true.
//
// The Databases pages now name the server's own address for MariaDB, because
// the credentials are for applications that connect from off the machine. Two
// things have to be so for that to work, and on a server set up by an earlier
// panel neither is: MariaDB listens on loopback only, and every account it
// created exists for 'localhost' and nowhere else.
//
// Printing an address that is refused is worse than printing loopback. So the
// server is opened and the accounts are rebuilt — once, on the first start
// after the update, and never again.
//
// Rebuilt with the password the panel already holds, so nothing that works
// today stops working. It restarts MariaDB, which is why it is guarded by a
// setting rather than run on every start.
func (a *App) EnsureMySQLReachable(ctx context.Context) {
	if !a.Store.ServiceInstalled("mysql") || a.Store.SettingBool(mysqlAccountsOpened, false) {
		return
	}

	if err := databases.EnsureMySQLRemote(a.Store, nil); err != nil {
		a.Log.Warn("could not open MariaDB to the network", "error", err)

		return
	}

	rows, err := a.Store.Databases(ctx)
	if err != nil {
		return
	}

	for _, database := range rows {
		if database.Engine != "mysql" {
			continue
		}

		if err := a.Databases.RepairAccount(ctx, database.ID); err != nil {
			a.Log.Warn("could not rebuild a MariaDB account",
				"database", database.Name, "error", err)

			return
		}
	}

	a.Store.SetSetting(mysqlAccountsOpened, "1")
	a.Log.Info("MariaDB now accepts connections from off this machine")
}
