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

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
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

// runSQL feeds a script to the engine's client with the script staged in a
// file rather than passed on the command line.
func runSQL(runner *tasks.Runner, engine, script string) error {
	staged, err := os.CreateTemp("", "ubuntu-panel-sql-*")
	if err != nil {
		return errors.New("could not stage the SQL statement")
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)

	if _, err := staged.WriteString(script); err != nil {
		staged.Close()
		return errors.New("could not stage the SQL statement")
	}

	staged.Close()

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
		// Mongo creates the database lazily; the user is what has to exist.
		script = fmt.Sprintf(
			"db.getSiblingDB(\"%s\").createUser({user:\"%s\",pwd:%s,roles:[{role:\"readWrite\",db:\"%s\"}]});\n",
			database.Name, database.Username, password, database.Name)

	default:
		script = fmt.Sprintf(
			"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s COLLATE %s_unicode_ci;\n"+
				"CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY %s;\n"+
				"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY %s;\n"+
				"ALTER USER '%s'@'127.0.0.1' IDENTIFIED BY %s;\n"+
				"ALTER USER '%s'@'localhost' IDENTIFIED BY %s;\n"+
				"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1';\n"+
				"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\n"+
				"FLUSH PRIVILEGES;\n",
			database.Name, charset, charset,
			database.Username, password,
			database.Username, password,
			database.Username, password,
			database.Username, password,
			database.Name, database.Username,
			database.Name, database.Username)
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
		script = fmt.Sprintf("db.getSiblingDB(\"%s\").dropUser(\"%s\");\ndb.getSiblingDB(\"%s\").dropDatabase();\n",
			database.Name, database.Username, database.Name)
	default:
		script = fmt.Sprintf(
			"DROP DATABASE IF EXISTS `%s`;\n"+
				"DROP USER IF EXISTS '%s'@'127.0.0.1';\n"+
				"DROP USER IF EXISTS '%s'@'localhost';\n"+
				"FLUSH PRIVILEGES;\n",
			database.Name, database.Username, database.Username)
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
