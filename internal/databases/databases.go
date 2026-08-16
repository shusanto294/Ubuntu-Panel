// Package databases creates and drops databases on the engines installed on
// this machine.
//
// Credentials never reach a command line where `ps` could read them: the
// statements are staged into a temporary file and fed to the client with `<`,
// and the file is removed afterwards.
package databases

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Service owns the database lifecycle.
type Service struct {
	Store *store.Store
}

// New wires it up.
func New(st *store.Store) *Service { return &Service{Store: st} }

// SafeIdentifier validates rather than escapes.
//
// SQL has no parameter binding for a database or user name, so the only safe
// form is an allow-list: letters, digits and underscore, nothing else, ever.
// The create form enforces the same rule, and this is the backstop for the
// paths that do not go through it (a site's generated database name).
func SafeIdentifier(text string) bool {
	if text == "" || len(text) > 64 {
		return false
	}

	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}

	return true
}

// sqlQuote is a single-quoted SQL string literal. Only ever used for
// passwords, which are generated from an alphabet that contains no quotes —
// this is the backstop for an operator-supplied one.
func sqlQuote(text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, "'", "''")

	return "'" + text + "'"
}

// stage writes a script to a file only this user can read, and returns its
// path with the function that removes it.
func stage(script string) (string, func(), error) {
	staged, err := os.CreateTemp("", "ubuntu-panel-sql-*")
	if err != nil {
		return "", func() {}, errors.New("could not stage the SQL statement")
	}

	name := staged.Name()
	cleanup := func() { os.Remove(name) }

	staged.Chmod(0o600)

	if _, err := staged.WriteString(script); err != nil {
		staged.Close()
		cleanup()

		return "", func() {}, errors.New("could not stage the SQL statement")
	}

	staged.Close()

	return name, cleanup, nil
}

// runSQL feeds a script to the engine's client with the script staged in a
// file rather than passed on the command line.
func runSQL(runner *tasks.Runner, engine, script string) error {
	name, cleanup, err := stage(script)
	if err != nil {
		return err
	}

	defer cleanup()

	var command, client, label string

	switch engine {
	case "postgres":
		client, label = "psql", "PostgreSQL"
		command = "sudo -u postgres psql -v ON_ERROR_STOP=1 -f " + proc.Quote(name)
	case "mongodb":
		client, label = "mongosh", "MongoDB"
		command = "mongosh --quiet --file " + proc.Quote(name)
	default:
		client, label = "mysql", "MariaDB"
		command = "sudo mysql < " + proc.Quote(name)
	}

	// The command is logged, not the script: the script is the whole point of
	// staging it, and echoing it into the console would put the password back
	// on the page the staging was meant to keep it off.
	if runner != nil {
		runner.Write("$ " + client + " < (staged statement, contents withheld)\n")
	}

	result := proc.Run(command, 5*time.Minute)

	if runner != nil && result.Output != "" {
		runner.Write(result.Output + "\n")
	}

	if result.OK() {
		return nil
	}

	output := result.Output
	if output == "" {
		output = "no output"
	}

	return fmt.Errorf("%s rejected the statement: %s", label, output)
}

