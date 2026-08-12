package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
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

	// What the panel created, and nothing else.
	//
	// 2.5.20 also listed what it found on the server — a database made in
	// phpMyAdmin or from MongoDB Compass appeared here alongside the rest,
	// marked as not the panel's. It was taken out again: those rows had no
	// credentials to show and no Drop, so they were a list of names the page
	// could do nothing with, and every engine had to be interrogated on every
	// load to produce them. MongoDB is reached through the Compass button,
	// which is one string and needs no list.
	summaries := make([]map[string]any, 0, len(databases))
	for _, database := range databases {
		summaries = append(summaries, database.Summary())
	}

	a.Render.Render(w, r, "Databases/Index", inertia.Props{
		"databases":        summaries,
		"availableEngines": a.availableEngines(),
		"engines":          engineLabels(),
		"phpMyAdmin":       a.Store.ServiceInstalled("phpmyadmin"),
		// Served from the panel's own origin, so the link is a path and not a
		// URL: whatever address the operator reached this page on is the one
		// phpMyAdmin should open on, hostname or bare IP, HTTP or HTTPS.
		"phpMyAdminPath": sites.PhpMyAdminPath + "/",
		"mongodb":        a.Store.ServiceInstalled("mongodb"),
		"activeTask":     a.activeTask(r, "database"),
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
	//
	// One URI, on the server's own address rather than loopback. There used to
	// be two — a loopback one and a remote one — and two connection strings for
	// one database is a question rather than an answer. The server's address is
	// an address on the machine's own interface, so it works from a client
	// sitting on the server exactly as it works from a laptop.
	if database.Engine == "mongodb" {
		body["host"] = mongoHost()
		body["auth_db"] = database.Name
		body["uri"] = mongoURI(mongoHost(), port, database.Name, database.Username, database.Password)
	}

	a.writeJSON(w, http.StatusOK, body)
}

// databasesMongoConnection is the server-wide connection string for MongoDB
// Compass: the panel's own administrator account, which can see every database
// on the machine rather than the one a site was given.
//
// Fetched on demand, like the per-database credentials, so the root password is
// not in the payload of a page anybody can leave open.
func (a *App) databasesMongoConnection(w http.ResponseWriter, r *http.Request) {
	if !a.Store.ServiceInstalled("mongodb") {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"error": "MongoDB is not installed on this machine.",
		})

		return
	}

	// Read, never minted. RememberSecret would generate one on a machine that
	// has none — and hand over a password MongoDB has never been told about,
	// which is a worse answer than saying there is nothing to give yet. The
	// account is created the first time a MongoDB database is.
	password := a.Store.SecretSetting(databases.MongoSecret)

	if password == "" {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"error": "MongoDB is installed, but the panel has not set up its administrator " +
				"account yet — that happens the first time a MongoDB database is created. " +
				"Create one, then this string will work.",
		})

		return
	}

	host := mongoHost()

	// authSource=admin, not the database being browsed: the panel's account was
	// created in admin, and Compass authenticating against anything else fails
	// with "Authentication failed" on a password that is perfectly correct.
	a.writeJSON(w, http.StatusOK, map[string]any{
		"host":     host,
		"port":     databases.MongoPort,
		"username": databases.MongoAdmin,
		"password": password,
		"auth_db":  "admin",
		"uri": fmt.Sprintf("mongodb://%s@%s:%d/?authSource=admin",
			url.UserPassword(databases.MongoAdmin, password).String(), host, databases.MongoPort),
	})
}

// mongoHost is the address every MongoDB string the panel hands out is built
// on: the one this machine answers to on its own network interface.
//
// Not 127.0.0.1. MongoDB here listens on every interface, and this address is
// on an interface of the machine's own — so the one string works from a laptop
// running Compass and from a process on the server alike. Two strings for one
// database, one of which only worked in one of those places, was a choice
// nobody wanted to have to make.
//
// Loopback only when the panel cannot work out any other address, where a
// string that works on the server is better than no string at all.
func mongoHost() string {
	if ip := system.PublicIP(); ip != "" {
		return ip
	}

	return "127.0.0.1"
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
