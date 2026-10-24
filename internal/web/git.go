package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// The Git tab: where a site is deployed from, how the panel authenticates to
// it, and what it does about new commits.

// gitProps is what the tab renders from.
func (a *App) gitProps(ctx context.Context, site *store.Site) map[string]any {
	method := sites.RepoAuthOf(site)

	props := map[string]any{
		"repository":  site.Repository,
		"branch":      orDefault(site.Branch, "main"),
		"auth":        method,
		"methods":     sites.RepoAuthMethods,
		"deploy_key":  nullable(site.RepoDeployKey),
		"has_token":   site.RepoToken != "",
		"last_commit": nullable(site.LastCommit),

		"schedules":       sites.PullSchedules,
		"auto_pull":       orDefault(site.AutoPull, "off"),
		"auto_pull_label": sites.PullScheduleLabel(orDefault(site.AutoPull, "off")),

		// The webhook. The URL is built from whatever address the panel is
		// actually reached at, because a URL with the machine's bare IP in it
		// is one a provider can still call — and one the operator can tell is
		// not the one they wanted.
		"webhook_url":    a.webhookURL(site),
		"webhook_secret": nullable(site.WebhookSecret),
		"reachable":      a.Store.Setting("panel_domain", "") != "",
	}

	if site.AutoPullNextAt > 0 {
		props["auto_pull_next"] = time.Unix(site.AutoPullNextAt, 0).UTC().Format(time.RFC3339)
	} else {
		props["auto_pull_next"] = nil
	}

	return props
}

// webhookURL is the address a provider posts to.
func (a *App) webhookURL(site *store.Site) string {
	return a.panelBaseURL() + "/webhooks/git/" + itoa(site.ID)
}

// panelBaseURL is where this panel is reached from outside.
func (a *App) panelBaseURL() string {
	domain := a.Store.Setting("panel_domain", "")

	if domain == "" {
		// No hostname set, so the best the panel can offer is its address and
		// port. Honest rather than useful: a provider can post to it, and the
		// page says what to do about the certificate.
		return "http://" + orDefault(system.PublicIP(), "this-server") + ":" +
			itoa(int64(a.Config.PanelPort))
	}

	if a.Store.SettingBool("panel_domain_tls", false) {
		return "https://" + domain
	}

	return "http://" + domain
}

// sitesGit saves the tab.
//
// One handler for several cards, the way the Settings tab works: a post carries
// the card that was submitted and the rest is left exactly as it was rather
// than cleared.
func (a *App) sitesGit(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil || site.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	switch strings.TrimSpace(input["action"]) {
	case "source":
		a.saveGitSource(w, r, site, input)

	case "auth":
		a.saveGitAuth(w, r, site, input)

	case "key":
		a.regenerateDeployKey(w, r, site)

	case "automation":
		a.saveGitAutomation(w, r, site, input)

	case "test":
		a.testGitAccess(w, r, site)

	default:
		sess.Error("action", "That is not something this form does.")
		a.failBack(w, r, "That is not something this form does.")
	}
}

// saveGitSource changes where the site is deployed from.
//
// Editable after creation on purpose. A repository URL is the one field on the
// create form nobody can verify by reading it back, and until this existed a
// typo meant deleting the site and making it again — which takes its database,
// its certificate and its DNS records with it.
func (a *App) saveGitSource(w http.ResponseWriter, r *http.Request, site *store.Site,
	input map[string]string) {
	sess := session.FromContext(r.Context())

	repository, refusal := NormaliseRepositoryInput(input["repository"])

	if refusal != "" {
		sess.Error("repository", refusal)
		inertia.Back(w, r, "/sites/"+itoa(site.ID))

		return
	}

	branch := strings.TrimSpace(input["branch"])
	if branch == "" {
		branch = "main"
	}

	// A branch name reaches a refspec on a command line. git's own rules are
	// longer than this, and anything this rejects is a name nobody has.
	if strings.ContainsAny(branch, " \t\n~^:?*[\\") || strings.HasPrefix(branch, "-") {
		sess.Error("branch", "That is not a branch name git will take.")
		inertia.Back(w, r, "/sites/"+itoa(site.ID))

		return
	}

	a.Store.SetSiteRepository(site.ID, repository, branch)

	if repository == "" {
		a.okBack(w, r, "The repository was cleared. This site is no longer deployed "+
			"from git, and the Pull button is gone with it.")

		return
	}

	// Checked straight away. The point of being able to edit this is that it
	// was wrong, so saving without saying whether it works would be the same
	// dead end one step further along.
	site.Repository = repository
	site.Branch = branch

	access := sites.CheckAccess(site, branch)

	if access.OK && access.Found {
		a.okBack(w, r, "Saved. "+access.Message+
			" Pull now to put that commit on the site.")

		return
	}

	a.failBack(w, r, "Saved, but the panel cannot read it yet: "+access.Message)
}

