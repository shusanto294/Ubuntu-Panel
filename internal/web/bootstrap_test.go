package web

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/catalog"
	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// bootstrapApp is the panel with nothing running: the bootstrap only ever
// queues, and the queue is not started, so nothing reaches a machine.
func bootstrapApp(t *testing.T, choice string) *App {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	app := &App{
		Config: &config.Config{InstallApps: choice, Workers: 1},
		Store:  st,
		Log:    log,
	}

	app.Queue = queue.New(st, log, 1)
	app.Catalog = catalog.New(st)

	// The bootstrap pushes this job; nothing runs it here.
	app.Queue.Handle(queue.ServicesInstall, func(ctx context.Context, job *store.Job) error {
		return nil
	})

	return app
}

// A fresh server should be ready to host a site without anybody installing
// anything first — everything in the catalogue except the apps that change what
// the machine is to the rest of the internet, which wait to be asked for.
func TestAFreshPanelQueuesEveryAppItIsAllowedTo(t *testing.T) {
	app := bootstrapApp(t, "all")

	app.BootstrapApps(context.Background())

	queued := map[string]bool{}

	for _, key := range app.Store.QueuedServices(context.Background()) {
		queued[key] = true
	}

	for _, entry := range catalog.Entries {
		switch {
		case entry.OnlyWhenAsked && queued[entry.Key]:
			t.Errorf("%s installs itself on a fresh panel, and should wait to be asked",
				entry.Key)

		case !entry.OnlyWhenAsked && !queued[entry.Key]:
			t.Errorf("%s was not queued on a fresh panel", entry.Key)
		}
	}
}

// `core` is what no site can run without, and nothing else.
func TestCoreQueuesOnlyWhatASiteNeeds(t *testing.T) {
	app := bootstrapApp(t, "core")

	app.BootstrapApps(context.Background())

	queued := app.Store.QueuedServices(context.Background())

	for _, key := range queued {
		entry := catalog.Find(key)

		if entry == nil || !entry.Core {
			t.Errorf("%q was queued and is not core", key)
		}
	}

	if len(queued) == 0 {
		t.Fatal("nothing was queued")
	}

	if len(queued) == len(catalog.Entries) {
		t.Fatal("core queued the whole catalogue")
	}
}

func TestNoneInstallsNothing(t *testing.T) {
	app := bootstrapApp(t, "none")

	app.BootstrapApps(context.Background())

	if queued := app.Store.QueuedServices(context.Background()); len(queued) > 0 {
		t.Fatalf("PANEL_INSTALL_APPS=none queued %v", queued)
	}
}

// It happens once. An operator who removed MongoDB is not asking for it back on
// the next restart.
func TestItOnlyEverRunsOnce(t *testing.T) {
	app := bootstrapApp(t, "all")

	app.BootstrapApps(context.Background())

	// Whatever the first run queued is now the machine's business, not the
	// bootstrap's.
	for _, key := range app.Store.QueuedServices(context.Background()) {
		app.Store.SetServiceStatus(key, store.ServiceNotInstalled, "", "")
	}

	app.BootstrapApps(context.Background())

	if queued := app.Store.QueuedServices(context.Background()); len(queued) > 0 {
		t.Fatalf("the second run queued %v", queued)
	}
}

// The installer's own path. Only the refusals are exercised here: the third
// case would install the catalogue on the machine running the tests, which is
// what the guards exist to prevent and not something a test should discover.
func TestTheInstallerRefusesWhenThereIsNothingToDo(t *testing.T) {
	cases := []struct {
		name   string
		choice string
		set    func(app *App)
	}{
		{
			name:   "none installs nothing",
			choice: "none",
			set:    func(*App) {},
		},
		{
			name:   "a machine that has already been bootstrapped is left alone",
			choice: "all",
			set:    func(app *App) { app.Store.SetSetting(bootstrapSetting, "1") },
		},
		{
			name:   "a machine that already has an app is left alone",
			choice: "all",
			set: func(app *App) {
				app.Catalog.SyncRows()
				app.Store.SetServiceStatus("nginx", store.ServiceInstalled, "1.24.0", "")
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app := bootstrapApp(t, test.choice)
			test.set(app)

			if err := app.InstallAppsNow(context.Background(), test.choice, nil); err != nil {
				t.Fatalf("a no-op install returned %v", err)
			}

			if queued := app.Store.QueuedServices(context.Background()); len(queued) > 0 {
				t.Fatalf("it queued %v", queued)
			}
		})
	}
}

// A panel that already has apps on it was set up by somebody; installing the
// rest behind their back is not the same favour.
func TestAMachineThatAlreadyHasAppsIsLeftAlone(t *testing.T) {
	app := bootstrapApp(t, "all")

	app.Catalog.SyncRows()
	app.Store.SetServiceStatus("nginx", store.ServiceInstalled, "1.24.0", "")

	app.BootstrapApps(context.Background())

	if queued := app.Store.QueuedServices(context.Background()); len(queued) > 0 {
		t.Fatalf("queued %v on a machine that already had nginx", queued)
	}
}
