package web

import (
	"net/http"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/terminal"
)

// terminalPage is the shell in the browser.
func (a *App) terminalPage(w http.ResponseWriter, r *http.Request) {
	a.Render.Render(w, r, "System/Terminal", inertia.Props{
		"system": a.systemSummary(r.Context()),
	})
}

// terminalTicket hands out the one-shot token the websocket redeems.
//
// A path comes back rather than an absolute URL: it is resolved in the browser,
// so it stays correct whether the panel is reached by IP or by hostname, over
// TLS or not — and a loopback address here could only ever mean the machine the
// *browser* is on.
func (a *App) terminalTicket(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, _ := a.input(r)

	ticket, err := a.Store.IssueTicket(r.Context(), user.ID, input["mode"])
	if err != nil {
		a.Log.Error("could not issue a terminal ticket", "error", err)
		a.writeJSON(w, http.StatusInternalServerError,
			map[string]any{"error": "Could not issue a terminal ticket."})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"ticket":     ticket,
		"url":        terminal.Path,
		"expires_in": int(store.TicketTTL.Seconds()),
	})
}