// saveGitAuth changes how the panel authenticates, generating a deploy key the
// first time one is asked for.
func (a *App) saveGitAuth(w http.ResponseWriter, r *http.Request, site *store.Site,
	input map[string]string) {
	sess := session.FromContext(r.Context())

	method := strings.TrimSpace(input["auth"])

	if !sites.ValidRepoAuth(method) {
		sess.Error("auth", "Pick one of the ways the panel can authenticate.")
		inertia.Back(w, r, "/sites/"+itoa(site.ID))

		return
	}

	token := strings.TrimSpace(input["token"])

	if method == sites.AuthToken && token == "" && site.RepoToken == "" {
		sess.Error("token", "Paste the access token.")
		inertia.Back(w, r, "/sites/"+itoa(site.ID))

		return
	}

	if err := a.Store.SetSiteRepoAuth(site.ID, method, token); err != nil {
		a.Log.Error("could not store the repository credential", "site", site.ID, "error", err)
		a.failBack(w, r, "Could not store the credential.")

		return
	}

	site.RepoAuth = method

	if token != "" {
		site.RepoToken = token
	}

	message := ""

	switch method {
	case sites.AuthNone:
		message = "The panel will clone this repository without credentials."

	case sites.AuthKey:
		if site.RepoDeployKey == "" {
			key, err := sites.GenerateDeployKey(site)
			if err != nil {
				a.failBack(w, r, err.Error())

				return
			}

			a.Store.SetSiteDeployKey(site.ID, key)
			site.RepoDeployKey = key

			message = "A deploy key was generated. Add the public key below to the " +
				"repository — until you do, the panel cannot read it."

			a.okBack(w, r, message)

			return
		}

		message = "The panel will use its deploy key for this repository."

	case sites.AuthToken:
		message = "The token is stored, encrypted. It is never sent back to the browser."
	}

	// And whether it actually works, which is the only answer worth having.
	if strings.TrimSpace(site.Repository) != "" {
		access := sites.CheckAccess(site, site.Branch)

		if access.OK && access.Found {
			a.okBack(w, r, message+" "+access.Message)

			return
		}

		a.failBack(w, r, message+" But "+strings.ToLower(access.Message[:1])+
			access.Message[1:])

		return
	}

	a.okBack(w, r, message)
}

// regenerateDeployKey replaces the pair.
func (a *App) regenerateDeployKey(w http.ResponseWriter, r *http.Request, site *store.Site) {
	key, err := sites.GenerateDeployKey(site)
	if err != nil {
		a.failBack(w, r, err.Error())
		return
	}

	a.Store.SetSiteDeployKey(site.ID, key)

	a.okBack(w, r, "A new deploy key was generated. The old one no longer works, so "+
		"this site cannot pull until the new public key is added to the repository — "+
		"and the old one is worth deleting there.")
}

