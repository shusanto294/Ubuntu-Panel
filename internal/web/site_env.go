package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The Environment tab: a Node site's variables. See sites/env.go for where
// they go and why.

// envEditable reports whether this site has a tab for them. The applications
// the panel installs itself (n8n) write their own environment and would have
// it overwritten.
func envEditable(site *store.Site) bool {
	return site.IsProxied() && !site.IsManaged()
}

// sitesEnv saves the variables and applies them: a restart, or — for values
// read at build time — a rebuild.
func (a *App) sitesEnv(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil || site.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	back := "/sites/" + itoa(site.ID)

	if !envEditable(site) {
		a.failBack(w, r, "Environment variables are for Node.js, Next.js and Nuxt sites.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	vars := strings.TrimSpace(input["vars"])

	parsed, err := sites.ParseEnv(vars)
	if err != nil {
		sess.Error("vars", err.Error())
		inertia.Back(w, r, back)

		return
	}

	if err := a.Store.SetSiteEnvVars(site.ID, vars); err != nil {
		a.Log.Error("could not store the site's environment", "site", site.ID, "error", err)
		a.failBack(w, r, "Could not store the variables.")

		return
	}

	site.EnvVars = vars

	if err := a.Sites.ApplyEnv(r.Context(), site); err != nil {
		a.failBack(w, r, "Saved, but they could not be put in place: "+err.Error())
		return
	}

	count := itoa(int64(len(parsed))) + " variable(s)"

	if site.Status == "pending" || site.Status == "deploying" {
		a.okBack(w, r, "Saved "+count+". The deployment uses them.")
		return
	}

	// A site whose deployment failed has nothing to restart and may have no
	// build to pull onto — and a missing variable is the commonest reason a
	// build fails. Saving them is the fix, so it deploys again.
	if site.Status == "failed" {
		a.Queue.PushID(queue.SiteDeploy, "site", site.ID)
		a.okBack(w, r, "Saved "+count+". Deploying the site again with them.")

		return
	}

	if strings.TrimSpace(input["apply"]) == "rebuild" && strings.TrimSpace(site.Repository) != "" {
		a.Queue.PushID(queue.SitePull, "site", site.ID)
		a.okBack(w, r, "Saved "+count+". Pulling, rebuilding and restarting with them.")

		return
	}

	a.Queue.PushID(queue.SiteRestart, "site", site.ID)
	a.okBack(w, r, "Saved "+count+" and restarting the app with them. Values the build "+
		"bakes in, such as NEXT_PUBLIC_…, need Save and rebuild.")
}

// environmentProps is what the Environment tab renders from.
func (a *App) environmentProps(ctx context.Context, site *store.Site) map[string]any {
	var database map[string]any

	if site.DatabaseID > 0 {
		if row, err := a.Store.FindDatabase(ctx, site.DatabaseID); err == nil {
			database = map[string]any{
				"id":           row.ID,
				"name":         row.Name,
				"engine":       row.Engine,
				"engine_label": config.EngineLabel(row.Engine),
				"status":       row.Status,
			}
		}
	}

	engines := []map[string]string{}
	for _, engine := range sites.AppDatabaseEngines {
		engines = append(engines, map[string]string{"key": engine, "label": config.EngineLabel(engine)})
	}

	return map[string]any{
		"vars":     site.EnvVars,
		"managed":  a.Sites.ManagedEnv(ctx, site),
		"redis":    site.RedisDB > 0,
		"redis_db": site.RedisDB,
		"database": database,
		"engines":  engines,
	}
}

// sitesConnections connects or disconnects Redis, or gives the site a
// database, and then puts it in place: installs what is missing, provisions,
// writes the environment and restarts — or deploys again, for a site whose
// deployment failed.
func (a *App) sitesConnections(w http.ResponseWriter, r *http.Request) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil || site.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if !envEditable(site) {
		a.failBack(w, r, "Connections are for Node.js, Next.js and Nuxt sites.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	message := ""

	switch strings.TrimSpace(input["action"]) {
	case "redis":
		db, err := a.Sites.ConnectRedis(r.Context(), site)
		if err != nil {
			a.failBack(w, r, err.Error())
			return
		}

		message = "Connecting Redis (database " + itoa(int64(db)) + "). REDIS_URL is set for the app."

	case "redis-off":
		a.Sites.DisconnectRedis(site)
		message = "Redis disconnected. REDIS_URL is no longer set; the data in its database was left alone."

	case "database":
		engine := strings.TrimSpace(input["engine"])

		if site.DatabaseID > 0 {
			a.failBack(w, r, "This site already has a database.")
			return
		}

		if _, err := a.Sites.CreateAppDatabase(r.Context(), site, engine); err != nil {
			a.failBack(w, r, err.Error())
			return
		}

		message = "Creating a " + config.EngineLabel(engine) + " database for the app."

	default:
		a.failBack(w, r, "That is not something this form does.")
		return
	}

	switch site.Status {
	case "pending", "deploying":
		// The deployment that is coming, or running, reads them.
	case "failed":
		a.Queue.PushID(queue.SiteDeploy, "site", site.ID)
		message += " Deploying the site again with it."
	default:
		a.Queue.PushID(queue.SiteConnect, "site", site.ID)
		message += " The app restarts with it when it is ready."
	}

	a.okBack(w, r, message)
}
