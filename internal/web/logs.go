package web

import (
	"net/http"
	"strconv"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
)

// logsIndex is every task the panel has run, newest first.
func (a *App) logsIndex(w http.ResponseWriter, r *http.Request) {
	const perPage = 25

	kind := r.URL.Query().Get("type")

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}

	// Read one page's worth plus everything before it, then slice: the table
	// is small — one row per deployment — and paging it in SQL would be a
	// second query for the count.
	all, err := a.Store.RecentTasks(r.Context(), 1000)
	if err != nil {
		a.serverError(w, r, "could not read the logs", err)
		return
	}

	filtered := make([]map[string]any, 0, len(all))

	for _, task := range all {
		if kind != "" && task.Type != kind {
			continue
		}

		filtered = append(filtered, map[string]any{
			"id":          task.ID,
			"type":        task.Type,
			"action":      task.Action,
			"status":      task.Status,
			"message":     task.Message,
			"progress":    task.Progress,
			"started_at":  task.StartedAt,
			"finished_at": task.FinishedAt,
			"created_at":  task.CreatedAt,
		})
	}

	total := len(filtered)
	pages := (total + perPage - 1) / perPage

	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}

	start := (page - 1) * perPage
	end := start + perPage

	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	a.Render.Render(w, r, "Logs/Index", inertia.Props{
		"logs": filtered[start:end],
		"pagination": map[string]any{
			"page":  page,
			"pages": pages,
			"total": total,
		},
		"type": nullable(kind),
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
