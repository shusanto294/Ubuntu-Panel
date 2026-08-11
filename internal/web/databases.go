package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// databaseEngines is what the Databases pages probe for.
var databaseEngines = []string{"mysql", "postgres", "mongodb"}

// engineLabels is the {key: label} map the pages render their select from.
func engineLabels() map[string]string {
	labels := map[string]string{}

	for _, engine := range config.Engines {
		labels[engine.Key] = engine.Label
	}

	return labels
}

// availableEngines are the ones actually installed on this machine.
func (a *App) availableEngines() []string {
	available := []string{}

	for _, engine := range config.Engines {
		if a.Store.ServiceInstalled(engine.Key) {
			available = append(available, engine.Key)
		}
	}

	return available
}

func (a *App) databasesIndex(w http.ResponseWriter, r *http.Request) {
	// Ask the machine rather than the record: this page is exactly where a
	// stale `failed` row from a half-finished install turns into "you cannot
	// create a database" — on a server that is running MariaDB well enough to
	// be storing this page's session.
	a.Catalog.DetectFresh(databaseEngines, 30*time.Second)

	databases, err := a.Store.Databases(r.Context())
	if err != nil {
		a.serverError(w, r, "could not list the databases", err)
		return
	}

	summaries := make([]map[string]any, 0, len(databases))
	for _, database := range databases {
		summaries = append(summaries, database.Summary())
	}

	a.Render.Render(w, r, "Databases/Index", inertia.Props{
		"databases":        summaries,
		"availableEngines": a.availableEngines(),
		"engines":          engineLabels(),
		"phpMyAdmin":       a.Store.ServiceInstalled("phpmyadmin"),
		"activeTask":       a.activeTask(r, "database"),
	})
}

func (a *App) databasesCreate(w http.ResponseWriter, r *http.Request) {
	a.Catalog.DetectFresh(databaseEngines, 30*time.Second)

	a.Render.Render(w, r, "Databases/Create", inertia.Props{
		"availableEngines": a.availableEngines(),
		"requirements":     a.Catalog.Requirements(r.Context(), databaseEngines),
		"engines":          engineLabels(),
	})
}

func (a *App) databasesStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	engine := input["engine"]
	name := strings.TrimSpace(input["name"])
	username := strings.TrimSpace(input["username"])
	password := input["password"]

	if config.FindEngine(engine) == nil {
		sess.Error("engine", "That is not a database engine the panel manages.")
	}

	if !validIdentifier(name, 60) {
		sess.Error("name", "Use only letters, digits and underscore, up to 60 characters.")
	}

	if username != "" && !validIdentifier(username, 32) {
		sess.Error("username", "Use only letters, digits and underscore, up to 32 characters.")
	}

	if password != "" && len(password) < 8 {
		sess.Error("password", "Use at least 8 characters, or leave it blank to have one generated.")
	}

	if !sess.HasErrors() && !a.Store.ServiceInstalled(engine) {
		sess.Error("engine", config.EngineLabel(engine)+" is not installed on this machine.")
	}

	if !sess.HasErrors() && a.Store.DatabaseExists(r.Context(), engine, name) {
		sess.Error("name", "A database with that name already exists.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/databases/create")
		return
	}

	if username == "" {
		username = truncate(name, 26) + "_u"
	}

	if password == "" {
		password = crypto.Password(24)
	}

	charset := ""
	if engine == "mysql" {
		charset = "utf8mb4"
	}

	id, err := a.Store.CreateDatabase(r.Context(), &store.Database{
		UserID:   user.ID,
		Engine:   engine,
		Name:     name,
		Username: username,
		Password: password,
		Charset:  charset,
	})
	if err != nil || id <= 0 {
		a.Log.Error("could not create the database record", "error", err)
		a.failBack(w, r, "Could not create the database record.")

		return
	}

	a.Queue.PushID(queue.DatabaseCreate, "database", id)

	inertia.Redirect(w, r, "/databases")
}

func (a *App) databasesDestroy(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "database")

	if _, err := a.Store.FindDatabase(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	a.Queue.PushID(queue.DatabaseDelete, "database", id)

	inertia.Back(w, r, "/databases")
}

// databasesCredentials answers the "show credentials" button. The password is
// fetched on demand rather than shipped in the page payload of every list.
func (a *App) databasesCredentials(w http.ResponseWriter, r *http.Request) {
	database, err := a.Store.FindDatabase(r.Context(), routeID(r, "database"))
	if err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	port := 3306
	if engine := config.FindEngine(database.Engine); engine != nil {
		port = engine.Port
	}

	body := map[string]any{
		"host":     "127.0.0.1",
		"port":     port,
		"database": database.Name,
		"username": database.Username,
		"password": database.Password,
	}

	// MongoDB clients take a URI rather than five fields, and the reason to
	// hand one over is that assembling it by hand goes wrong in the same two
	// places every time: the authentication database is the one the user was
	// created in, not `admin`, and the password has to be escaped.
	if database.Engine == "mongodb" {
		body["auth_db"] = database.Name
		body["uri"] = mongoURI("127.0.0.1", port, database.Name, database.Username, database.Password)

		if ip := system.PublicIP(); ip != "" {
			body["remote_host"] = ip
			body["remote_uri"] = mongoURI(ip, port, database.Name, database.Username, database.Password)
		}
	}

	a.writeJSON(w, http.StatusOK, body)
}

// mongoURI is the connection string a driver or MongoDB Compass is given.
func mongoURI(host string, port int, name, username, password string) string {
	return fmt.Sprintf("mongodb://%s@%s:%d/%s?authSource=%s",
		url.UserPassword(username, password).String(), host, port, name, name)
}

func truncate(text string, length int) string {
	if len(text) <= length {
		return text
	}

	return text[:length]
}
