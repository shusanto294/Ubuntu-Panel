package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/serverlog"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// logsIndex is the Logs page: every entry the machine's own programs logged —
// nginx, PHP, the databases, fail2ban, sudo, the kernel, mail and each site
// (events.go). The traffic those logs add up to is the Analytics page's
// (analytics.go).
//
// Only the server's: the panel's own history is on each site and task, and
// sign-ins on the Security page.
func (a *App) logsIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	flat := map[string]string{}
	for key := range query {
		flat[key] = query.Get(key)
	}

	a.freshLogs(r.Context())

	props := inertia.Props{
		"retention":        a.logRetentionDays(),
		"retentionOptions": logRetentionOptions,
		"clearSources":     a.clearSources(),
	}

	for key, value := range a.logsEventsProps(r.Context(), flat, "server") {
		props[key] = value
	}

	a.Render.Render(w, r, "Logs/Index", props)
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

	a.Store.PruneNotifications(ctx, before)

	if removed := a.Store.ClearLogs(ctx, []string{store.LogsTasks, store.LogsLogins, store.LogsSSH, store.LogsServer}, before); removed > 0 {
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

// clearSources are the kinds of log the Clear form offers, by key.
func (a *App) clearSources() []map[string]string {
	list := []map[string]string{
		{"key": "all", "label": "Everything"},
		{"key": "tasks", "label": "Panel activity"},
		{"key": "logins", "label": "Panel sign-ins"},
		{"key": "ssh", "label": "SSH logins and attempts"},
		{"key": "server", "label": "Everything the server logged"},
	}

	for _, key := range []string{serverlog.SourceRequests, serverlog.SourceSite, serverlog.SourceNginx,
		serverlog.SourcePHP, serverlog.SourceDatabase, serverlog.SourceFail2ban, serverlog.SourceAuth,
		serverlog.SourceKernel, serverlog.SourceSystem, serverlog.SourceMail} {
		list = append(list, map[string]string{"key": "server:" + key, "label": "Server · " + serverlog.SourceLabels[key]})
	}

	return list
}

// logsClear deletes logs on request: every kind or one, everything or only
// what is older than a number of days, and — when asked — empties the log
// files on disk the server's entries were read from, too. The database is
// compacted afterwards, so the space is actually given back.
func (a *App) logsClear(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	what := input["what"]

	var (
		kinds   []string
		sources []string // server sources; nil with server set means all of them
		server  bool
	)

	switch {
	case what == "all":
		kinds, server = []string{store.LogsTasks, store.LogsLogins, store.LogsSSH}, true
	case what == "tasks" || what == "logins" || what == "ssh":
		kinds = []string{what}
	case what == "server":
		server = true
	case strings.HasPrefix(what, "server:") && serverlog.SourceLabels[strings.TrimPrefix(what, "server:")] != "":
		server, sources = true, []string{strings.TrimPrefix(what, "server:")}
	default:
		a.failBack(w, r, "Choose which logs to clear.")
		return
	}

	var before time.Time
	if days, err := strconv.Atoi(input["older_than"]); err == nil && days > 0 {
		before = time.Now().AddDate(0, 0, -days)
	}

	removed := a.Store.ClearLogs(r.Context(), kinds, before)
	if server {
		removed += a.Store.ClearServerEvents(r.Context(), sources, before)
	}

	// The files themselves, only for "everything" in time: a file cannot be
	// emptied of only its older lines.
	emptied := 0
	if server && boolInput(input, "files", false) && before.IsZero() {
		for _, file := range a.collectFiles(r.Context()) {
			if sources != nil && !slices.Contains(sources, file.Source) {
				continue
			}
			if err := clearLogFile(file.Path); err != nil {
				a.Log.Warn("could not empty a log file", "path", file.Path, "error", err)
				continue
			}
			emptied++
		}
	}

	if err := a.Store.Compact(r.Context()); err != nil {
		a.Log.Warn("could not compact the database after clearing logs", "error", err)
	}

	a.Log.Info("logs cleared", "what", what, "older_than", input["older_than"], "rows", removed, "files", emptied)

	switch {
	case removed == 0 && emptied == 0:
		a.okBack(w, r, "There was nothing to clear.")
	case emptied > 0:
		a.okBack(w, r, fmt.Sprintf("Cleared %d log entries and emptied %d log files.", removed, emptied))
	default:
		a.okBack(w, r, fmt.Sprintf("Cleared %d log entries.", removed))
	}
}