// Provision creates the database, its user and its grants.
func (s *Service) Provision(ctx context.Context, databaseID int64, runner *tasks.Runner) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	fail := func(err error) error {
		s.Store.SetDatabaseStatus(databaseID, "failed", err.Error())

		return err
	}

	if !SafeIdentifier(database.Name) || (database.Username != "" && !SafeIdentifier(database.Username)) {
		return fail(errors.New("The database name or username contains characters that are not " +
			"allowed. Only letters, digits and underscore are accepted."))
	}

	if !s.Store.ServiceInstalled(database.Engine) {
		return fail(fmt.Errorf("%s is not installed on this machine.", config.EngineLabel(database.Engine)))
	}

	s.Store.SetDatabaseStatus(databaseID, "creating", "")

	password := sqlQuote(database.Password)
	charset := database.Charset
	if charset == "" {
		charset = "utf8mb4"
	}

	var script string

	switch database.Engine {
	case "postgres":
		script = fmt.Sprintf(
			"CREATE DATABASE %s;\n"+
				"DO $$ BEGIN\n"+
				"  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN\n"+
				"    CREATE ROLE %s LOGIN PASSWORD %s;\n"+
				"  ELSE\n"+
				"    ALTER ROLE %s WITH LOGIN PASSWORD %s;\n"+
				"  END IF;\n"+
				"END $$;\n"+
				"GRANT ALL PRIVILEGES ON DATABASE %s TO %s;\n",
			database.Name, database.Username, database.Username, password,
			database.Username, password, database.Name, database.Username)

	case "mongodb":
		// The server has to be ready before anything can be created on it: a
		// MongoDB with authorization enabled and nobody holding the password
		// rejects every statement below with "requires authentication".
		if err := EnsureMongoServer(s.Store, runner); err != nil {
			return fail(err)
		}

		// Mongo creates the database lazily, so a database with nothing in it
		// does not exist — it would not appear in Compass, and the operator who
		// just created it would be looking at an empty server. One collection
		// makes it real; the application is free to drop it.
		//
		// dbOwner rather than readWrite: this user is the owner of its own
		// database, and managing it visually means creating collections and
		// indexes, which readWrite alone does not allow.
		script = mongoPrelude(s.Store) + fmt.Sprintf(`const target = db.getSiblingDB("%[1]s");
if (target.getUser("%[2]s")) {
  target.updateUser("%[2]s", {pwd: %[3]s, roles: [{role: "dbOwner", db: "%[1]s"}]});
} else {
  target.createUser({user: "%[2]s", pwd: %[3]s, roles: [{role: "dbOwner", db: "%[1]s"}]});
}
if (target.getCollectionNames().length === 0) {
  target.createCollection("panel_placeholder");
}
`, database.Name, database.Username, jsQuote(database.Password))

	default:
		// The server has to be listening off the machine before an account
		// that may only be used from off the machine is worth creating.
		if err := EnsureMySQLRemote(s.Store, runner); err != nil {
			return fail(err)
		}

		script = mysqlAccountScript(database.Name, database.Username, database.Password,
			fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s COLLATE %s_unicode_ci;\n",
				database.Name, charset, charset))
	}

	if err := runSQL(runner, database.Engine, script); err != nil {
		return fail(err)
	}

	s.Store.SetDatabaseStatus(databaseID, "ready", "")

	return nil
}

// Drop removes the database and its user. A record that is already gone is
// not an error: this is usually a step of a site deletion, and nothing should
// stop that.
func (s *Service) Drop(ctx context.Context, databaseID int64, runner *tasks.Runner) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return nil
	}

	if !SafeIdentifier(database.Name) || (database.Username != "" && !SafeIdentifier(database.Username)) {
		return errors.New("Refusing to drop a database whose name is not a plain identifier.")
	}

	s.Store.SetDatabaseStatus(databaseID, "deleting", "")

	var script string

	switch database.Engine {
	case "postgres":
		script = fmt.Sprintf("DROP DATABASE IF EXISTS %s;\nDROP ROLE IF EXISTS %s;\n",
			database.Name, database.Username)
	case "mongodb":
		// The user may already be gone — a half-finished create, or a drop that
		// failed after it. Losing the database over that would leave a row the
		// panel can never remove.
		script = mongoPrelude(s.Store) + fmt.Sprintf(`const target = db.getSiblingDB("%[1]s");
if (target.getUser("%[2]s")) {
  target.dropUser("%[2]s");
}
target.dropDatabase();
`, database.Name, database.Username)
	default:
		script = fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\n", database.Name)

		for _, host := range mysqlHosts {
			script += fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s';\n", database.Username, host)
		}

		script += "FLUSH PRIVILEGES;\n"
	}

	if err := runSQL(runner, database.Engine, script); err != nil {
		s.Store.SetDatabaseStatus(databaseID, "failed", err.Error())

		return err
	}

	s.Store.DeleteDatabase(databaseID)

	return nil
}

