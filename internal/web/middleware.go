package web

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

type userKey struct{}

// userFrom is the signed-in account, or nil.
func userFrom(ctx context.Context) *store.User {
	user, _ := ctx.Value(userKey{}).(*store.User)
	return user
}

// withUser loads the account the session names.
func (a *App) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := session.FromContext(r.Context())

		if sess == nil || sess.UserID <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		user, err := a.Store.FindUser(r.Context(), sess.UserID)

		if errors.Is(err, store.ErrNoUser) {
			// The row is gone but the session still points at it. Sign the
			// session out rather than serving pages for an account that no
			// longer exists.
			sess.Logout(r.Context())
			next.ServeHTTP(w, r)
			return
		}

		if err != nil {
			a.Log.Error("could not load the signed-in user", "user", sess.UserID, "error", err)
			next.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

// guard applies a route's access level and, for anything that changes state,
// its CSRF check.
func (a *App) guard(level access, handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := session.FromContext(r.Context())
		user := userFrom(r.Context())

		switch level {
		case authenticated:
			if user == nil {
				a.requireSignIn(w, r, sess)
				return
			}

		case guest:
			if user != nil {
				inertia.Redirect(w, r, "/dashboard")
				return
			}
		}

		// Every state-changing request that is not public, signed in or not:
		// the sign-in form is exactly the kind of thing a cross-site POST
		// targets.
		if level != public && changesState(r) && (sess == nil || !sess.ValidCSRF(r)) {
			a.rejectCSRF(w, r, sess)
			return
		}

		// A tab that was open across a frontend rebuild is running old
		// JavaScript against a new server. Reloading it is the protocol's
		// answer, and it costs one round trip.
		if a.Render.StaleAssets(r) {
			inertia.Location(w, r, r.URL.String())
			return
		}

		handler(w, r)
	})
}

func (a *App) requireSignIn(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	// Remember where they were going, so signing in lands there rather than
	// dumping everyone on the dashboard.
	if r.Method == http.MethodGet && sess != nil {
		sess.Put("intended", r.URL.Path)
	}

	// A plain JSON 401 is what Inertia refuses outright — it shows "All
	// Inertia requests must receive a valid Inertia response" and the tab is
	// stuck. 409 with a location puts the sign-in page on screen instead.
	inertia.Location(w, r, "/login")
}

func (a *App) rejectCSRF(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	a.Log.Warn("CSRF rejected", "method", r.Method, "path", r.URL.Path, "ip", r.RemoteAddr)

	if sess != nil {
		// Survives the reload in the next request's flash bag, so the form
		// comes back with an explanation rather than silently empty.
		sess.Flash("error", "Your session expired, so the form was not submitted. Try again.")
	}

	if inertia.IsInertia(r) {
		// Reload the page the form was on. Its own path is usually POST-only,
		// and reloading that would answer 405.
		inertia.Location(w, r, inertia.RefererPath(r, "/"))
		return
	}

	// 419 is not in the RFCs; it is what this family of panels has always
	// answered for an expired page, and what tooling around them expects.
	http.Error(w, "Your session expired. Reload the page and try again.", 419)
}

func changesState(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}

	return true
}

// withLogging records one line per request.
func (a *App) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		// Assets are the bulk of the traffic and none of the interest, so they
		// are only logged when they fail or when debugging.
		if !a.Config.Debug && recorder.status < 400 && isAsset(r.URL.Path) {
			return
		}

		a.Log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(started).Round(time.Millisecond))
	})
}

// withRecovery keeps one bad handler from taking the panel down with it. A
// control panel that dies on a nil map is a control panel you cannot use to
// fix the machine it died on.
func (a *App) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if problem := recover(); problem != nil {
				a.Log.Error("handler panicked",
					"path", r.URL.Path,
					"panic", problem,
					"stack", string(debug.Stack()))

				http.Error(w, "The panel hit an unexpected error.", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.written {
		s.status = status
		s.written = true
	}

	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
