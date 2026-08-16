package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The Backups tab: taking a copy of a site, keeping some of them, and putting
// one back.

// QueueBackup writes the row and pushes the work.
//
// One function because the page, the MCP tool and the panel's own scheduler all
// want the same two things to happen in the same order — the row first, so that
// a backup is visible as `pending` from the moment it is asked for rather than
// from the moment a worker picks it up.
func (a *App) QueueBackup(ctx context.Context, site *store.Site, userID int64,
	kind, note string) (int64, error) {
	if site.Status == "deleting" {
		return 0, errors.New("this site is being removed")
	}

	backup := &store.Backup{
		SiteID:   site.ID,
		UserID:   userID,
		Kind:     kind,
		Note:     note,
		HasFiles: true,
	}

	// Only a database the panel made for this site. One somebody pointed the
	// site at by hand is not the panel's to dump, and it is certainly not the
	// panel's to drop and reload during a restore.
	if site.DatabaseID > 0 {
		if database, err := a.Store.FindDatabase(ctx, site.DatabaseID); err == nil && database.ManagedBySite {
			backup.HasDatabase = true
			backup.DatabaseEngine = database.Engine
			backup.DatabaseName = database.Name
		}
	}

	id, err := a.Store.CreateBackup(ctx, backup)
	if err != nil || id <= 0 {
		a.Log.Error("could not record a backup", "site", site.ID, "error", err)

		return 0, errors.New("the panel could not record the backup")
	}

	a.Queue.PushID(queue.SiteBackup, "backup", id)

	return id, nil
}

// sitesBackupStore is the "Back up now" button.
func (a *App) sitesBackupStore(w http.ResponseWriter, r *http.Request) {
	site, ok := a.backupSite(w, r)
	if !ok {
		return
	}

	user := userFrom(r.Context())

	userID := int64(0)
	if user != nil {
		userID = user.ID
	}

	input, _ := a.input(r)

	if _, err := a.QueueBackup(r.Context(), site, userID, "manual",
		truncate(strings.TrimSpace(input["note"]), 120)); err != nil {
		a.failBack(w, r, err.Error())

		return
	}

	a.okBack(w, r, "Backing up "+site.Domain+" — the log has it as it runs.")
}

// sitesBackupRestore puts one back.
func (a *App) sitesBackupRestore(w http.ResponseWriter, r *http.Request) {
	site, ok := a.backupSite(w, r)
	if !ok {
		return
	}

	backup, ok := a.backupFor(w, r, site)
	if !ok {
		return
	}

	if backup.Status != "complete" {
		a.failBack(w, r, "That backup did not finish, so there is nothing to restore from.")

		return
	}

	a.Queue.PushID(queue.SiteRestore, "backup", backup.ID)

	a.okBack(w, r, "Restoring "+site.Domain+" from the backup taken "+backup.CreatedAt+
		". The site is down until it finishes.")
}

// sitesBackupDestroy removes one.
func (a *App) sitesBackupDestroy(w http.ResponseWriter, r *http.Request) {
	site, ok := a.backupSite(w, r)
	if !ok {
		return
	}

	backup, ok := a.backupFor(w, r, site)
	if !ok {
		return
	}

	a.Sites.RemoveBackup(backup)

	a.okBack(w, r, "The backup was deleted.")
}

// sitesBackupSchedule sets how often the panel takes one on its own.
func (a *App) sitesBackupSchedule(w http.ResponseWriter, r *http.Request) {
	site, ok := a.backupSite(w, r)
	if !ok {
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	schedule := strings.ToLower(strings.TrimSpace(input["schedule"]))
	keep, _ := strconv.Atoi(strings.TrimSpace(input["keep"]))

	if schedule == "" {
		schedule = "off"
	}

	if _, known := sites.BackupSchedules[schedule]; !known && schedule != "off" {
		a.failBack(w, r, "Choose off, daily, weekly or monthly.")

		return
	}

	// A number rather than a limit somebody has to think about: one is a
	// working schedule, and thirty of a large site is a full disk.
	if keep < 1 {
		keep = 1
	}

	if keep > 30 {
		keep = 30
	}

	next := int64(0)

	if schedule != "off" {
		// From now rather than from the last backup, so switching a site onto a
		// schedule does not immediately fire one for a site backed up an hour
		// ago — and switching it back on after a pause does not fire a burst of
		// the ones it missed.
		next = sites.NextBackupAt(schedule, time.Now())
	}

	a.Store.SetSiteBackupSchedule(site.ID, schedule, keep, next)

	if schedule == "off" {
		a.okBack(w, r, "Automatic backups are off for "+site.Domain+
			". The ones already taken are kept.")

		return
	}

	a.okBack(w, r, site.Domain+" is backed up "+schedule+", keeping the newest "+
		strconv.Itoa(keep)+".")
}

// backupSite is the site in the URL, or a 404.
func (a *App) backupSite(w http.ResponseWriter, r *http.Request) (*store.Site, bool) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))

	if err != nil || site.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")

		return nil, false
	}

	return site, true
}

