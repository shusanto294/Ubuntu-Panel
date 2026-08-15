package web_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/web"
)

// panel is a running server plus a client that keeps its cookies, which is the
// only way to test anything about sessions honestly.
type panel struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
}

func start(t *testing.T) *panel {
	t.Helper()

	cfg := &config.Config{
		DatabasePath: filepath.Join(t.TempDir(), "panel.db"),
		SitesRoot:    "/var/www",
		BindHost:     "127.0.0.1",
		BindPort:     8088,
	}

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	app := web.New(cfg, db, os.DirFS(t.TempDir()), log)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building a cookie jar: %v", err)
	}

	client := &http.Client{
		Jar: jar,

		// Redirects are the assertion in most of these tests, so they are not
		// followed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &panel{t: t, server: server, client: client}
}

func (p *panel) do(method, path string, body string, headers map[string]string) *http.Response {
	p.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	request, err := http.NewRequest(method, p.server.URL+path, reader)
	if err != nil {
		p.t.Fatalf("building the request: %v", err)
	}

	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}

	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := p.client.Do(request)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}

	return response
}

// csrf is the token the browser would have been given, read the way the
// frontend reads it: out of the cookie.
func (p *panel) csrf() string {
	p.t.Helper()

	for _, cookie := range p.client.Jar.Cookies(mustParse(p.t, p.server.URL)) {
		if cookie.Name == session.CSRFCookieName {
			return cookie.Value
		}
	}

	return ""
}

func (p *panel) inertia() map[string]string {
	return map[string]string{"X-Inertia": "true", "X-Inertia-Version": "dev"}
}

