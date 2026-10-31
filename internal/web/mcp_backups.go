package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The Backups tab, as tools.
//
// Same four things the page does — take one, list them, put one back, throw one
// away — and the schedule that decides when the panel takes them on its own.
// Every one of them calls the same App and Service methods the handlers call:
// an assistant asked to back a site up before upgrading it should end up with
// exactly the backup the button would have made.

func (a *App) mcpListSiteBackups() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_site_backups",
		Capability: capSitesRead,
		Title:      "List a site's backups",
		ReadOnly:   true,
		Description: "Every backup taken of one site, newest first: when it was taken, whether " +
			"the panel took it or somebody asked for it, what is in it, how big it is and " +
			"whether it finished. Also the automatic schedule the site is on.\n\n" +
			"This is what to read before restore_site_backup, and what to poll after " +
			"create_site_backup — a backup runs on the queue, so it is `pending` for as long " +
			"as it takes to copy the site.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			list, settings := a.backupProps(ctx, site)

			return map[string]any{
				"site_id":  site.ID,
				"domain":   site.Domain,
				"backups":  list,
				"schedule": settings,
			}, nil
		},
	}
}

func (a *App) mcpCreateSiteBackup() *mcp.Tool {
	return &mcp.Tool{
		Name:       "create_site_backup",
		Capability: capSitesWrite,
		Title:      "Back a site up",
		Description: "Take a copy of a site now: everything under its root directory, and the " +
			"database the panel made for it. Works for every kind of site — WordPress, Laravel, " +
			"a Node app, a plain static one — because what is copied is the directory and the " +
			"database rather than anything type-specific.\n\n" +
			"Do this before anything that could break a site and cannot be undone: a plugin or " +
			"framework upgrade, a bulk edit, a migration. Asynchronous — poll " +
			"list_site_backups until the newest one is `complete`.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"note": {Description: "What this copy is for, shown beside it on the Backups tab — " +
				"\"before the WooCommerce 9 upgrade\". Worth sending: it is what tells somebody " +
				"months later which backup is the one they want."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			user := mcpUser(ctx)

			userID := int64(0)
			if user != nil {
				userID = user.ID
			}

			id, err := a.QueueBackup(ctx, site, userID, "manual",
				truncate(strings.TrimSpace(args.String("note")), 120))
			if err != nil {
				return nil, err
			}

			backup, _ := a.Store.FindBackup(ctx, id)

			answer := map[string]any{
				"backup_id": id,
				"site_id":   site.ID,
				"domain":    site.Domain,
				"status":    "pending",
				"queued":    true,
				"next": fmt.Sprintf("Poll list_site_backups for site %d until backup %d is "+
					"`complete`. A large site takes minutes.", site.ID, id),
			}

			if backup != nil {
				answer["has_files"] = backup.HasFiles
				answer["database"] = nullable(backup.DatabaseName)

				if !backup.HasDatabase {
					answer["note"] = "This site has no database the panel created, so the backup " +
						"is its files alone. A database attached by hand is not the panel's to " +
						"copy — or to overwrite on a restore."
				}
			}

			return answer, nil
		},
	}
}

