package web

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

// Staging, as tools.
//
// An assistant asked to "try the upgrade somewhere safe first" has no way to do
// that without these: it would otherwise reach for create_site, which builds a
// fresh application rather than a copy of this one, and then be asked why the
// staging site has none of the content.

func (a *App) mcpCreateStagingSite() *mcp.Tool {
	return &mcp.Tool{
		Name:       "create_staging_site",
		Capability: capSitesWrite,
		Title:      "Clone a site to a staging hostname",
		Description: "Make a complete copy of a site on a hostname of its own: its files, its " +
			"database, its own nginx vhost and its own certificate. The copy's URLs are " +
			"rewritten as part of the clone — for WordPress that means every table, " +
			"including inside serialised data — so it does not redirect back to the site " +
			"it came from. Queued; read_task follows it. Use push_staging_site to put the " +
			"changes back. This is not create_site: that builds a fresh application.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The production site's domain or id."},
			"domain": {Description: "The hostname for the copy. Left out, a `staging.` " +
				"subdomain of the site is used, which is in the same DNS zone and so " +
				"needs nothing done at a registrar."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			source, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if why := stagingSupported(source); why != "" {
				return nil, errors.New(why)
			}

			if source.IsStaging() {
				return nil, errors.New("that site is already a staging copy — clone the " +
					"production site instead, or push this one up first")
			}

			domain := strings.ToLower(strings.TrimSpace(args.StringOr("domain", "")))
			if domain == "" {
				domain = sites.SuggestStagingDomain(source.Domain)
			}

			staging, err := a.prepareStagingSite(ctx, user, source, domain)
			if err != nil {
				return nil, err
			}

			id, err := a.Store.CreateSite(ctx, staging)
			if err != nil || id <= 0 {
				if err == nil {
					err = errors.New("the staging site record was not created")
				}

				return nil, err
			}

			taskID, _ := a.Queue.PushID(queue.SiteStagingCreate, "site", id)

			return map[string]any{
				"id":       id,
				"domain":   staging.Domain,
				"url":      staging.URL(),
				"clone_of": source.Domain,
				"job":      taskID,
				"note": fmt.Sprintf("Cloning %s to %s. It gets none of %s's aliases and "+
					"its cache starts off, so the two cannot be served from each other's "+
					"directory or read each other's cached pages.",
					source.Domain, staging.Domain, source.Domain),
			}, nil
		},
	}
}

func (a *App) mcpPushStagingSite() *mcp.Tool {
	return &mcp.Tool{
		Name:        "push_staging_site",
		Capability:  capSitesWrite,
		Title:       "Push a staging site to production",
		Destructive: true,
		Description: "Copy a staging site back over the site it was cloned from, rewriting " +
			"the URLs on the way so what arrives believes it lives there. The production " +
			"site is backed up first and the push does not start if that backup fails. " +
			"Replaces what is on production rather than merging: with `database` on, " +
			"everything written there since the clone was taken is gone. Queued; " +
			"read_task follows it.",
		Properties: map[string]mcp.Property{
			"site":  {Description: "The staging site's domain or id."},
			"files": {Type: "boolean", Description: "Push the files. Default true."},
			"database": {Type: "boolean", Description: "Push the database, replacing " +
				"production's. Default false, because this is the half that destroys " +
				"content somebody else created."},
			"keep_uploads": {Type: "boolean", Description: "WordPress: leave " +
				"wp-content/uploads on production as it is rather than replacing it with " +
				"the staging copy's. Default true — anything uploaded to production since " +
				"the clone exists in no backup of staging."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			staging, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if !staging.IsStaging() {
				return nil, fmt.Errorf("%s is not a staging copy, so there is nothing to "+
					"push it to", staging.Domain)
			}

			target, err := a.Store.FindSite(ctx, staging.StagingOf)
			if err != nil {
				return nil, errors.New("the site this was a staging copy of no longer exists")
			}

			options := sites.StagingOptions{
				Files:       args.Bool("files", true),
				Database:    args.Bool("database", false),
				KeepUploads: args.Bool("keep_uploads", true),
			}

			if !options.Files && !options.Database {
				return nil, errors.New("a push has to move the files, the database, or both")
			}

			if options.Database && !target.NeedsDatabase() {
				options.Database = false
			}

			taskID, err := a.Queue.Push(queue.SiteStagingPush, map[string]any{
				"site":         staging.ID,
				"files":        options.Files,
				"database":     options.Database,
				"keep_uploads": options.KeepUploads,
			})
			if err != nil {
				return nil, err
			}

			moving := []string{}

			if options.Files {
				moving = append(moving, "files")
			}

			if options.Database {
				moving = append(moving, "database")
			}

			return map[string]any{
				"from":     staging.Domain,
				"to":       target.Domain,
				"moving":   moving,
				"job":      taskID,
				"backedup": true,
				"note": fmt.Sprintf("%s is backed up before anything is replaced; "+
					"restoring that backup undoes the push. It is down for as long as "+
					"the copy takes.", target.Domain),
			}, nil
		},
	}
}

func (a *App) mcpListStagingSites() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_staging_sites",
		Capability:  capSitesRead,
		Title:       "List a site's staging copies",
		ReadOnly:    true,
		Description: "The staging copies of one site, or — for a staging site — what it is a copy of.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's domain or id."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if site.IsStaging() {
				parent, err := a.Store.FindSite(ctx, site.StagingOf)
				if err != nil {
					return map[string]any{
						"is_staging": true,
						"clone_of":   nil,
						"note": "This is a staging copy, but the site it came from no " +
							"longer exists, so there is nothing to push to.",
					}, nil
				}

				return map[string]any{
					"is_staging": true,
					"clone_of": map[string]any{
						"id":     parent.ID,
						"domain": parent.Domain,
						"status": parent.Status,
					},
				}, nil
			}

			copies, err := a.Store.StagingSitesFor(ctx, site.ID)
			if err != nil {
				return nil, err
			}

			rows := make([]map[string]any, 0, len(copies))

			for _, clone := range copies {
				rows = append(rows, map[string]any{
					"id":         clone.ID,
					"domain":     clone.Domain,
					"url":        clone.URL(),
					"status":     clone.Status,
					"created_at": clone.CreatedAt,
				})
			}

			return map[string]any{
				"is_staging":       false,
				"copies":           rows,
				"can_clone":        stagingSupported(site) == "",
				"why_not":          nullable(stagingSupported(site)),
				"suggested_domain": sites.SuggestStagingDomain(site.Domain),
			}, nil
		},
	}
}
