// Package session keeps per-browser state in the database and hands the
// browser two cookies: an opaque, HttpOnly session id, and the CSRF token that
// id's session expects to see echoed back.
//
// The ordering rule this package exists to enforce: a session is written to
// the database, and its cookies onto the response, before a single byte of the
// response reaches the browser. Getting that backwards is what produces the
// failure where every form submit answers "your session expired" — the browser
// was handed a token belonging to a session that was never stored, so the next
// request looks brand new and the token it presents can never match.
package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// CookieName is the session id cookie. Same name the C panel used, so a
	// browser signed in to one stays signed in to the other.
	CookieName = "ubuntu_panel_session"

	// CSRFCookieName is read by JavaScript and echoed back as a header. That
	// it is readable is the entire mechanism; it carries no authority on its
	// own, because the session cookie is still required and is HttpOnly.
	CSRFCookieName = "XSRF-TOKEN"

	// Lifetime is how long a session survives without being used.
	Lifetime = 7 * 24 * time.Hour

	// touchInterval keeps a one-second dashboard poll from making
	// last_activity the busiest write in the process.
	touchInterval = time.Minute
)

// Session is one browser's state.
type Session struct {
	ID     string
	UserID int64
	CSRF   string

	// What the previous request flashed, for this one to display.
	flashNow  map[string]string
	errorsNow map[string]string

	// What this request is flashing, for the next one to display.
	flashNext  map[string]string
	errorsNext map[string]string

	// Values that survive until something reads them — the URL a sign-in
	// should return to, for instance.
	keep map[string]string

	dirty       bool
	cookieDirty bool
	committed   bool

	manager *Manager
	writer  http.ResponseWriter
}

// Manager loads and stores sessions.
type Manager struct {
	db     *sql.DB
	log    *slog.Logger
	secure bool
}

// New builds a manager. secure marks the cookies Secure, which is correct once
// the panel is only reachable over HTTPS and wrong before then — a Secure
// cookie is silently dropped on a plain-HTTP page, which looks exactly like
// the session never being stored.
func New(db *sql.DB, log *slog.Logger, secure bool) *Manager {
	return &Manager{db: db, log: log, secure: secure}
}

type contextKey struct{}

// FromContext returns the session attached by the middleware. It is always
// present on a request that went through Middleware.
func FromContext(ctx context.Context) *Session {
	session, _ := ctx.Value(contextKey{}).(*Session)
	return session
}

// Middleware attaches a session to every request.
//
// The response writer is wrapped so that the session is committed — written to
// the database, cookies set — on the first write, whichever handler makes it.
// Committing afterwards would be a race: the browser can act on a redirect and
// send its next request before a deferred write finished.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := m.start(r)
		if err != nil {
			m.log.Error("could not start a session", "error", err)
			http.Error(w, "The panel could not start a session.", http.StatusInternalServerError)
			return
		}

		writer := &committingWriter{ResponseWriter: w, commit: session.commit}

		session.writer = writer

		// A brand new session's cookie goes out with this response, before
		// anything can decide to return early without touching the session.
		if session.cookieDirty {
			session.writeCookies()
		}

		next.ServeHTTP(writer, r.WithContext(context.WithValue(r.Context(), contextKey{}, session)))

		// A handler that wrote nothing at all still gets its session saved.
		session.commit()
	})
}

// start loads the session the cookie names, or makes a new one.
func (m *Manager) start(r *http.Request) (*Session, error) {
	session := &Session{
		manager:    m,
		flashNow:   map[string]string{},
		errorsNow:  map[string]string{},
		flashNext:  map[string]string{},
		errorsNext: map[string]string{},
		keep:       map[string]string{},
	}

	if cookie, err := r.Cookie(CookieName); err == nil && len(cookie.Value) == 64 {
		if loaded, err := m.load(r.Context(), session, cookie.Value); err != nil {
			return nil, err
		} else if loaded {
			return session, nil
		}
	}

	if err := m.create(r, session); err != nil {
		return nil, err
	}

	return session, nil
}

