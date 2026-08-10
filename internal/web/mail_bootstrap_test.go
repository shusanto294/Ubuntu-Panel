package web

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/mail"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func mailApp(t *testing.T) *App {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	app := &App{
		Config: &config.Config{InstallApps: "none", Workers: 1},
		Store:  st,
		Log:    log,
	}

	app.Queue = queue.New(st, log, 1)
	app.Catalog = catalog.New(st)
	app.Mail = mail.New(st)

	app.Queue.Handle(queue.MailConfigure, func(ctx context.Context, job *store.Job) error {
		return nil
	})

	return app
}

// queued is how many mail.configure jobs are waiting.
func queued(t *testing.T, app *App) int {
	t.Helper()

	count := 0

	for {
		job, err := app.Store.ClaimJob(context.Background())
		if err != nil {
			return count
		}

		if job.Kind == queue.MailConfigure {
			count++
		}

		app.Store.FinishJob(job.ID)
	}
}

// Nothing to configure on a machine with no mail server.
func TestMailIsNotConfiguredBeforeItIsInstalled(t *testing.T) {
	app := mailApp(t)

	app.EnsureMailConfigured()

	if n := queued(t, app); n != 0 {
		t.Fatalf("queued %d configure jobs with no mail server installed", n)
	}
}

// Installed and never configured: nobody should have to ask.
func TestMailIsConfiguredOnceItIsInstalled(t *testing.T) {
	app := mailApp(t)

	app.Catalog.SyncRows()
	app.Store.SetServiceStatus("mail", store.ServiceInstalled, "3.8.6", "")

	app.EnsureMailConfigured()

	if n := queued(t, app); n != 1 {
		t.Fatalf("queued %d configure jobs, want one", n)
	}
}

// The case that used to need a repair button: an update changed what the server
// expects, and the files on disk still describe the old arrangement.
func TestMailIsConfiguredAgainAfterAPanelUpdate(t *testing.T) {
	app := mailApp(t)

	app.Catalog.SyncRows()
	app.Store.SetServiceStatus("mail", store.ServiceInstalled, "3.8.6", "")
	app.Store.SetSetting("mail_configured", "1")
	app.Store.SetSetting("mail_configured_version", "1.0.0-ancient")

	app.EnsureMailConfigured()

	if n := queued(t, app); n != 1 {
		t.Fatalf("queued %d configure jobs after an update, want one", n)
	}
}

// And left alone when it is already current, so restarting the panel does not
// restart the mail server with it.
func TestAConfiguredMailServerIsLeftAlone(t *testing.T) {
	app := mailApp(t)

	app.Catalog.SyncRows()
	app.Store.SetServiceStatus("mail", store.ServiceInstalled, "3.8.6", "")
	app.Store.SetSetting("mail_configured", "1")
	app.Store.SetSetting("mail_configured_version", config.Version)

	app.EnsureMailConfigured()

	if n := queued(t, app); n != 0 {
		t.Fatalf("queued %d configure jobs for a current configuration", n)
	}
}
