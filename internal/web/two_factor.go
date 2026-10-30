package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/totp"
)

// The session keys the second step of a sign-in is carried in. None of them
// signs anybody in: the account id here is one whose password was right, and
// it becomes the session's user only when a code has been checked as well.
const (
	twoFactorUserKey  = "two_factor_user"
	twoFactorAtKey    = "two_factor_at"
	twoFactorTriesKey = "two_factor_tries"

	// recoveryCodesKey carries freshly made recovery codes from the request
	// that made them to the page that shows them, once.
	recoveryCodesKey = "two_factor_recovery_codes"

	// twoFactorWindow is how long a right password is remembered while the
	// code is fetched from a phone.
	twoFactorWindow = 5 * time.Minute

	// twoFactorTries is how many wrong codes one right password buys. After
	// that the password has to be typed again, which puts the attempts back
	// under the per-address lockout.
	twoFactorTries = 5
)

// ----------------------------------------------------------------- sign in

// pendingTwoFactor is the account waiting on its second step, or nil.
func (a *App) pendingTwoFactor(r *http.Request) *store.User {
	sess := session.FromContext(r.Context())
	if sess == nil {
		return nil
	}

	id, _ := strconv.ParseInt(sess.Get(twoFactorUserKey), 10, 64)
	at, _ := strconv.ParseInt(sess.Get(twoFactorAtKey), 10, 64)

	if id <= 0 || time.Since(time.Unix(at, 0)) > twoFactorWindow {
		sess.Forget(twoFactorUserKey, twoFactorAtKey, twoFactorTriesKey)
		return nil
	}

	user, err := a.Store.FindUser(r.Context(), id)
	if err != nil {
		return nil
	}

	return user
}

func (a *App) showTwoFactorChallenge(w http.ResponseWriter, r *http.Request) {
	if a.pendingTwoFactor(r) == nil {
		inertia.Redirect(w, r, "/login")
		return
	}

	a.Render.Render(w, r, "Auth/TwoFactor", inertia.Props{})
}

