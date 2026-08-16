package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/databases"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
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
		// Two ways in, and they are different links.
		//
		// The header one signs itself in as the panel's own MariaDB
		// administrator, so it opens on every database at once. The one beside
		// a database opens the same phpMyAdmin at its login form, with that
		// database named — sign in with the credentials shown next to it and
		// nothing else on the server is reachable.
		//
		// Paths rather than URLs: phpMyAdmin is served from the panel's own
		// origin, so whatever address this page was reached on is the one it
		// should open on, hostname or bare IP, HTTP or HTTPS.
		"phpMyAdminAll":    a.phpMyAdminURL(phpMyAdminServerAll),
		"phpMyAdminManual": a.phpMyAdminURL(phpMyAdminServerManual),
		"mongodb":          a.Store.ServiceInstalled("mongodb"),
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

	body := databaseCredentials(database, port)

	a.writeJSON(w, http.StatusOK, body)
}

// databaseCredentials is everything needed to connect to one database, in the
// shape both the credentials endpoint and the database's own page use.
//
// The host is the address an application should actually use, which is not
// always loopback. MariaDB and MongoDB are both listening on every interface
// here — the panel opens them so an application on another machine can connect
// with the credentials it prints — so naming 127.0.0.1 would be printing an
// address that only works in one of the two places the credentials are for.
//
// PostgreSQL keeps loopback, because the panel has not opened it.
func databaseCredentials(database *store.Database, port int) map[string]any {
	host := "127.0.0.1"

	if database.Engine == "mysql" || database.Engine == "mongodb" {
		host = serverAddress()
	}

	body := map[string]any{
		"host":     host,
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
		body["uri"] = mongoURI(host, port, database.Name, database.Username, database.Password)
	}

	return body
}

// serverAddress is the address this machine answers to on its own network
// interface — the one to print for a database an application off the machine
// is meant to reach. Loopback only when the panel cannot work out any other,
// where an address that works on the server beats none at all.
func serverAddress() string {
	if ip := system.PublicIP(); ip != "" {
		return ip
	}

	return "127.0.0.1"
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

// databasesShow is one database's own page: what it is, how to connect to it,
// and the two things that can be done to it.
//
// The credentials are rendered into this page rather than fetched, unlike the
// list — a page about one database is a page whose whole purpose is those
// details, and asking for them a second time to display them is a round trip
// for nothing.
func (a *App) databasesShow(w http.ResponseWriter, r *http.Request) {
	database, err := a.Store.FindDatabase(r.Context(), routeID(r, "database"))
	if err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	port := 3306
	if engine := config.FindEngine(database.Engine); engine != nil {
		port = engine.Port
	}

	summary := database.Summary()
	summary["credentials"] = databaseCredentials(database, port)

	users, err := a.Databases.Users(r.Context(), database.ID)
	if err != nil {
		// A listing the engine would not give is not a reason to lose the page:
		// the credentials above it are still what somebody came for.
		a.Log.Warn("could not list a database's users", "database", database.Name, "error", err)

		users = nil
	}

	a.Render.Render(w, r, "Databases/Show", inertia.Props{
		"database":   summary,
		"users":      users,
		"phpMyAdmin": a.Store.ServiceInstalled("phpmyadmin"),
		"phpMyAdminManual": a.phpMyAdminURL(phpMyAdminServerManual) +
			separatorFor(a.phpMyAdminURL(phpMyAdminServerManual)) +
			"db=" + url.QueryEscape(database.Name),
		"activeTask": a.activeTask(r, "database"),
	})
}

// separatorFor is whether a query string has been started already.
func separatorFor(link string) string {
	if strings.Contains(link, "?") {
		return "&"
	}

	return "?"
}

// databasesPassword changes the password of a database's own user.
//
// Done in the request rather than on the queue: it is one statement, the
// operator is waiting to copy the result, and a password that changes some
// seconds later is a password they will have copied the old version of.
func (a *App) databasesPassword(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "database")

	database, err := a.Store.FindDatabase(r.Context(), id)
	if err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	password := input["password"]

	// Generated when none was typed, which is the better answer anyway: the
	// reason to change a password is usually that the old one got out, and a
	// generated one cannot be the old one with a digit on the end.
	if strings.TrimSpace(password) == "" {
		password = crypto.Password(24)
	} else if len(password) < 8 {
		sess.Error("password", "Use at least 8 characters, or leave it blank to have one generated.")

		inertia.Back(w, r, "/databases")

		return
	}

	if err := a.Databases.SetPassword(r.Context(), id, password); err != nil {
		a.Log.Error("could not change the database password", "database", id, "error", err)
		a.failBack(w, r, err.Error())

		return
	}

	sess.Flash("success", "The password for "+database.Username+" was changed.")

	inertia.Back(w, r, "/databases")
}

// databasesRepair puts a database's account back at every host the panel
// creates accounts for, with the password the panel holds.
//
// For a database made before MariaDB was opened to the network: its account
// exists for localhost and nowhere else, so the credentials this page prints
// are correct and the connection from anywhere else is refused — which reads
// as a password that does not work.
func (a *App) databasesRepair(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "database")

	if _, err := a.Store.FindDatabase(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	if err := a.Databases.RepairAccount(r.Context(), id); err != nil {
		a.Log.Error("could not repair the database account", "database", id, "error", err)
		a.failBack(w, r, err.Error())

		return
	}

	sess.Flash("success", "The account was rebuilt. It now works from this server and from off it.")

	inertia.Back(w, r, "/databases")
}

// databasesUsers is the one endpoint behind the users table: add, change a
// password, remove.
//
// One endpoint and an action, rather than three routes: they are three forms
// against the same database, and the checks that matter — is this a name the
// panel will accept, is this the account the site is configured with — are the
// same for all of them.
func (a *App) databasesUsers(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "database")

	if _, err := a.Store.FindDatabase(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such database.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	username := strings.TrimSpace(input["username"])
	password := input["password"]

	// Generated when none was typed. The reason to set a password is usually
	// that the last one got out, and one chosen in that moment is the old one
	// with a digit on the end.
	if strings.TrimSpace(password) == "" {
		password = crypto.Password(24)
	}

	switch input["action"] {
	case "create":
		err = a.Databases.CreateUser(r.Context(), id, username, password)

		if err == nil {
			sess.Flash("success", username+" was created. Its password is "+password+
				" — the panel does not store it, so copy it now.")
		}

	case "password":
		err = a.Databases.SetUserPassword(r.Context(), id, username, password)

		if err == nil {
			sess.Flash("success", "The password for "+username+" is now "+password+
				" — copy it before leaving this page.")
		}

	case "delete":
		err = a.Databases.DropUser(r.Context(), id, username)

		if err == nil {
			sess.Flash("danger", username+" no longer has access to this database.")
		}

	default:
		err = errors.New("that is not something the panel does to a database user")
	}

	if err != nil {
		a.failBack(w, r, err.Error())

		return
	}

	inertia.Back(w, r, "/databases")
}