func TestSetupCreatesTheFirstAccount(t *testing.T) {
	p := start(t)

	// The visit that hands out the session and its token.
	response := p.do(http.MethodGet, "/setup", "", nil)
	response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /setup = %d, want 200", response.StatusCode)
	}

	token := p.csrf()
	if token == "" {
		t.Fatal("GET /setup handed out no CSRF token")
	}

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = token

	response = p.do(http.MethodPost, "/setup", `{
		"name": "Admin",
		"email": "admin@example.com",
		"password": "a-long-enough-password",
		"password_confirmation": "a-long-enough-password"
	}`, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup = %d, want 303", response.StatusCode)
	}

	if location := response.Header.Get("Location"); location != "/dashboard" {
		t.Fatalf("POST /setup redirected to %q, want /dashboard", location)
	}

	// The session id is regenerated on sign-in, so the response has to carry
	// the new cookie. Without it the browser keeps presenting an id that no
	// longer names a row, and every later request looks signed out.
	response = p.do(http.MethodGet, "/dashboard", "", p.inertia())
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /dashboard after setup = %d, want 200", response.StatusCode)
	}

	var page struct {
		Component string `json:"component"`
		Props     struct {
			Auth struct {
				User *struct {
					Email string `json:"email"`
				} `json:"user"`
			} `json:"auth"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	if page.Props.Auth.User == nil {
		t.Fatal("the dashboard rendered with nobody signed in")
	}

	if page.Props.Auth.User.Email != "admin@example.com" {
		t.Fatalf("signed in as %q, want admin@example.com", page.Props.Auth.User.Email)
	}
}

// The regression this whole rewrite started from: an Inertia request whose CSRF
// token does not match must never be answered with a JSON body. The client
// refuses anything that is not a page object and shows "All Inertia requests
// must receive a valid Inertia response", which the user cannot act on.
func TestStaleTokenIsRecoverable(t *testing.T) {
	p := start(t)

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = "not-the-token"
	headers["Referer"] = p.server.URL + "/setup"

	response := p.do(http.MethodPost, "/setup", `{"name":"Admin"}`, headers)
	defer response.Body.Close()

	if response.StatusCode != http.StatusConflict {
		t.Fatalf("POST /setup with a stale token = %d, want 409", response.StatusCode)
	}

	if location := response.Header.Get("X-Inertia-Location"); location != "/setup" {
		t.Fatalf("X-Inertia-Location = %q, want /setup", location)
	}

	body, _ := io.ReadAll(response.Body)

	if len(body) > 0 {
		t.Fatalf("a rejected Inertia request answered with a body: %s", body)
	}
}

func TestSignedOutInertiaRequestIsSentToLogin(t *testing.T) {
	p := start(t)

	response := p.do(http.MethodGet, "/dashboard", "", p.inertia())
	defer response.Body.Close()

	if response.StatusCode != http.StatusConflict {
		t.Fatalf("GET /dashboard signed out = %d, want 409", response.StatusCode)
	}

	if location := response.Header.Get("X-Inertia-Location"); location != "/login" {
		t.Fatalf("X-Inertia-Location = %q, want /login", location)
	}
}

// A session handed to the browser but never stored is the failure that hides:
// pages render, and every form comes back "your session expired".
func TestTheSessionSurvivesTheNextRequest(t *testing.T) {
	p := start(t)

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()
	first := p.csrf()

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()
	second := p.csrf()

	if first == "" || first != second {
		t.Fatalf("the token changed between requests: %q then %q", first, second)
	}
}

func TestValidationErrorsComeBackOnTheForm(t *testing.T) {
	p := start(t)

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()
	headers["Referer"] = p.server.URL + "/setup"

	response := p.do(http.MethodPost, "/setup", `{
		"name": "Admin",
		"email": "admin@example.com",
		"password": "short",
		"password_confirmation": "short"
	}`, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup with a short password = %d, want 303", response.StatusCode)
	}

	// The errors are flashed, so they arrive with the next render of the page.
	response = p.do(http.MethodGet, "/setup", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Errors map[string]string `json:"errors"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	if page.Props.Errors["password"] == "" {
		t.Fatalf("no error came back for the password, got %v", page.Props.Errors)
	}
}

func TestRouteManifestReachesTheFrontend(t *testing.T) {
	p := start(t)

	response := p.do(http.MethodGet, "/setup", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Ziggy struct {
				Routes map[string]struct {
					URI     string   `json:"uri"`
					Methods []string `json:"methods"`
				} `json:"routes"`
			} `json:"ziggy"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	// Every name the layout's navigation calls route() with. An unknown name
	// makes the helper throw, which takes the whole page down rather than one
	// link.
	for _, name := range []string{
		"dashboard", "logout", "profile.edit", "sites.index", "databases.index",
		"email.index", "terminal", "services.index", "dns.index", "logs.index",
		"settings",
	} {
		if _, ok := page.Props.Ziggy.Routes[name]; !ok {
			t.Errorf("the route manifest is missing %q", name)
		}
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}

	return parsed
}

// A mailbox has a page of its own, and the pages that list mailboxes link to
// it.
//
// Clicking a mailbox used to open the domain it belongs to — a list of every
// address on that domain, none of which could be changed. The three names below
// are what the Email pages call route() with for the new page and its form; an
// unknown name makes the helper throw, which takes the whole page down rather
// than one link.
func TestTheMailboxPageIsRouted(t *testing.T) {
	p := start(t)

	response := p.do(http.MethodGet, "/setup", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Ziggy struct {
				Routes map[string]struct {
					URI     string   `json:"uri"`
					Methods []string `json:"methods"`
				} `json:"routes"`
			} `json:"ziggy"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	show, ok := page.Props.Ziggy.Routes["email.accounts.show"]
	if !ok {
		t.Fatal("the route manifest has no page for a single mailbox")
	}

	if show.URI != "email/mailboxes/{account}" {
		t.Errorf("the mailbox page is at %q", show.URI)
	}

	if _, ok := page.Props.Ziggy.Routes["email.accounts.update"]; !ok {
		t.Error("the route manifest has no way to save a mailbox")
	}

	// The form for a new mailbox lives at the same prefix, and the static path
	// has to keep winning over the one with the id in it.
	if create, ok := page.Props.Ziggy.Routes["email.accounts.new"]; !ok ||
		create.URI != "email/mailboxes/create" {
		t.Errorf("the create form moved: %v", create)
	}
}

// The two mail lists are two addresses, not two tabs.
//
// They shared one URL and a `ref`, which is why deleting a mailbox left the
// browser on the page for the mailbox that had just stopped existing: there was
// nowhere else to send it. /email/mailboxes is that somewhere, and /email is
// the menu's entry pointing at the other one.
func TestTheMailListsAreTheirOwnPages(t *testing.T) {
	p := start(t)
	signIn(t, p)

	response := p.do(http.MethodGet, "/email", "", nil)
	defer response.Body.Close()

	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /email = %d, want a redirect", response.StatusCode)
	}

	if to := response.Header.Get("Location"); to != "/email/domains" {
		t.Errorf("GET /email went to %q, want /email/domains", to)
	}

	for _, path := range []string{"/email/domains", "/email/mailboxes"} {
		page := p.do(http.MethodGet, path, "", nil)
		page.Body.Close()

		if page.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, page.StatusCode)
		}
	}
}

// And the names those pages are linked by are in the manifest, with the list
// URLs not having eaten the ones with an id or a word after them.
func TestTheMailListsAreRouted(t *testing.T) {
	p := start(t)

	response := p.do(http.MethodGet, "/setup", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Ziggy struct {
				Routes map[string]struct {
					URI string `json:"uri"`
				} `json:"routes"`
			} `json:"ziggy"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	for name, want := range map[string]string{
		"email.domains.index":   "email/domains",
		"email.mailboxes.index": "email/mailboxes",
		"email.domains.show":    "email/domains/{domain}",
		"email.accounts.show":   "email/mailboxes/{account}",
		"email.accounts.new":    "email/mailboxes/create",
	} {
		if got, ok := page.Props.Ziggy.Routes[name]; !ok || got.URI != want {
			t.Errorf("%s is at %q, want %q", name, got.URI, want)
		}
	}
}

// And a mailbox that does not exist is a 404 rather than a panic or a blank
// page — the id in that URL comes from whatever somebody typed.
func TestAMissingMailboxIsNotFound(t *testing.T) {
	p := start(t)
	signIn(t, p)

	response := p.do(http.MethodGet, "/email/mailboxes/4321", "", nil)
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET a missing mailbox = %d, want 404", response.StatusCode)
	}
}