func (m *Manager) load(ctx context.Context, session *Session, id string) (bool, error) {
	var (
		userID       sql.NullInt64
		payload      string
		lastActivity int64
	)

	err := m.db.QueryRowContext(ctx,
		`SELECT user_id, payload, last_activity FROM sessions WHERE id = ?`, id,
	).Scan(&userID, &payload, &lastActivity)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Expired. Delete it rather than leaving it for a sweeper: an id that is
	// past its lifetime but still in the table is one that a stolen cookie can
	// keep presenting.
	if time.Since(time.Unix(lastActivity, 0)) > Lifetime {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
			m.log.Warn("could not delete an expired session", "error", err)
		}
		return false, nil
	}

	var stored struct {
		CSRF   string            `json:"csrf"`
		Flash  map[string]string `json:"flash"`
		Errors map[string]string `json:"errors"`
		Keep   map[string]string `json:"keep"`
	}

	// A payload that will not parse is not a reason to refuse the request; it
	// is a reason to start this session over with an empty one.
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		m.log.Warn("session payload was unreadable, starting it empty", "session", short(id))
	}

	session.ID = id
	session.UserID = userID.Int64
	session.CSRF = stored.CSRF

	// Last request's outgoing bags are this request's incoming ones.
	session.flashNow = orEmpty(stored.Flash)
	session.errorsNow = orEmpty(stored.Errors)
	session.keep = orEmpty(stored.Keep)

	if session.CSRF == "" {
		session.CSRF = token()
		session.dirty = true
	}

	if time.Since(time.Unix(lastActivity, 0)) > touchInterval {
		session.dirty = true
	}

	return true, nil
}

func (m *Manager) create(r *http.Request, session *Session) error {
	session.ID = token()
	session.CSRF = token()
	session.cookieDirty = true

	payload, err := session.payload()
	if err != nil {
		return err
	}

	_, err = m.db.ExecContext(r.Context(),
		`INSERT INTO sessions (id, user_id, ip_address, user_agent, payload, last_activity)
		 VALUES (?, 0, ?, ?, ?, ?)`,
		session.ID, clientIP(r), r.UserAgent(), payload, time.Now().Unix())

	// Handing out a session that was never stored is worse than refusing the
	// request: the browser gets a token for a session that does not exist, and
	// every form it submits from then on fails a CSRF check with nothing in
	// the log to explain it.
	if err != nil {
		return err
	}

	return nil
}

// commit writes the session and its cookies. Safe to call more than once; only
// the first call does anything.
func (s *Session) commit() {
	if s.committed {
		return
	}
	s.committed = true

	if s.cookieDirty {
		s.writeCookies()
	}

	if s.ID == "" {
		return
	}

	// The flash bags are written even when empty: that is what clears the
	// previous request's messages. Without it a "Site queued" banner would
	// follow the user from page to page until something replaced it.
	if !s.dirty && len(s.flashNow) == 0 && len(s.errorsNow) == 0 &&
		len(s.flashNext) == 0 && len(s.errorsNext) == 0 {
		return
	}

	payload, err := s.payload()
	if err != nil {
		s.manager.log.Error("could not encode the session", "error", err)
		return
	}

	result, err := s.manager.db.Exec(
		`UPDATE sessions SET user_id = ?, payload = ?, last_activity = ? WHERE id = ?`,
		s.UserID, payload, time.Now().Unix(), s.ID)

	if err != nil {
		s.manager.log.Error("could not save the session", "session", short(s.ID), "error", err)
		return
	}

	if rows, _ := result.RowsAffected(); rows == 0 {
		s.manager.log.Warn("saved a session whose row is gone", "session", short(s.ID))
	}
}

func (s *Session) writeCookies() {
	if s.writer == nil {
		return
	}

	s.cookieDirty = false

	if s.ID == "" {
		// Signed out. Expire both cookies.
		http.SetCookie(s.writer, s.cookie(CookieName, "", true, -1))
		http.SetCookie(s.writer, s.cookie(CSRFCookieName, "", false, -1))
		return
	}

	http.SetCookie(s.writer, s.cookie(CookieName, s.ID, true, int(Lifetime.Seconds())))
	http.SetCookie(s.writer, s.cookie(CSRFCookieName, s.CSRF, false, int(Lifetime.Seconds())))
}

func (s *Session) cookie(name, value string, httpOnly bool, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   s.manager.secure,

		// Lax, not Strict: the panel is reached from bookmarks and from the
		// "log in at https://…" line the installer prints, and Strict drops
		// the session on those top-level navigations. Lax still blocks the
		// cross-site POST that CSRF is about, and the token is checked
		// independently anyway.
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Session) payload() (string, error) {
	encoded, err := json.Marshal(map[string]any{
		"csrf": s.CSRF,
		// What this request flashed is what the next one reads.
		"flash":  s.flashNext,
		"errors": s.errorsNext,
		"keep":   s.keep,
	})

	return string(encoded), err
}

