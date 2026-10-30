package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// sshUsers is the Security page's list of login accounts, each with the last
// time it logged in over SSH as far as the log goes back.
func (a *App) sshUsers(snap *securitySnapshot) []map[string]any {
	lastLogin := map[string]security.Event{}

	// Newest first, so the first accepted login seen per user is the latest.
	for _, event := range snap.Events {
		if event.Kind != security.EventAccepted {
			continue
		}

		if _, seen := lastLogin[event.User]; !seen {
			lastLogin[event.User] = event
		}
	}

	list := []map[string]any{}

	for _, user := range snap.Users {
		entry := map[string]any{
			"name":             user.Name,
			"uid":              user.UID,
			"home":             user.Home,
			"shell":            user.Shell,
			"groups":           user.Groups,
			"sudo":             user.Sudo,
			"locked":           user.Locked,
			"password_locked":  user.PasswordLocked,
			"expired":          user.Expired,
			"has_password":     user.Password,
			"keys":             user.Keys,
			"protected":        user.Protected,
			"protected_reason": user.Reason,
			"site":             nullable(user.Site),
			"last_login":       nil,
		}

		if event, ok := lastLogin[user.Name]; ok {
			entry["last_login"] = map[string]any{
				"time": event.Time, "ip": event.IP, "method": event.Method,
			}
		}

		list = append(list, entry)
	}

	return list
}

func (a *App) sshUserStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	user := security.NewUser{
		Name:      strings.TrimSpace(input["name"]),
		Password:  input["password"],
		PublicKey: strings.TrimSpace(input["public_key"]),
		Sudo:      boolInput(input, "sudo", false),
	}

	for field, problem := range user.Validate() {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/security")
		return
	}

	if err := security.CreateUser(user); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("SSH user created", "user", user.Name, "sudo", user.Sudo)
	a.securityBack(w, r, user.Name+" can now log in over SSH.", false)
}

// sshUserUpdate is every change to one account: a password, sudo, locking.
// One endpoint with an `action`, because each is one field and one command.
func (a *App) sshUserUpdate(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	name := r.PathValue("user")

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	var message string

	switch input["action"] {
	case "password":
		if input["password"] != input["password_confirmation"] {
			sess.Error("password_confirmation", "The two passwords do not match.")
			inertia.Back(w, r, "/security")
			return
		}

		err = security.SetPassword(name, input["password"], a.Config.SystemUser)
		message = "Password changed for " + name + "."

	case "sudo":
		enabled := boolInput(input, "enabled", false)
		err = security.SetSudo(name, enabled, a.Config.SystemUser)
		message = map[bool]string{true: name + " can now use sudo.", false: name + " can no longer use sudo."}[enabled]

	case "lock":
		err = security.SetLocked(name, true, a.Config.SystemUser)
		message = name + " is locked: no password or key logs in, and its sessions were ended."

	case "unlock":
		err = security.SetLocked(name, false, a.Config.SystemUser)
		message = name + " can log in again."

	default:
		a.securityBack(w, r, "That is not something the panel can change about an account.", true)
		return
	}

	if err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("SSH user changed", "user", name, "action", input["action"])
	a.securityBack(w, r, message, false)
}

func (a *App) sshUserDestroy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("user")
	removeHome := r.URL.Query().Get("home") == "1"

	if err := security.DeleteUser(name, removeHome, a.Config.SystemUser); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Warn("SSH user deleted", "user", name, "home_removed", removeHome)

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", name+" was deleted.")
	}

	a.securityBack(w, r, "", false)
}

func (a *App) sshUserKeyStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	name := r.PathValue("user")

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	key := strings.TrimSpace(input["public_key"])
	if !security.ValidPublicKey(key) {
		sess.Error("public_key", "Paste one public key, the single line from a .pub file.")
		inertia.Back(w, r, "/security")
		return
	}

	if err := security.AddKey(name, key, a.Config.SystemUser); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("SSH key added", "user", name)
	a.securityBack(w, r, "Key added for "+name+".", false)
}

func (a *App) sshUserKeyDestroy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("user")

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if err := security.RemoveKey(name, input["fingerprint"], a.Config.SystemUser); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("SSH key removed", "user", name, "fingerprint", input["fingerprint"])
	a.securityBack(w, r, "Key removed from "+name+".", false)
}

// siteIsolationSetting records that site accounts were moved out of www-data
// and every site's files were restricted to it.
const siteIsolationSetting = "site_isolation_v1"

// EnsureSiteIsolation does that once, on a machine set up before it was how
// site accounts worked. Marked done only when every site succeeded, so a site
// that failed is tried again at the next start.
func (a *App) EnsureSiteIsolation(ctx context.Context) {
	if a.Store.SettingBool(siteIsolationSetting, false) {
		return
	}

	if err := a.Sites.MigrateAccessUsers(ctx); err != nil {
		a.Log.Error("could not restrict every site's files to the site", "error", err)
		return
	}

	a.Store.SetSetting(siteIsolationSetting, "1")
	a.Log.Info("site accounts can reach their own site only")
}

// siteHomes tells the security package which accounts belong to a site: the
// ones whose home is a site's directory. Those are managed from the site's
// page, and the SSH users tab — which is about the machine — shows them
// without offering anything that would reach past the site.
func (a *App) siteHomes() map[string]string {
	homes := map[string]string{}

	sites, err := a.Store.Sites(context.Background())
	if err != nil {
		return homes
	}

	for _, site := range sites {
		homes[strings.TrimRight(site.RootPath, "/")] = site.Domain
	}

	return homes
}
