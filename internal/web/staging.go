package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The Staging tab: cloning a site to a second hostname, and pushing it back.

// stagingProps is what the site page's Staging tab renders from.
//
// Both halves of the pair in one shape, because the tab is the same tab on
// either side of it: on production it offers the clone and lists the copies, and
// on a staging site it offers the push and names what it is a copy of.
func (a *App) stagingProps(ctx context.Context, site *store.Site) map[string]any {
	props := map[string]any{
		"is_staging":        site.IsStaging(),
		"suggested_domain":  sites.SuggestStagingDomain(site.Domain),
		"supported":         stagingSupported(site) == "",
		"unsupported_why":   nullable(stagingSupported(site)),
		"has_database":      site.NeedsDatabase(),
		"rewrites_database": site.Type == "wordpress" || site.Type == "laravel",
		"parent":            nil,
		"copies":            []map[string]any{},
	}

	if site.IsStaging() {
		if parent, err := a.Store.FindSite(ctx, site.StagingOf); err == nil {
			props["parent"] = map[string]any{
				"id":     parent.ID,
				"domain": parent.Domain,
				"url":    parent.URL(),
				"status": parent.Status,
			}
		}

		return props
	}

	copies, err := a.Store.StagingSitesFor(ctx, site.ID)
	if err != nil {
		return props
	}

	rows := make([]map[string]any, 0, len(copies))

	for _, clone := range copies {
		rows = append(rows, map[string]any{
			"id":         clone.ID,
			"domain":     clone.Domain,
			"url":        clone.URL(),
			"status":     clone.Status,
			"last_error": nullable(clone.LastError),
			"created_at": clone.CreatedAt,
		})
	}

	props["copies"] = rows

	return props
}

// stagingSupported is why this site cannot be cloned, or empty.
func stagingSupported(site *store.Site) string {
	switch site.Type {
	case "nextcloud":
		// Not a shortage of effort. Nextcloud keeps a data directory whose
		// absolute path is recorded in its configuration and in its database,
		// holds file locks and a cron job keyed to the installation, and
		// refuses hostnames that are not in its trusted_domains list. A copy
		// that got any of that wrong would be a Nextcloud that mounts the
		// original's files — which is not a staging environment, it is two
		// front ends writing to one set of user data.
		return "Nextcloud stores the absolute path of its data directory inside itself, " +
			"so a copy on another hostname would still be reading and writing the " +
			"original's files. Use the Backups tab to take a copy you can restore."

	case "n8n":
		// n8n's credentials are encrypted with a key in its own home
		// directory, and its workflows are live: a second copy on another
		// hostname runs the same schedules and calls the same webhooks.
		return "n8n workflows run on a schedule and call out to other services, so a " +
			"second copy would fire every one of them a second time. Use the Backups " +
			"tab to take a copy you can restore."
	}

	if site.Status == "deleting" {
		return "This site is being removed."
	}

	return ""
}

// sitesStagingStore creates the staging copy.
func (a *App) sitesStagingStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	source, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil || source.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if why := stagingSupported(source); why != "" {
		a.failBack(w, r, why)
		return
	}

	// One level, not a tree.
	//
	// A staging copy of a staging copy has no production site to push to, and
	// the pair of buttons on the tab stops meaning anything. Refusing is
	// clearer than a third state nobody asked for.
	if source.IsStaging() {
		a.failBack(w, r, "This is already a staging copy. Clone the production site "+
			"instead, or push this one up first.")

		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	domain := strings.ToLower(strings.TrimSpace(input["domain"]))
	if domain == "" {
		domain = sites.SuggestStagingDomain(source.Domain)
	}

	staging, err := a.prepareStagingSite(r.Context(), user, source, domain)
	if err != nil {
		var refusal siteProblem

		if errors.As(err, &refusal) && refusal.Field != "" {
			sess.Error(refusal.Field, refusal.Message)
		} else {
			sess.Error("domain", err.Error())
		}

		inertia.Back(w, r, "/sites/"+itoa(source.ID))

		return
	}

	id, err := a.Store.CreateSite(r.Context(), staging)
	if err != nil || id <= 0 {
		a.Log.Error("could not create the staging site record", "error", err)
		a.failBack(w, r, "Could not create the staging site record.")

		return
	}

	if _, err := a.Queue.PushID(queue.SiteStagingCreate, "site", id); err != nil {
		a.Log.Error("could not queue the clone", "site", id, "error", err)
	}

	// To the new site's own page, where the clone is running step by step.
	inertia.Redirect(w, r, "/sites/"+itoa(id))
}

