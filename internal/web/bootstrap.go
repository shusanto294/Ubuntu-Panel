package web

import (
	"context"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
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
