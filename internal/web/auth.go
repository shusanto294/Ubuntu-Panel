package web

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// minPasswordLength is deliberately longer than the usual eight. This account
// controls the whole server.
const minPasswordLength = 12

func (a *App) showSetup(w http.ResponseWriter, r *http.Request) {
	count, err := a.Store.UserCount(r.Context())
	if err != nil {
		a.serverError(w, r, "could not count accounts", err)
		return
	}

	// Setup is a one-time page. Once an account exists it is a sign-in page
	// with a misleading name, and leaving it open is a way to make a second
	// administrator.
	if count > 0 {
		inertia.Redirect(w, r, "/login")
		return
	}

	a.Render.Render(w, r, "Auth/Setup", inertia.Props{"panelName": config.Name})
}

func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	count, err := a.Store.UserCount(r.Context())
	if err != nil {
		a.serverError(w, r, "could not count accounts", err)
		return
	}
	if count > 0 {
		inertia.Redirect(w, r, "/login")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := strings.TrimSpace(input["name"])
	email := strings.TrimSpace(input["email"])
	password := input["password"]

	if name == "" {
		sess.Error("name", "Enter a name.")
	}
	if email == "" || !strings.Contains(email, "@") {
		sess.Error("email", "Enter a valid email address.")
	}
	if len(password) < minPasswordLength {
		sess.Error("password", "Use at least 12 characters. This account controls the whole server.")
	}
	if password != input["password_confirmation"] {
		sess.Error("password_confirmation", "The two passwords do not match.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/setup")
		return
	}

	user, err := a.Store.CreateUser(r.Context(), name, email, password)
	if err != nil {
		a.Log.Error("could not create the first account", "error", err)
		sess.Flash("error", "Could not create the account.")
		inertia.Back(w, r, "/setup")
		return
	}

	if err := sess.Login(r.Context(), user.ID); err != nil {
		a.serverError(w, r, "could not sign in the new account", err)
		return
	}

	a.Log.Info("first account created", "email", user.Email)

	inertia.Redirect(w, r, "/dashboard")
}

func (a *App) showLogin(w http.ResponseWriter, r *http.Request) {
	count, err := a.Store.UserCount(r.Context())
	if err != nil {
		a.serverError(w, r, "could not count accounts", err)
		return
	}

	// Nobody to sign in as yet.
	if count == 0 {
		inertia.Redirect(w, r, "/setup")
		return
	}

	sess := session.FromContext(r.Context())

	a.Render.Render(w, r, "Auth/Login", inertia.Props{
		"canResetPassword": false,
		"status":           nullable(sess.Flashed("status")),
	})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	user, err := a.Store.Authenticate(r.Context(), input["email"], input["password"])

	if errors.Is(err, store.ErrNoUser) {
		// One message for a wrong address and a wrong password alike. Telling
		// them apart is how an attacker learns which addresses have accounts.
		sess.Error("email", "Those credentials do not match our records.")
		inertia.Back(w, r, "/login")
		return
	}

	if err != nil {
		a.serverError(w, r, "could not check the credentials", err)
		return
	}

	if err := sess.Login(r.Context(), user.ID); err != nil {
		a.serverError(w, r, "could not sign in", err)
		return
	}

	a.Log.Info("signed in", "email", user.Email)

	// Back to whatever they were reaching for before being asked to sign in.
	target := sess.Pull("intended")
	if target == "" {
		target = "/dashboard"
	}

	inertia.Redirect(w, r, target)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	session.FromContext(r.Context()).Logout(r.Context())

	inertia.Redirect(w, r, "/login")
}

func (a *App) editProfile(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	a.Render.Render(w, r, "Profile/Edit", inertia.Props{
		"mustVerifyEmail": false,
		"status":          nullable(sess.Flashed("status")),
	})
}

func (a *App) updateProfile(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := strings.TrimSpace(input["name"])
	email := strings.TrimSpace(input["email"])

	if name == "" {
		sess.Error("name", "Enter a name.")
	}
	if email == "" || !strings.Contains(email, "@") {
		sess.Error("email", "Enter a valid email address.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/profile")
		return
	}

	if err := a.Store.UpdateProfile(r.Context(), user.ID, name, email); err != nil {
		a.Log.Error("could not update the profile", "error", err)
		sess.Flash("error", "Could not save those changes.")
		inertia.Back(w, r, "/profile")
		return
	}

	sess.Flash("status", "profile-updated")

	inertia.Redirect(w, r, "/profile")
}

func (a *App) updatePassword(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	// The current password is asked for so that a borrowed, still-signed-in
	// browser cannot be turned into a permanent one.
	if !a.Store.CheckPassword(r.Context(), user.ID, input["current_password"]) {
		sess.Error("current_password", "That is not your current password.")
	}

	if len(input["password"]) < minPasswordLength {
		sess.Error("password", "Use at least 12 characters.")
	}
	if input["password"] != input["password_confirmation"] {
		sess.Error("password_confirmation", "The two passwords do not match.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/profile")
		return
	}

	if err := a.Store.UpdatePassword(r.Context(), user.ID, input["password"]); err != nil {
		a.Log.Error("could not update the password", "error", err)
		sess.Flash("error", "Could not change the password.")
		inertia.Back(w, r, "/profile")
		return
	}

	sess.Flash("status", "password-updated")

	inertia.Redirect(w, r, "/profile")
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	a.Render.Render(w, r, "Dashboard", inertia.Props{
		"host":      hostname(),
		"sitesRoot": a.Config.SitesRoot,
		"database":  a.Config.DatabasePath,
	})
}

// hostname is what the machine calls itself, for the dashboard to show.
func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "this server"
	}

	return name
}