// prepareStagingSite is the staging site's row, built from the one it copies.
//
// Most of it comes from the source rather than from the panel's defaults: a
// staging copy on a different PHP version, or with a different web directory,
// is not a copy of anything. What is deliberately *not* copied is as important
// — the aliases above all.
func (a *App) prepareStagingSite(ctx context.Context, user *store.User, source *store.Site,
	domain string) (*store.Site, error) {
	// Through the same door every other site goes through: it is what checks
	// the domain is a domain and is free, decides whether the panel can publish
	// its records and with which credential, hands a proxied site a free port,
	// and refuses a type whose runtime is not installed.
	staging, err := a.prepareSite(ctx, user, siteRequest{
		Type:   source.Type,
		Domain: domain,

		// No aliases, and no www.
		//
		// This is the one that would do real damage. A staging site that
		// inherited production's aliases would put production's hostnames into
		// a second nginx server_name — and nginx hands a hostname to whichever
		// vhost claims it first, so production would start being served out of
		// the staging directory. Silently, and only after the next reload.
		Aliases: nil,
		WWW:     boolPointer(false),

		PHPVersion:   source.PHPVersion,
		WebDirectory: strings.TrimPrefix(source.WebDirectory, "/"),
		Repository:   source.Repository,
		Branch:       source.Branch,
		StartCommand: source.StartCommand,
		BuildCommand: source.BuildCommand,

		SSL: boolPointer(source.SSL),

		// Whatever production is doing for DNS, including which credential.
		// A staging subdomain of a managed domain is in the same zone, so the
		// panel can publish it without being asked anything.
		ManageDNS:    boolPointer(source.ManageDNS),
		DNSAccountID: source.DNSAccountID,
		DNSType:      source.DNSType,
		DNSContent:   source.DNSContent,
		DNSProxied:   boolPointer(source.DNSProxied),
	})

	if err != nil {
		return nil, err
	}

	staging.StagingOf = source.ID

	// How the panel reaches the repository, as production has it.
	//
	// Not the deploy key, though: that is a key pair whose public half is
	// registered at the repository, and sharing one between two sites means
	// revoking it for both. The copy gets its own, generated the first time
	// its Git tab asks for one — until then the method is set and the tab says
	// what is missing. A token needs none of that and works immediately.
	staging.RepoAuth = source.RepoAuth
	staging.RepoToken = source.RepoToken

	// The application's own login, as production has it. The database is about
	// to be a copy of production's, so production's credentials are the ones
	// that will work — generating a new password here would write it onto a row
	// and have it be wrong.
	staging.WPAdminUser = source.WPAdminUser
	staging.WPAdminPassword = source.WPAdminPassword
	staging.WPAdminEmail = source.WPAdminEmail
	staging.WPTitle = source.WPTitle

	// The cache is not copied. Every site on this machine shares one Redis and
	// one nginx cache zone, and a copy that arrived with production's cache
	// configuration would be reading production's cached pages — which is a
	// staging site showing production's content and no way to tell.
	staging.CacheMode = "off"
	staging.CacheEnabled = false

	return staging, nil
}

// sitesStagingPush copies a staging site back over the site it came from.
func (a *App) sitesStagingPush(w http.ResponseWriter, r *http.Request) {
	staging, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil || staging.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if !staging.IsStaging() {
		a.failBack(w, r, "This site is not a staging copy, so there is nothing to push to.")
		return
	}

	target, err := a.Store.FindSite(r.Context(), staging.StagingOf)
	if err != nil {
		a.failBack(w, r, "The site this is a staging copy of no longer exists.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	options := sites.StagingOptions{
		Files:    boolInput(input, "files", true),
		Database: boolInput(input, "database", false),

		// On by default, and the default that matters. See StagingOptions.
		KeepUploads: boolInput(input, "keep_uploads", true),
	}

	if !options.Files && !options.Database {
		a.failBack(w, r, "Pick at least one of the files and the database — a push that "+
			"moves neither does nothing.")

		return
	}

	if options.Database && !target.NeedsDatabase() {
		options.Database = false
	}

	if _, err := a.Queue.Push(queue.SiteStagingPush, map[string]any{
		"site":         staging.ID,
		"files":        options.Files,
		"database":     options.Database,
		"keep_uploads": options.KeepUploads,
	}); err != nil {
		a.Log.Error("could not queue the push", "site", staging.ID, "error", err)
		a.failBack(w, r, "Could not start the push.")

		return
	}

	sess := session.FromContext(r.Context())

	sess.Flash("success", "Pushing "+staging.Domain+" to "+target.Domain+
		" — it is backed up first, and every step is below as it runs.")

	// To the production site's page rather than back to this one. That is where
	// the task's console is, where the backup it just took is listed, and where
	// somebody needs to be standing if the push goes wrong.
	inertia.Redirect(w, r, "/sites/"+itoa(target.ID))
}