// Login attaches an account to the session and gives it a new id.
//
// The new id is not optional: without it, an id an attacker planted in the
// victim's browser before they signed in still names their session afterwards.
func (s *Session) Login(ctx context.Context, userID int64) error {
	old := s.ID

	s.ID = token()
	s.UserID = userID
	s.dirty = true
	s.cookieDirty = true

	if _, err := s.manager.db.ExecContext(ctx,
		`UPDATE sessions SET id = ?, user_id = ? WHERE id = ?`, s.ID, userID, old); err != nil {
		return err
	}

	// The cookie has to go out now. A handler that redirects immediately after
	// signing in would otherwise send the browser on with the old id.
	s.writeCookies()

	return nil
}

// Logout drops the session entirely.
func (s *Session) Logout(ctx context.Context) {
	if s.ID != "" {
		if _, err := s.manager.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, s.ID); err != nil {
			s.manager.log.Warn("could not delete the session", "error", err)
		}
	}

	s.ID = ""
	s.UserID = 0
	s.dirty = false
	s.cookieDirty = true

	s.writeCookies()
}

// Flash stores a message for the next request to display.
func (s *Session) Flash(kind, message string) {
	s.flashNext[kind] = message
	s.dirty = true
}

// Flashed reads a message the previous request left.
func (s *Session) Flashed(kind string) string { return s.flashNow[kind] }

// AllFlashed is every message the previous request left.
func (s *Session) AllFlashed() map[string]string { return s.flashNow }

// Error records a validation failure against a field, for the next request.
func (s *Session) Error(field, message string) {
	s.errorsNext[field] = message
	s.dirty = true
}

// HasErrors says whether this request recorded any validation failures.
func (s *Session) HasErrors() bool { return len(s.errorsNext) > 0 }

// Errors is what the previous request recorded, which is what the page shows.
func (s *Session) Errors() map[string]string { return s.errorsNow }

// Put stores a value that survives until Pull takes it.
func (s *Session) Put(key, value string) {
	s.keep[key] = value
	s.dirty = true
}

// Pull takes a stored value and removes it.
func (s *Session) Pull(key string) string {
	value := s.keep[key]

	if value != "" {
		delete(s.keep, key)
		s.dirty = true
	}

	return value
}

// ValidCSRF says whether the request presented this session's token. The
// comparison is constant time: a byte-at-a-time one leaks the token's prefix
// to anybody willing to measure.
func (s *Session) ValidCSRF(r *http.Request) bool {
	if s.CSRF == "" {
		return false
	}

	presented := r.Header.Get("X-XSRF-TOKEN")

	if presented == "" {
		presented = r.Header.Get("X-CSRF-TOKEN")
	}
	if presented == "" {
		presented = r.PostFormValue("_token")
	}
	if presented == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(presented)) == 1
}

// Prune removes sessions nobody has used inside the lifetime.
func (m *Manager) Prune(ctx context.Context) {
	cutoff := time.Now().Add(-Lifetime).Unix()

	result, err := m.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_activity < ?`, cutoff)
	if err != nil {
		m.log.Warn("could not prune sessions", "error", err)
		return
	}

	if rows, _ := result.RowsAffected(); rows > 0 {
		m.log.Info("pruned expired sessions", "count", rows)
	}
}

// committingWriter makes sure the session is saved and its cookies set before
// any part of the response is written. Once WriteHeader has run, adding a
// Set-Cookie is too late and Go will say so in the log rather than send it.
type committingWriter struct {
	http.ResponseWriter

	commit  func()
	written bool
}

func (w *committingWriter) WriteHeader(status int) {
	if !w.written {
		w.written = true
		w.commit()
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *committingWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.written = true
		w.commit()
	}

	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *committingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack hands the connection over for the websocket upgrade.
//
// Unwrap is not enough for that: the websocket library type-asserts the writer
// to http.Hijacker directly, and a wrapper without this method fails the
// handshake with a 500 the browser reports as "terminal server unreachable".
//
// The session is committed first. From here the response is written to the
// connection by hand, so a Set-Cookie added afterwards would go nowhere.
func (w *committingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}

	if !w.written {
		w.written = true
		w.commit()
	}

	return hijacker.Hijack()
}

// Flush is the same arrangement for a response that streams.
func (w *committingWriter) Flush() {
	if !w.written {
		w.written = true
		w.commit()
	}

	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func token() string {
	buffer := make([]byte, 32)

	// crypto/rand.Read is documented never to fail on any platform the panel
	// runs on; it panics internally if the kernel cannot produce randomness,
	// which is the right outcome for a session id.
	rand.Read(buffer)

	return hex.EncodeToString(buffer)
}

func clientIP(r *http.Request) string {
	// The panel sits behind nginx on the same machine, which sets these.
	if forwarded := r.Header.Get("X-Real-IP"); forwarded != "" {
		return forwarded
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}

	// SplitHostPort rather than cutting at the colon: an IPv6 address is full
	// of them.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
