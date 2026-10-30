package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// logsIndex is everything that has happened to the panel and the machine,
// newest first: the tasks the panel ran, every sign-in to the panel, and every
// SSH login — with failed SSH attempts under their own filter.
func (a *App) logsIndex(w http.ResponseWriter, r *http.Request) {
	const perPage = 25

	kind := r.URL.Query().Get("type")

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}

	entries, total, err := a.Store.Timeline(r.Context(), kind, perPage, (page-1)*perPage)
	if err != nil {
		a.serverError(w, r, "could not read the logs", err)
		return
	}

	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}

	// Past the end — a filter that has fewer pages than the one being read —
	// shows the last page rather than an empty one.
	if page > pages {
		page = pages

		entries, _, err = a.Store.Timeline(r.Context(), kind, perPage, (page-1)*perPage)
		if err != nil {
			a.serverError(w, r, "could not read the logs", err)
			return
		}
	}

	a.Render.Render(w, r, "Logs/Index", inertia.Props{
		"logs": entries,
		"pagination": map[string]any{
			"page":  page,
			"pages": pages,
			"total": total,
		},
		"type":             nullable(kind),
		"retention":        a.logRetentionDays(),
		"retentionOptions": logRetentionOptions,
	})
}

// logsShow is one task, with its console.
func (a *App) logsShow(w http.ResponseWriter, r *http.Request) {
	task, err := a.Store.FindTask(r.Context(), routeID(r, "task"))
	if err != nil {
		a.notFoundError(w, r, "No such log.")
		return
	}

	a.Render.Render(w, r, "Logs/Show", inertia.Props{
		"task": task.Console(0),
	})
}

// tasksShow is what the live console polls. `offset` trims the output the
// browser already has; an offset past the end asks for the status line alone.
func (a *App) tasksShow(w http.ResponseWriter, r *http.Request) {
	task, err := a.Store.FindTask(r.Context(), routeID(r, "task"))
	if err != nil {
		a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such task."})
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	a.writeJSON(w, http.StatusOK, task.Console(offset))
}

// tasksLatest is the most recent task of a type, for a page that has just
// dispatched one and does not know its id yet.
func (a *App) tasksLatest(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("type")

	var id int64

	if kind == "" {
		id = a.Store.LatestTaskID(r.Context(), "provision", false)
	} else {
		id = a.Store.LatestTaskID(r.Context(), kind, false)
	}

	console := a.Store.TaskConsole(r.Context(), id, 0)

	if console == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"task": nil})
		return
	}

	a.writeJSON(w, http.StatusOK, console)
}

// ---------------------------------------------------------------- retention

// logRetentionSetting is how many days of logs the panel keeps: the tasks it
// ran, the sign-ins to it, and the SSH logins and attempts it copied out of
// sshd's log. Older ones are deleted every hour, so the database stops growing
// once it holds that many days.
const logRetentionSetting = "log_retention_days"

var logRetentionOptions = []int{7, 30, 90, 180, 365}

const defaultLogRetention = 90

func (a *App) logRetentionDays() int {
	days, err := strconv.Atoi(a.Store.Setting(logRetentionSetting, ""))
	if err != nil || !slices.Contains(logRetentionOptions, days) {
		return defaultLogRetention
	}

	return days
}

var lastPrune struct {
	sync.Mutex
	at time.Time
}

// pruneLogs deletes what is older than the retention, at most once an hour.
// Called from the minute clock; the hour is what keeps it from being a delete
// across three tables every minute.
func (a *App) pruneLogs(ctx context.Context) {
	lastPrune.Lock()
	if time.Since(lastPrune.at) < time.Hour {
		lastPrune.Unlock()
		return
	}
	lastPrune.at = time.Now()
	lastPrune.Unlock()

	before := time.Now().AddDate(0, 0, -a.logRetentionDays())

	if removed := a.Store.ClearLogs(ctx, []string{store.LogsTasks, store.LogsLogins, store.LogsSSH}, before); removed > 0 {
		a.Log.Info("old logs removed", "rows", removed, "older_than_days", a.logRetentionDays())
	}
}

// logsRetention sets how long logs are kept, and applies it now rather than
// within the hour.
func (a *App) logsRetention(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	days, err := strconv.Atoi(input["days"])
	if err != nil || !slices.Contains(logRetentionOptions, days) {
		a.failBack(w, r, "That is not one of the periods the panel keeps logs for.")
		return
	}

	a.Store.SetSetting(logRetentionSetting, strconv.Itoa(days))

	lastPrune.Lock()
	lastPrune.at = time.Time{}
	lastPrune.Unlock()

	a.pruneLogs(r.Context())

	a.okBack(w, r, fmt.Sprintf("Logs are kept for %d days now; anything older was removed.", days))
}

// logsClear deletes logs on request: all of them, or one kind, and either
// everything or only what is older than a number of days. The database is
// compacted afterwards, so the space is actually given back.
func (a *App) logsClear(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	kinds := map[string][]string{
		"all":    {store.LogsTasks, store.LogsLogins, store.LogsSSH},
		"tasks":  {store.LogsTasks},
		"logins": {store.LogsLogins},
		"ssh":    {store.LogsSSH},
	}[input["what"]]

	if kinds == nil {
		a.failBack(w, r, "Choose which logs to clear.")
		return
	}

	var before time.Time
	if days, err := strconv.Atoi(input["older_than"]); err == nil && days > 0 {
		before = time.Now().AddDate(0, 0, -days)
	}

	removed := a.Store.ClearLogs(r.Context(), kinds, before)

	if err := a.Store.Compact(r.Context()); err != nil {
		a.Log.Warn("could not compact the database after clearing logs", "error", err)
	}

	a.Log.Info("logs cleared", "what", input["what"], "older_than", input["older_than"], "rows", removed)

	if removed == 0 {
		a.okBack(w, r, "There was nothing to clear.")
		return
	}

	a.okBack(w, r, fmt.Sprintf("Cleared %d log entries.", removed))
}
