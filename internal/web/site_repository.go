package web

import (
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The repository on the create form: checked as it is typed, and asked for a
// credential only when the remote turns out to want one.
//
// The form used to take a URL and nothing else, so a private repository was
// discovered by the deployment — after the site, its vhost and its database
// existed — and the fix was on a tab of a site that had already failed. Now
// the form asks the remote first. A public repository needs nothing more; a
// private one gets a deploy key or a token right there, tested before the site
// is made.

// repositoryRequest is what the form sends about the repository.
type repositoryRequest struct {
	Repository string
	Branch     string
	Auth       string
	Token      string
	KeyID      string
}

func repositoryRequestFrom(input map[string]string) repositoryRequest {
	return repositoryRequest{
		Repository: input["repository"],
		Branch:     input["branch"],
		Auth:       strings.TrimSpace(input["repo_auth"]),
		Token:      strings.TrimSpace(input["repo_token"]),
		KeyID:      strings.TrimSpace(input["repo_key_id"]),
	}
}

// repositoryAccess resolves the request into the site fields it would set and
// asks the remote whether they work. field and refusal are set instead when
// the request cannot be checked at all.
func repositoryAccess(request repositoryRequest) (site *store.Site, keyPath string,
	access sites.Access, field, refusal string) {
	repository, problem := NormaliseRepositoryInput(request.Repository)
	if problem != "" {
		return nil, "", access, "repository", problem
	}

	method := request.Auth
	if method == "" {
		method = sites.AuthNone
	}

	if !sites.ValidRepoAuth(method) {
		return nil, "", access, "repo_auth", "Pick one of the ways the panel can authenticate."
	}

	site = &store.Site{
		Repository: sites.RepositoryForAuth(repository, method),
		Branch:     orDefault(strings.TrimSpace(request.Branch), "main"),
		RepoAuth:   method,
	}

	switch method {
	case sites.AuthToken:
		if request.Token == "" {
			return nil, "", access, "repo_token", "Paste the access token."
		}

		site.RepoToken = request.Token

	case sites.AuthKey:
		if !sites.ValidPendingKeyID(request.KeyID) {
			return nil, "", access, "repo_auth",
				"Generate the deploy key and add it to the repository first."
		}

		keyPath = sites.PendingDeployKeyPath(request.KeyID)
	}

	if site.Repository == "" {
		return site, keyPath, access, "", ""
	}

	return site, keyPath, sites.CheckAccessWith(site, site.Branch, keyPath), "", ""
}

// sitesRepositoryCheck answers the form's live check.
func (a *App) sitesRepositoryCheck(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	site, _, access, field, refusal := repositoryAccess(repositoryRequestFrom(input))

	if refusal != "" {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "found": false, "message": refusal, "field": field,
		})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"ok":                access.OK,
		"found":             access.Found,
		"branch":            access.Branch,
		"commit":            access.Commit,
		"message":           access.Message,
		"needs_credentials": access.NeedsCredentials,

		// The URL as it will be cloned, which differs from the one typed when
		// a deploy key turned https into SSH or a token the other way.
		"repository": site.Repository,
	})
}

// sitesRepositoryKey makes a deploy key for a site that is still a form.
func (a *App) sitesRepositoryKey(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	id, public, err := sites.GeneratePendingDeployKey(input["domain"])
	if err != nil {
		a.Log.Error("could not generate a deploy key for the create form", "error", err)
		a.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"key_id": id, "public_key": public})
}
