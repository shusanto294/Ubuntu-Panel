package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

// Git deployment, as one tool.
//
// One rather than six because the whole thing is a single subject with a single
// question behind it — "can the panel read this repository, and what does it do
// when it changes" — and an assistant that has to call `set_repository`,
// `set_git_auth` and `test_git_access` in the right order to find out will get
// the order wrong.
func (a *App) mcpSiteGit() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_git",
		Capability: capSitesWrite,
		Title:      "Read or change where a site is deployed from",
		Description: "The repository a site is deployed from, how the panel authenticates to " +
			"it, and what it does when somebody pushes.\n\n" +
			"Called with `site` alone it reads all of that back and tests the connection, " +
			"which is how to find out why a deployment failed on its clone. Called with " +
			"anything else it changes it and then tests.\n\n" +
			"Private repositories work two ways. A deploy key is generated here and its " +
			"public half has to be added to the repository by a person — the answer carries " +
			"it, and the panel cannot do that step. A token is pasted in and works " +
			"immediately. Prefer the key and say plainly that it needs adding; a repository " +
			"reports itself as 'not found' to anybody who cannot see it, so an unexpected " +
			"'not found' is nearly always a missing credential rather than a wrong URL.\n\n" +
			"`pull: true` deploys the current head now.",
		Properties: map[string]mcp.Property{
			"site":       {Description: "The site's domain or id."},
			"repository": {Description: "The repository URL — https or SSH. An empty string takes the site off git."},
			"branch":     {Description: "The branch to deploy. Defaults to main."},
			"auth": {Description: "How to authenticate: none for a public repository, " +
				"key for a generated deploy key, token for a personal access token.",
				Enum: []string{"none", "key", "token"}},
			"token": {Description: "The access token, for auth=token. Stored encrypted and never read back."},
			"auto_pull": {Description: "Poll the repository on this interval and pull when the " +
				"commit changes: off, 5m, 15m, hourly, daily.",
				Enum: []string{"off", "5m", "15m", "hourly", "daily"}},
			"webhook": {Type: "boolean", Description: "Switch the push webhook on or off. " +
				"On, the answer carries the URL and secret for somebody to paste into the " +
				"repository's settings — which is a step only a person can do."},
			"pull": {Type: "boolean", Description: "Pull and redeploy the branch's current head now."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			changes := []string{}

			// The source, first: everything else is about a repository, so a
			// wrong one makes the rest meaningless.
			if value, sent := args["repository"]; sent {
				repository, refusal := NormaliseRepositoryInput(fmt.Sprint(value))
				if refusal != "" {
					return nil, errors.New(refusal)
				}

				branch := strings.TrimSpace(args.StringOr("branch", site.Branch))
				if branch == "" {
					branch = "main"
				}

				a.Store.SetSiteRepository(site.ID, repository, branch)

				site.Repository = repository
				site.Branch = branch

				changes = append(changes, "repository")
			} else if branch := strings.TrimSpace(args.StringOr("branch", "")); branch != "" &&
				branch != site.Branch {
				a.Store.SetSiteRepository(site.ID, site.Repository, branch)

				site.Branch = branch

				changes = append(changes, "branch")
			}

			if method := strings.TrimSpace(args.StringOr("auth", "")); method != "" {
				if !sites.ValidRepoAuth(method) {
					return nil, fmt.Errorf("%q is not a way the panel authenticates", method)
				}

				token := strings.TrimSpace(args.StringOr("token", ""))

				if method == sites.AuthToken && token == "" && site.RepoToken == "" {
					return nil, errors.New("auth=token needs a token")
				}

				if err := a.Store.SetSiteRepoAuth(site.ID, method, token); err != nil {
					return nil, err
				}

				site.RepoAuth = method

				if token != "" {
					site.RepoToken = token
				}

				changes = append(changes, "access")

				// The key is generated here and nowhere else, so it has to
				// happen as part of choosing the method.
				if method == sites.AuthKey && site.RepoDeployKey == "" {
					key, err := sites.GenerateDeployKey(site)
					if err != nil {
						return nil, err
					}

					a.Store.SetSiteDeployKey(site.ID, key)
					site.RepoDeployKey = key
				}
			}

			if schedule := strings.TrimSpace(args.StringOr("auto_pull", "")); schedule != "" {
				if !sites.ValidPullSchedule(schedule) {
					return nil, fmt.Errorf("%q is not an interval the panel offers", schedule)
				}

				a.Store.SetSiteAutoPull(site.ID, schedule,
					sites.NextPullAt(schedule, time.Now()))

				site.AutoPull = schedule

				changes = append(changes, "auto_pull")
			}

			if value, sent := args["webhook"]; sent {
				if args.Bool("webhook", false) {
					if site.WebhookSecret == "" {
						secret := crypto.Password(40)

						if err := a.Store.SetSiteWebhookSecret(site.ID, secret); err != nil {
							return nil, err
						}

						site.WebhookSecret = secret
					}
				} else {
					a.Store.SetSiteWebhookSecret(site.ID, "")
					site.WebhookSecret = ""
				}

				_ = value

				changes = append(changes, "webhook")
			}

			answer := map[string]any{
				"site":        site.Domain,
				"repository":  nullable(site.Repository),
				"branch":      orDefault(site.Branch, "main"),
				"auth":        sites.RepoAuthOf(site),
				"auto_pull":   orDefault(site.AutoPull, "off"),
				"last_commit": nullable(site.LastCommit),
				"changed":     changes,
			}

			// The two things a person has to go and do, and the only two.
			// Named `tell_the_person` because that is what the guide tells an
			// assistant these fields are for, and an assistant that quietly
			// drops a deploy key nobody ever pastes has configured nothing.
			todo := []string{}

			if sites.RepoAuthOf(site) == sites.AuthKey && site.RepoDeployKey != "" {
				answer["deploy_key"] = site.RepoDeployKey

				todo = append(todo, "Add this deploy key to the repository — on GitHub, "+
					"the repository's Settings · Deploy keys · Add deploy key. Read-only "+
					"is enough. On the repository rather than on the account, or this "+
					"server gets access to everything that account can see. Until it is "+
					"added, the panel cannot read the repository: "+site.RepoDeployKey)
			}

			if site.WebhookSecret != "" {
				answer["webhook_url"] = a.webhookURL(site)
				answer["webhook_secret"] = site.WebhookSecret

				todo = append(todo, fmt.Sprintf("Add a webhook to the repository: URL %s, "+
					"secret %s, content type application/json, push events only. Only a "+
					"push to %s deploys.", a.webhookURL(site), site.WebhookSecret,
					orDefault(site.Branch, "main")))
			}

			if len(todo) > 0 {
				answer["tell_the_person"] = todo
			}

			// Whether any of it works, which is the question underneath all of
			// the above.
			if strings.TrimSpace(site.Repository) != "" {
				access := sites.CheckAccess(site, site.Branch)

				answer["access"] = map[string]any{
					"ok":      access.OK,
					"branch":  access.Branch,
					"found":   access.Found,
					"commit":  nullable(access.Commit),
					"message": access.Message,
				}

				if args.Bool("pull", false) {
					if !access.OK || !access.Found {
						return nil, errors.New("the repository cannot be read, so a pull " +
							"would fail: " + access.Message)
					}

					if err := a.pullSiteNow(ctx, site); err != nil {
						return nil, err
					}

					answer["pulling"] = true
					answer["next"] = "Pulling " + access.Commit + " into " + site.Domain +
						". site_logs has the run; get_site's status says when it finishes."
				}
			}

			return answer, nil
		},
	}
}