func (a *App) mcpRestoreSiteBackup() *mcp.Tool {
	return &mcp.Tool{
		Name:        "restore_site_backup",
		Capability:  capSitesWrite,
		Title:       "Put a site back from a backup",
		Destructive: true,
		Description: "Replace a site with one of its backups. Every file under the site's root " +
			"is replaced by the ones in the backup — files added since are removed — and the " +
			"database the panel made for it is emptied and reloaded from the dump.\n\n" +
			"There is no undo, and anything done to the site since that backup was taken is " +
			"gone. The site is offline while it runs. Take a fresh backup with " +
			"create_site_backup first if today's version might be wanted back, and tell the " +
			"person the date of the backup being restored before calling this with `confirm`.",
		Properties: map[string]mcp.Property{
			"site":   {Description: "The site's id, or its domain."},
			"backup": {Type: "integer", Description: "Which backup, from list_site_backups."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what is replaced and has said yes."},
		},
		Required: []string{"site", "backup"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, backup, err := a.mcpBackup(ctx, args)
			if err != nil {
				return nil, err
			}

			if backup.Status != "complete" {
				return nil, errors.New("that backup did not finish, so there is nothing to " +
					"restore from")
			}

			what := "every file under " + site.RootPath
			if backup.DatabaseName != "" {
				what += " and everything in the " + backup.DatabaseName + " database"
			}

			if err := a.requireConfirmation(args, fmt.Sprintf(
				"Replacing %s with the backup taken %s — %s. Anything changed since is lost",
				site.Domain, backup.CreatedAt, what)); err != nil {
				return nil, err
			}

			a.Queue.PushID(queue.SiteRestore, "backup", backup.ID)

			return map[string]any{
				"site_id":   site.ID,
				"domain":    site.Domain,
				"backup_id": backup.ID,
				"taken_at":  backup.CreatedAt,
				"queued":    true,
				"next": "The restore is running on the queue and the site is down until it " +
					"finishes. get_site says when its status is back to `active`; list_tasks " +
					"and get_task have the log if it is not.",
			}, nil
		},
	}
}

func (a *App) mcpDeleteSiteBackup() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_site_backup",
		Capability:  capSitesWrite,
		Title:       "Delete a backup",
		Destructive: true,
		Description: "Remove one backup from the server: its file archive and its database dump. " +
			"The site itself is untouched. Use it to free disk space, not to tidy up — the " +
			"automatic schedule already deletes its own old copies.",
		Properties: map[string]mcp.Property{
			"site":   {Description: "The site's id, or its domain."},
			"backup": {Type: "integer", Description: "Which backup, from list_site_backups."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told which copy goes and has said yes."},
		},
		Required: []string{"site", "backup"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, backup, err := a.mcpBackup(ctx, args)
			if err != nil {
				return nil, err
			}

			if err := a.requireConfirmation(args, fmt.Sprintf(
				"Deleting the backup of %s taken %s. It cannot be recovered",
				site.Domain, backup.CreatedAt)); err != nil {
				return nil, err
			}

			a.Sites.RemoveBackup(backup)

			return map[string]any{
				"site_id":   site.ID,
				"domain":    site.Domain,
				"backup_id": backup.ID,
				"deleted":   true,
			}, nil
		},
	}
}

func (a *App) mcpSiteBackupSchedule() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_backup_schedule",
		Capability: capSitesWrite,
		Title:      "Set how often a site backs itself up",
		Description: "Put a site on an automatic backup schedule, or take it off one. The panel " +
			"takes the backup itself and deletes its own old ones once there are more than " +
			"`keep`.\n\n" +
			"Only the automatic backups are counted and only they are deleted — one somebody " +
			"asked for is kept until it is deleted on purpose. Called with no `schedule`, it " +
			"reads the current one back.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"schedule": {Description: "How often.",
				Enum: []string{"off", "daily", "weekly", "monthly"}},
			"keep": {Type: "integer", Default: 7,
				Description: "How many automatic backups to keep, 1 to 30. A large site kept " +
					"thirty times over is a full disk."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			schedule := strings.ToLower(strings.TrimSpace(args.String("schedule")))

			if schedule == "" {
				_, settings := a.backupProps(ctx, site)

				return map[string]any{
					"site_id":  site.ID,
					"domain":   site.Domain,
					"schedule": settings,
				}, nil
			}

			if _, known := sites.BackupSchedules[schedule]; !known && schedule != "off" {
				return nil, errors.New("the schedule is off, daily, weekly or monthly")
			}

			keep := int(args.Int("keep"))

			if !args.Has("keep") {
				keep = 7
			}

			if keep < 1 || keep > 30 {
				return nil, errors.New("keep between 1 and 30 backups")
			}

			next := int64(0)

			if schedule != "off" {
				next = sites.NextBackupAt(schedule, time.Now())
			}

			a.Store.SetSiteBackupSchedule(site.ID, schedule, keep, next, site.BackupDestination)

			fresh, err := a.Store.FindSite(ctx, site.ID)
			if err != nil {
				fresh = site
			}

			_, settings := a.backupProps(ctx, fresh)

			return map[string]any{
				"site_id":  site.ID,
				"domain":   site.Domain,
				"schedule": settings,
				"written":  true,
			}, nil
		},
	}
}

// mcpBackup is the backup named in the arguments, checked against the site
// named alongside it.
//
// Checked, not trusted. Without it a wrong number in `backup` is a way to
// replace one site with another site's files, which is the single worst thing
// this set of tools could be made to do.
func (a *App) mcpBackup(ctx context.Context, args mcp.Args) (*store.Site, *store.Backup, error) {
	site, err := a.mcpSite(ctx, args)
	if err != nil {
		return nil, nil, err
	}

	id := args.Int("backup")
	if id <= 0 {
		return nil, nil, errors.New("name the backup by its id, from list_site_backups")
	}

	backup, err := a.Store.FindBackup(ctx, id)
	if err != nil || backup.SiteID != site.ID {
		return nil, nil, fmt.Errorf("%s has no backup with id %d — list_site_backups has its own",
			site.Domain, id)
	}

	return site, backup, nil
}