// backupFor is the backup in the URL, checked against the site in it.
//
// The check is the point. Without it the id in the URL is a way to restore any
// site from any other site's backup, which is one mistyped number away from
// replacing a live site with somebody else's files.
func (a *App) backupFor(w http.ResponseWriter, r *http.Request, site *store.Site) (*store.Backup, bool) {
	backup, err := a.Store.FindBackup(r.Context(), routeID(r, "backup"))

	if err != nil || backup.SiteID != site.ID {
		a.notFoundError(w, r, "No such backup for this site.")

		return nil, false
	}

	return backup, true
}

// ----------------------------------------------------------------- scheduler

// runBackupSchedules takes the backups that are due, for ever.
//
// A ticker in the panel rather than a line in crontab. The panel is what knows
// which sites are on a schedule, what to keep and where the copies go, and a
// cron entry would be a second place holding half of that — one that keeps
// firing for a site somebody deleted from the panel a month ago.
//
// A machine that was switched off overnight takes the backup it missed the
// moment it is back, rather than waiting a whole day for the next window:
// `backup_next_at` is a due time, and a due time in the past is still due.
func (a *App) runBackupSchedules(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			a.takeDueBackups(ctx)
		}
	}
}

// takeDueBackups queues one backup for every site whose turn it is.
//
// The due time moves on before the work is queued, not after it. A backup that
// fails is a failed row somebody can see; a due time that never moved is the
// same site being queued again every minute for ever.
func (a *App) takeDueBackups(ctx context.Context) {
	due, err := a.Store.DueBackups(ctx, time.Now())
	if err != nil {
		a.Log.Warn("could not look for sites due a backup", "error", err)

		return
	}

	for _, site := range due {
		a.Store.SetSiteBackupNext(site.ID, sites.NextBackupAt(site.BackupSchedule, time.Now()))

		if _, err := a.QueueBackup(ctx, site, site.UserID, "scheduled", ""); err != nil {
			a.Log.Warn("could not queue a scheduled backup", "site", site.Domain, "error", err)

			continue
		}

		a.Log.Info("queued a scheduled backup", "site", site.Domain,
			"schedule", site.BackupSchedule)
	}
}

// backupProps is what the Backups tab reads.
func (a *App) backupProps(ctx context.Context, site *store.Site) (any, map[string]any) {
	list := []map[string]any{}

	backups, err := a.Store.BackupsForSite(ctx, site.ID)
	if err != nil {
		a.Log.Warn("could not read a site's backups", "site", site.Domain, "error", err)
	}

	var total int64

	for _, backup := range backups {
		list = append(list, backup.Summary())
		total += backup.SizeBytes
	}

	schedule := site.BackupSchedule
	if schedule == "" {
		schedule = "off"
	}

	keep := site.BackupKeep
	if keep < 1 {
		keep = 7
	}

	settings := map[string]any{
		"schedule": schedule,
		"keep":     keep,
		"next_at":  nil,
		"root":     sites.BackupRoot + "/" + site.Domain,
		"total":    total,
		// Whether a backup would contain a database at all. A static site's
		// would not, and the tab says so rather than leaving somebody to wonder
		// what happened to it.
		"database": nil,
	}

	if schedule != "off" && site.BackupNextAt > 0 {
		settings["next_at"] = time.Unix(site.BackupNextAt, 0).UTC().Format(time.RFC3339)
	}

	if site.DatabaseID > 0 {
		if database, err := a.Store.FindDatabase(ctx, site.DatabaseID); err == nil && database.ManagedBySite {
			settings["database"] = database.Name
		}
	}

	return list, settings
}