// saveGitAutomation sets the polling interval and the webhook secret.
func (a *App) saveGitAutomation(w http.ResponseWriter, r *http.Request, site *store.Site,
	input map[string]string) {
	sess := session.FromContext(r.Context())

	schedule := strings.TrimSpace(input["auto_pull"])
	if schedule == "" {
		schedule = "off"
	}

	if !sites.ValidPullSchedule(schedule) {
		sess.Error("auto_pull", "Pick one of the intervals offered.")
		inertia.Back(w, r, "/sites/"+itoa(site.ID))

		return
	}

	if schedule != "off" && strings.TrimSpace(site.Repository) == "" {
		a.failBack(w, r, "This site is not deployed from a repository, so there is "+
			"nothing to pull.")

		return
	}

	a.Store.SetSiteAutoPull(site.ID, schedule, sites.NextPullAt(schedule, time.Now()))

	notes := []string{}

	switch schedule {
	case "off":
		notes = append(notes, "The panel will not pull on its own.")

	default:
		notes = append(notes, "The panel will check the repository "+
			strings.ToLower(sites.PullScheduleLabel(schedule))+
			" and pull only when the commit has changed.")
	}

	// The webhook, which is the other half and switched on independently: a
	// site can have both, and most should — the webhook for a deploy that
	// happens the moment somebody pushes, and the interval as the backstop for
	// the push the webhook missed because the panel was restarting.
	switch {
	case boolInput(input, "webhook", false) && site.WebhookSecret == "":
		secret := crypto.Password(40)

		if err := a.Store.SetSiteWebhookSecret(site.ID, secret); err != nil {
			a.Log.Error("could not store the webhook secret", "site", site.ID, "error", err)
		} else {
			notes = append(notes, "A webhook secret was generated — the URL and the "+
				"secret to paste into the repository are below.")
		}

	case !boolInput(input, "webhook", false) && site.WebhookSecret != "":
		a.Store.SetSiteWebhookSecret(site.ID, "")

		notes = append(notes, "The webhook is switched off, and a push to it is now "+
			"refused. Remove it at the repository too.")
	}

	a.okBack(w, r, strings.Join(notes, " "))
}

// testGitAccess is the Test connection button.
func (a *App) testGitAccess(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if strings.TrimSpace(site.Repository) == "" {
		a.failBack(w, r, "This site has no repository.")
		return
	}

	access := sites.CheckAccess(site, site.Branch)

	if access.OK && access.Found {
		a.okBack(w, r, access.Message)
		return
	}

	a.failBack(w, r, access.Message)
}

// ---------------------------------------------------------------- the poller

// runPullSchedules is the clock behind the automatic pull.
//
// Its own goroutine rather than part of the queue, and the same shape as the
// backup scheduler: it does nothing but look at the time every minute and push
// work onto the queue.
func (a *App) runPullSchedules(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			a.takeDuePulls(ctx)
		}
	}
}

// takeDuePulls checks every site whose turn it is, and queues a pull for the
// ones that have actually moved.
//
// The check is `git ls-remote` and the comparison is against the commit the
// panel recorded when it last deployed. Pulling unconditionally would be a
// fetch, an npm install, a rebuild and a service restart every five minutes
// for a repository nobody had touched — which is a site that spends its life
// restarting.
//
// The due time moves on before the work is queued and not after it, so a
// repository that cannot be reached is not retried every minute for ever.
func (a *App) takeDuePulls(ctx context.Context) {
	due, err := a.Store.DuePulls(ctx, time.Now())
	if err != nil {
		a.Log.Warn("could not look for sites due a pull", "error", err)

		return
	}

	for _, site := range due {
		a.Store.SetSiteAutoPullNext(site.ID, sites.NextPullAt(site.AutoPull, time.Now()))

		if site.Status == "deploying" || site.Status == "deleting" {
			continue
		}

		head := sites.RemoteHead(site, site.Branch)

		if head == "" {
			a.Log.Warn("could not read the remote head for a scheduled pull",
				"site", site.Domain)

			continue
		}

		if head == site.LastCommit {
			continue
		}

		if _, err := a.Queue.PushID(queue.SitePull, "site", site.ID); err != nil {
			a.Log.Warn("could not queue a scheduled pull", "site", site.Domain, "error", err)

			continue
		}

		a.Log.Info("queued a pull: the branch moved", "site", site.Domain,
			"from", site.LastCommit, "to", head)
	}
}

// pullSiteNow is the shared path for a pull somebody or something asked for.
func (a *App) pullSiteNow(ctx context.Context, site *store.Site) error {
	if strings.TrimSpace(site.Repository) == "" {
		return errors.New("this site was not deployed from a repository")
	}

	if site.Status == "deleting" {
		return errors.New("this site is being removed")
	}

	_, err := a.Queue.PushID(queue.SitePull, "site", site.ID)

	return err
}
