package web

import (
	"net/http"
	"strings"

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

	if err := sites.ApplyEnv(site); err != nil {
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
