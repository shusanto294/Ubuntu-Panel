package web

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/catalog"
	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A server has only the services its sites use. Nothing is installed up front
// and an update adds nothing new: a site's deployment installs what its type
// needs the first time a site of that type is made — PHP, MariaDB and WP-CLI
// for WordPress, Node.js for Next.js — and waits for it.

// siteServices are the services a site of this type needs on the machine.
// The site form's own list (Sites/Create.vue) says the same.
func siteServices(kind string, ssl bool) []string {
	meta := config.FindSiteType(kind)
	if meta == nil {
		return nil
	}

	keys := []string{"nginx"}

	if ssl {
		keys = append(keys, "certbot")
	}
	if meta.Proxied {
		keys = append(keys, "node")
	}
	if meta.Runtime == "php" {
		keys = append(keys, "php")
	}
	if meta.Database || kind == "wordpress" {
		keys = append(keys, "mysql")
	}
	if kind == "laravel" {
		keys = append(keys, "composer")
	}
	if kind == "wordpress" {
		keys = append(keys, "wpcli")
	}

	return keys
}

// serviceWait is how often a deployment waiting for its services looks again.
const serviceWait = 10 * time.Second

// awaitSiteServices is the deployment's first step: the services the site
// needs, installed. It returns nil when they all are, a RetryLater while they
// are being installed — the job comes back when it is due, and the worker is
// free for the install meanwhile — and an error when one of them could not
// be installed, which is also written on the site.
func (a *App) awaitSiteServices(ctx context.Context, job *store.Job, siteID int64) error {
	site, err := a.Store.FindSite(ctx, siteID)
	if err != nil {
		// Gone, or never there: the deployment says so itself.
		return nil
	}

	failed, err := a.awaitServices(ctx, job, siteServices(site.Type, site.SSL), site.UserID, site.Domain)

	if len(failed) > 0 {
		reason := fmt.Sprintf("%s could not be installed, so the site was not deployed. "+
			"Logs has the install's console; deploying the site again tries the install again.",
			strings.Join(failed, " and "))
		a.Store.SetSiteStatus(site.ID, "failed", reason)

		return fmt.Errorf("%s", reason)
	}

	return err
}

// awaitServices gets the services in needed, and what they depend on, onto
// this machine for a job that cannot start without them: a site's deployment,
// or a site or mail domain arriving from another server.
//
// It returns nil once they are all installed and a RetryLater while any is
// still queued or installing, having queued the ones that are missing. failed
// names the ones whose install has already been tried for this job and did
// not work; the caller says what that means for its own work.
func (a *App) awaitServices(ctx context.Context, job *store.Job, needed []string, userID int64, forWhat string) (failed []string, err error) {
	needed = catalog.WithDependencies(needed)

	// Asked of the machine rather than the rows the first time: a service
	// put there by hand, or a stale `failed`, is not a reason to install.
	if job.Attempts <= 1 {
		a.Catalog.Detect(needed)
	}

	rows, _ := a.Store.Services(ctx)

	missing := []string{}
	pending := false

	for _, key := range needed {
		row := rows[key]
		status := store.ServiceNotInstalled
		if row != nil {
			status = row.Status
		}

		switch status {
		case store.ServiceInstalled:
		case store.ServiceQueued, store.ServiceInstalling:
			pending = true
		case store.ServiceFailed:
			// Failed before this job asked for it: worth one more go.
			// Failed after: the job stops here.
			if job.Attempts <= 1 {
				missing = append(missing, key)
			} else {
				failed = append(failed, label(key))
			}
		default:
			missing = append(missing, key)
		}
	}

	if len(failed) > 0 {
		return failed, nil
	}

	if len(missing) > 0 {
		if a.Catalog.Queue(missing, false) > 0 {
			if _, err := a.Queue.PushID(queue.ServicesInstall, "user", userID); err != nil {
				return nil, err
			}
		}

		a.Log.Info("installing what a job needs before it runs", "for", forWhat,
			"services", strings.Join(missing, ","))

		return nil, &queue.RetryLater{After: serviceWait, Reason: fmt.Errorf("installing %s", strings.Join(missing, ", "))}
	}

	if pending {
		return nil, &queue.RetryLater{After: serviceWait, Reason: fmt.Errorf("waiting for services to install")}
	}

	return nil, nil
}

// label is a service's name as the Services page shows it.
func label(key string) string {
	if entry := catalog.Find(key); entry != nil {
		return entry.Label
	}

	return key
}

// installingFor is what a site waiting to be deployed is waiting for: the
// services it needs that are being installed, by name. Empty for a site that
// is not waiting.
func (a *App) installingFor(ctx context.Context, site *store.Site) []string {
	if site.Status != "pending" {
		return []string{}
	}

	rows, _ := a.Store.Services(ctx)
	names := []string{}

	for _, key := range catalog.WithDependencies(siteServices(site.Type, site.SSL)) {
		if row := rows[key]; row != nil && (row.Status == store.ServiceQueued || row.Status == store.ServiceInstalling) {
			names = append(names, label(key))
		}
	}

	return names
}
