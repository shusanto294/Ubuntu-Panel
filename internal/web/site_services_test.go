package web

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/catalog"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// Creating a WordPress site on a server with nothing on it installs what
// WordPress needs and nothing else, waits for it, and then deploys.
func TestASiteDeploymentInstallsOnlyWhatItsTypeNeeds(t *testing.T) {
	ctx := context.Background()
	app := bootstrapApp(t, "none")
	app.Catalog.SyncRows()

	id, err := app.Store.CreateSite(ctx, &store.Site{
		Domain: "blog.example", Type: "wordpress", RootPath: "/tmp/blog.example", PHPVersion: "8.3", SSL: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A second attempt, so the machine is not probed: what the rows say is
	// what this test is about.
	job := &store.Job{Attempts: 2}

	var retry *queue.RetryLater
	if err := app.awaitSiteServices(ctx, job, id); !errors.As(err, &retry) {
		t.Fatalf("the deployment did not wait for its services: %v", err)
	}

	queued := app.Store.QueuedServices(ctx)
	for _, key := range []string{"php", "mysql", "wpcli", "certbot"} {
		if !slices.Contains(queued, key) {
			t.Errorf("%s was not queued for a WordPress site; queued %v", key, queued)
		}
	}
	for _, key := range []string{"mongodb", "redis", "node", "mail", "dns", "phpmyadmin"} {
		if slices.Contains(queued, key) {
			t.Errorf("%s was queued, but a WordPress site does not need it", key)
		}
	}

	// Installed: the deployment goes ahead.
	for _, key := range catalog.WithDependencies(siteServices("wordpress", true)) {
		app.Store.SetServiceStatus(key, store.ServiceInstalled, "", "")
	}

	if err := app.awaitSiteServices(ctx, job, id); err != nil {
		t.Fatalf("the deployment still waits with everything installed: %v", err)
	}

	// One that failed while the deployment waited stops it, and says so on
	// the site.
	app.Store.SetServiceStatus("mysql", store.ServiceFailed, "", "apt said no")

	if err := app.awaitSiteServices(ctx, job, id); err == nil || errors.As(err, &retry) {
		t.Fatalf("a failed install did not stop the deployment: %v", err)
	}

	if site, _ := app.Store.FindSite(ctx, id); site.Status != "failed" || site.LastError == "" {
		t.Fatalf("the site does not say why: %q %q", site.Status, site.LastError)
	}
}
