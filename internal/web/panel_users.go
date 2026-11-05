package web

import (
	"context"
	"net/http"
	"net/mail"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// Panel users: more than one person signing in, each with their own password
// and two-factor, as an administrator (everything) or read-only (every page,
// nothing changed, and none of what would let them change things anyway: the
// terminal, phpMyAdmin, database passwords, a site's files).

func (a *App) panelUsers(ctx context.Context) []map[string]any {
	list := []map[string]any{}

	users, err := a.Store.Users(ctx)
	if err != nil {
		return list
	}

	for _, user := range users {
		role := user.Role
		if role == "" {
			role = store.RoleAdmin
		}
		list = append(list, map[string]any{"id": user.ID, "name": user.Name, "email": user.Email, "role": role})
	}

	return list
}

func validRole(role string) bool { return role == store.RoleAdmin || role == store.RoleViewer }

func (a *App) panelUserStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := strings.TrimSpace(input["name"])
	email := strings.ToLower(strings.TrimSpace(input["email"]))
	password := input["password"]
	role := input["role"]

	ok := true
	if name == "" || len(name) > 100 {
		sess.Error("name", "Give the account a name.")
		ok = false
	}
	if address, err := mail.ParseAddress(email); err != nil || address.Address != email {
		sess.Error("email", "That is not an email address.")
		ok = false
	}
	if len(password) < 12 {
		sess.Error("password", "Use at least 12 characters.")
		ok = false
	}
	if !validRole(role) {
		sess.Error("role", "Choose what the account may do.")
		ok = false
	}
	if !ok {
		inertia.Back(w, r, "/settings")
		return
	}

	user, err := a.Store.CreateUser(r.Context(), name, email, password)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			sess.Error("email", "There is already an account with that address.")
		} else {
			sess.Flash("error", "The account could not be created: "+err.Error())
		}
		inertia.Back(w, r, "/settings")
		return
	}

	if role != store.RoleAdmin {
		a.Store.SetUserRole(r.Context(), user.ID, role)
	}

	a.Log.Info("panel user created", "email", email, "role", role, "by", userFrom(r.Context()).Email, "ip", session.ClientIP(r))
	sess.Flash("success", name+" can now sign in as "+email+map[string]string{
		store.RoleAdmin: ", with full access.", store.RoleViewer: ", read-only.",
	}[role])
	inertia.Back(w, r, "/settings")
}

func (a *App) panelUserUpdate(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	id, ok := pathID(r, "user")
	input, err := a.input(r)
	if !ok || err != nil || !validRole(input["role"]) {
		a.badRequest(w, r, err)
		return
	}

	target, err := a.Store.FindUser(r.Context(), id)
	if err != nil {
		sess.Flash("error", "That account no longer exists.")
		inertia.Back(w, r, "/settings")
		return
	}

	if input["role"] == store.RoleViewer && target.IsAdmin() && a.Store.AdminCount(r.Context()) <= 1 {
		sess.Flash("error", "This is the only administrator. Make another account an administrator first.")
		inertia.Back(w, r, "/settings")
		return
	}

	if err := a.Store.SetUserRole(r.Context(), id, input["role"]); err != nil {
		sess.Flash("error", err.Error())
		inertia.Back(w, r, "/settings")
		return
	}

	a.Log.Info("panel user role changed", "email", target.Email, "role", input["role"], "ip", session.ClientIP(r))
	sess.Flash("success", target.Name+map[string]string{
		store.RoleAdmin: " is now an administrator.", store.RoleViewer: " is now read-only.",
	}[input["role"]])
	inertia.Back(w, r, "/settings")
}

func (a *App) panelUserDestroy(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	id, ok := pathID(r, "user")
	if !ok {
		a.badRequest(w, r, nil)
		return
	}

	if me := userFrom(r.Context()); me != nil && me.ID == id {
		sess.Flash("error", "You cannot remove your own account while signed in to it.")
		inertia.Back(w, r, "/settings")
		return
	}

	target, err := a.Store.FindUser(r.Context(), id)
	if err != nil {
		inertia.Back(w, r, "/settings")
		return
	}
	if target.IsAdmin() && a.Store.AdminCount(r.Context()) <= 1 {
		sess.Flash("error", "That is the only administrator.")
		inertia.Back(w, r, "/settings")
		return
	}

	if err := a.Store.DeleteUser(r.Context(), id); err != nil {
		sess.Flash("error", "The account could not be removed: "+err.Error())
		inertia.Back(w, r, "/settings")
		return
	}

	a.Log.Info("panel user removed", "email", target.Email, "ip", session.ClientIP(r))
	sess.Flash("success", target.Name+" can no longer sign in.")
	inertia.Back(w, r, "/settings")
}

func pathID(r *http.Request, name string) (int64, bool) {
	id := routeID(r, name)
	return id, id > 0
}