func (a *App) twoFactorChallenge(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	user := a.pendingTwoFactor(r)
	if user == nil {
		sess.Flash("error", "That took too long. Sign in again.")
		inertia.Redirect(w, r, "/login")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	attempt := a.loginAttempt(r, user.Email)
	attempt.UserID = user.ID

	if until := a.loginLockedUntil(r.Context(), attempt.IP); until > 0 {
		attempt.Reason = store.LoginLocked
		a.Store.RecordLoginAttempt(r.Context(), attempt)

		sess.Forget(twoFactorUserKey, twoFactorAtKey, twoFactorTriesKey)
		sess.Error("email", lockedMessage(until))
		inertia.Redirect(w, r, "/login")
		return
	}

	code := strings.TrimSpace(input["code"])
	recovery := strings.TrimSpace(input["recovery_code"])

	switch {
	case recovery != "":
		if a.Store.UseRecoveryCode(r.Context(), user.ID, recovery) {
			sess.Forget(twoFactorUserKey, twoFactorAtKey, twoFactorTriesKey)
			a.Log.Warn("signed in with a recovery code", "email", user.Email)
			a.completeLogin(w, r, user, store.LoginRecovery)
			return
		}

		attempt.Reason = store.LoginRecovery

	case code != "":
		factor, err := a.Store.TwoFactorFor(r.Context(), user.ID)
		if err != nil {
			a.serverError(w, r, "could not read the second factor", err)
			return
		}

		if step, ok := totp.Verify(factor.Secret, code, time.Now(), factor.LastStep); ok &&
			a.Store.UseTwoFactorStep(r.Context(), user.ID, step) {
			sess.Forget(twoFactorUserKey, twoFactorAtKey, twoFactorTriesKey)
			a.completeLogin(w, r, user, store.LoginTwoFactor)
			return
		}

		attempt.Reason = store.LoginTwoFactor

	default:
		sess.Error("code", "Enter the six-digit code from your authenticator app.")
		inertia.Back(w, r, "/login/two-factor")
		return
	}

	a.Store.RecordLoginAttempt(r.Context(), attempt)
	a.Log.Warn("second sign-in step failed", "ip", attempt.IP, "email", user.Email)

	tries, _ := strconv.Atoi(sess.Get(twoFactorTriesKey))
	tries++

	if tries >= twoFactorTries {
		sess.Forget(twoFactorUserKey, twoFactorAtKey, twoFactorTriesKey)
		sess.Error("email", "Too many wrong codes. Sign in again.")
		inertia.Redirect(w, r, "/login")
		return
	}

	sess.Put(twoFactorTriesKey, strconv.Itoa(tries))

	if attempt.Reason == store.LoginRecovery {
		sess.Error("recovery_code", "That recovery code is not valid, or has already been used.")
	} else {
		sess.Error("code", "That code is not valid. Check the time on your phone is set automatically.")
	}

	inertia.Back(w, r, "/login/two-factor")
}

// ----------------------------------------------------------------- profile

// twoFactorProps is the Profile page's view of the account's second factor.
func (a *App) twoFactorProps(r *http.Request, user *store.User) map[string]any {
	sess := session.FromContext(r.Context())

	factor, _ := a.Store.TwoFactorFor(r.Context(), user.ID)

	props := map[string]any{
		"enabled":   factor.Confirmed,
		"pending":   factor.Secret != "" && !factor.Confirmed,
		"remaining": len(factor.Recovery),
		"secret":    nil,
		"uri":       nil,
		"codes":     nil,
	}

	// The secret is on the page only while it is being enrolled. Once it is
	// confirmed, nothing shows it again: losing the phone is what recovery
	// codes are for, and a secret on a page is a secret in a screenshot.
	if factor.Secret != "" && !factor.Confirmed {
		props["secret"] = factor.Secret
		props["uri"] = totp.URI(config.Name, user.Email, factor.Secret)
	}

	if encoded := sess.Pull(recoveryCodesKey); encoded != "" {
		var codes []string
		if json.Unmarshal([]byte(encoded), &codes) == nil {
			props["codes"] = codes
		}
	}

	return props
}

// checkPassword is the gate on every change to the second factor. Without it,
// a browser somebody left signed in is a way to switch it off.
func (a *App) checkPassword(r *http.Request, user *store.User, input map[string]string) bool {
	if a.Store.CheckPassword(r.Context(), user.ID, input["password"]) {
		return true
	}

	session.FromContext(r.Context()).Error("password", "That is not your current password.")

	return false
}

func (a *App) twoFactorStart(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !a.checkPassword(r, user, input) {
		inertia.Back(w, r, "/profile")
		return
	}

	if err := a.Store.StartTwoFactor(r.Context(), user.ID, totp.NewSecret()); err != nil {
		a.Log.Error("could not start two-factor enrolment", "error", err)
		a.failBack(w, r, "Could not start two-factor authentication. The panel's application key may be missing.")
		return
	}

	inertia.Back(w, r, "/profile")
}

func (a *App) twoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	factor, err := a.Store.TwoFactorFor(r.Context(), user.ID)
	if err != nil || factor.Secret == "" || factor.Confirmed {
		inertia.Back(w, r, "/profile")
		return
	}

	step, ok := totp.Verify(factor.Secret, input["code"], time.Now(), 0)
	if !ok {
		sess.Error("code", "That code is not valid. Scan the QR code again, and check the time "+
			"on your phone is set automatically.")
		inertia.Back(w, r, "/profile")
		return
	}

	codes, err := a.Store.ConfirmTwoFactor(r.Context(), user.ID, step)
	if err != nil {
		a.serverError(w, r, "could not confirm two-factor authentication", err)
		return
	}

	encoded, _ := json.Marshal(codes)
	sess.Put(recoveryCodesKey, string(encoded))

	// Anybody else signed in got in without a code. Now that one is needed,
	// they have to come back through the door that asks for it.
	a.Store.RevokeOtherSessions(r.Context(), user.ID, sess.ID)

	a.Log.Info("two-factor authentication enabled", "email", user.Email)

	a.okBack(w, r, "Two-factor authentication is on. Save your recovery codes somewhere safe.")
}

func (a *App) twoFactorRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !a.checkPassword(r, user, input) {
		inertia.Back(w, r, "/profile")
		return
	}

	codes, err := a.Store.RegenerateRecoveryCodes(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, "could not make recovery codes", err)
		return
	}

	encoded, _ := json.Marshal(codes)
	sess.Put(recoveryCodesKey, string(encoded))

	a.okBack(w, r, "New recovery codes made. The old ones no longer work.")
}

func (a *App) twoFactorDisable(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !a.checkPassword(r, user, input) {
		inertia.Back(w, r, "/profile")
		return
	}

	if err := a.Store.DisableTwoFactor(r.Context(), user.ID); err != nil {
		a.serverError(w, r, "could not turn two-factor authentication off", err)
		return
	}

	a.Log.Warn("two-factor authentication disabled", "email", user.Email)

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "Two-factor authentication is off. Your password alone now signs in.")
	}

	inertia.Back(w, r, "/profile")
}

// lockedMessage says when an address can try again.
func lockedMessage(until int64) string {
	minutes := int(time.Until(time.Unix(until, 0)).Minutes()) + 1

	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}

	return fmt.Sprintf("Too many failed sign-ins from your address. Try again in %d %s.", minutes, unit)
}