// CreateTask is the whole lifecycle as its own task, for the Databases page.
func (s *Service) CreateTask(ctx context.Context, databaseID int64) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "database", "database.create",
		database.UserID, 0, database.Name)
	if err != nil {
		return err
	}

	runner := tasks.New(s.Store, taskID)

	list := &tasks.List{}
	list.AddFn("Create the database", func(runner *tasks.Runner) (string, error) {
		if err := s.Provision(ctx, databaseID, runner); err != nil {
			return "", err
		}

		return "Database created.", nil
	})

	if !runner.Run(list) {
		return errors.New("the database was not created")
	}

	return nil
}

// DeleteTask is the same for a removal.
func (s *Service) DeleteTask(ctx context.Context, databaseID int64) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "database", "database.delete",
		database.UserID, 0, database.Name)
	if err != nil {
		return err
	}

	runner := tasks.New(s.Store, taskID)

	list := &tasks.List{}
	list.AddFn("Drop the database", func(runner *tasks.Runner) (string, error) {
		if err := s.Drop(ctx, databaseID, runner); err != nil {
			return "", err
		}

		return "Database dropped.", nil
	})

	if !runner.Run(list) {
		return errors.New("the database was not dropped")
	}

	return nil
}

// SetPassword changes the password of a database's own user, on the server and
// in the panel's record.
//
// The reason this exists is the one nobody plans for: the password has been
// somewhere it should not have been — a chat message, a screenshot, a
// repository — and the answer is a new one, now, without losing the database.
// So the account is altered rather than dropped and recreated, and everything
// in it is untouched.
//
// The record is only updated once the server has accepted the change. A panel
// showing a password the server does not have is worse than one showing the
// old password, because the old one at least works.
func (s *Service) SetPassword(ctx context.Context, databaseID int64, password string) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if !SafeIdentifier(database.Name) || !SafeIdentifier(database.Username) {
		return errors.New("Refusing to touch an account whose name is not a plain identifier.")
	}

	if !s.Store.ServiceInstalled(database.Engine) {
		return fmt.Errorf("%s is not installed on this machine.", config.EngineLabel(database.Engine))
	}

	var script string

	switch database.Engine {
	case "postgres":
		script = fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %s;\n",
			database.Username, sqlQuote(password))

	case "mongodb":
		script = mongoPrelude(s.Store) + fmt.Sprintf(
			"db.getSiblingDB(\"%s\").changeUserPassword(\"%s\", %s);\n",
			database.Name, database.Username, jsQuote(password))

	default:
		// Every host the account exists at, or the application connecting from
		// one of the others goes on using the old password — which is the
		// whole thing this was called to stop.
		script = mysqlAccountScript(database.Name, database.Username, password, "")
	}

	if err := runSQL(nil, database.Engine, script); err != nil {
		return err
	}

	return s.Store.SetDatabasePassword(databaseID, password)
}

// RepairAccount puts a database's account back the way the panel means it to
// be: existing at every host, with the password the panel holds, and granted
// its own database.
//
// For the databases created before the panel opened MariaDB to the network,
// whose accounts exist for localhost and nowhere else — the credentials the
// panel prints are right and the connection is refused, which reads as a
// password that does not work.
func (s *Service) RepairAccount(ctx context.Context, databaseID int64) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if database.Engine != "mysql" {
		return nil
	}

	if !SafeIdentifier(database.Name) || !SafeIdentifier(database.Username) {
		return errors.New("Refusing to touch an account whose name is not a plain identifier.")
	}

	if err := EnsureMySQLRemote(s.Store, nil); err != nil {
		return err
	}

	return runSQL(nil, database.Engine,
		mysqlAccountScript(database.Name, database.Username, database.Password, ""))
}
