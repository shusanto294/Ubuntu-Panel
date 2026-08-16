package databases

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// DatabaseUser is one account that can reach a database.
//
// Named by user alone, not by user and host. MariaDB identifies an account by
// both, and the panel creates every one of its accounts at three hosts with
// one password — so showing three rows for what an operator made once would be
// showing them the implementation.
type DatabaseUser struct {
	Name    string `json:"name"`
	Hosts   string `json:"hosts"`
	Primary bool   `json:"primary"`
}

// Users are the accounts with access to one database, as the engine reports
// them.
//
// Asked of the engine rather than kept in the panel's table: an account made in
// phpMyAdmin is an account that can read this database, and a list that only
// showed the panel's own would be answering a different question.
func (s *Service) Users(ctx context.Context, databaseID int64) ([]DatabaseUser, error) {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return nil, errors.New("The database record no longer exists.")
	}

	if !SafeIdentifier(database.Name) {
		return nil, errors.New("That database's name is not a plain identifier.")
	}

	var result proc.Result

	switch database.Engine {
	case "postgres":
		// Roles that can log in and are allowed to connect. The engine's own
		// accounts are filtered out by name, which is how PostgreSQL marks
		// them.
		result = proc.Run("sudo -u postgres psql -Atqc "+proc.Quote(fmt.Sprintf(
			"SELECT rolname FROM pg_roles WHERE rolcanlogin "+
				"AND has_database_privilege(rolname, '%s', 'CONNECT') "+
				"AND rolname NOT LIKE 'pg\\_%%' AND rolname <> 'postgres'", database.Name))+
			" 2>/dev/null", time.Minute)

	case "mongodb":
		script := mongoPrelude(s.Store) + fmt.Sprintf(
			"for (const user of db.getSiblingDB(\"%s\").getUsers().users || []) { print(user.user); }\n",
			database.Name)

		result = mongoRun(script, time.Minute)

	default:
		// mysql.db is the table that says who was granted what on a schema,
		// which is what "a user of this database" means here. A superuser with
		// global privileges can also read it and is not listed: that is an
		// account of the server, not of this database.
		result = proc.Run("sudo mysql -N -B -e "+proc.Quote(fmt.Sprintf(
			"SELECT DISTINCT User FROM mysql.db WHERE Db = '%s'", database.Name))+
			" 2>/dev/null", time.Minute)
	}

	if !result.OK() {
		return nil, errors.New("The engine would not list this database's users.")
	}

	seen := map[string]bool{}
	users := []DatabaseUser{}

	for _, line := range strings.Split(result.Output, "\n") {
		name := strings.TrimSpace(line)

		if name == "" || seen[name] || strings.ContainsAny(name, " \t") {
			continue
		}

		seen[name] = true

		users = append(users, DatabaseUser{
			Name:    name,
			Primary: name == database.Username,
		})
	}

	sort.Slice(users, func(i, j int) bool {
		// The account the panel made first, then the rest by name: it is the
		// one the site is configured with.
		if users[i].Primary != users[j].Primary {
			return users[i].Primary
		}

		return users[i].Name < users[j].Name
	})

	// The panel's own account is listed even where the engine did not report
	// it — a database whose user was dropped by hand is worth showing as
	// missing rather than as absent.
	if !seen[database.Username] && database.Username != "" {
		users = append([]DatabaseUser{{Name: database.Username, Primary: true}}, users...)
	}

	if database.Engine == "mysql" {
		for i := range users {
			users[i].Hosts = strings.Join(mysqlHosts, ", ")
		}
	}

	return users, nil
}

// CreateUser adds an account to a database.
func (s *Service) CreateUser(ctx context.Context, databaseID int64, username, password string) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if !SafeIdentifier(username) {
		return errors.New("Use only letters, digits and underscore for a username.")
	}

	if len(password) < 8 {
		return errors.New("Use a password of at least 8 characters.")
	}

	if !s.Store.ServiceInstalled(database.Engine) {
		return fmt.Errorf("%s is not installed on this machine.", config.EngineLabel(database.Engine))
	}

	var script string

	switch database.Engine {
	case "postgres":
		script = fmt.Sprintf(
			"DO $$ BEGIN\n"+
				"  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN\n"+
				"    CREATE ROLE %[1]s LOGIN PASSWORD %[2]s;\n"+
				"  ELSE\n"+
				"    ALTER ROLE %[1]s WITH LOGIN PASSWORD %[2]s;\n"+
				"  END IF;\n"+
				"END $$;\n"+
				"GRANT ALL PRIVILEGES ON DATABASE %[3]s TO %[1]s;\n",
			username, sqlQuote(password), database.Name)

	case "mongodb":
		script = mongoPrelude(s.Store) + fmt.Sprintf(`const target = db.getSiblingDB("%[1]s");
if (target.getUser("%[2]s")) {
  target.updateUser("%[2]s", {pwd: %[3]s, roles: [{role: "readWrite", db: "%[1]s"}]});
} else {
  target.createUser({user: "%[2]s", pwd: %[3]s, roles: [{role: "readWrite", db: "%[1]s"}]});
}
`, database.Name, username, jsQuote(password))

	default:
		// The same three hosts every panel account exists at, so a second user
		// works from this machine and from off it exactly as the first does.
		script = mysqlAccountScript(database.Name, username, password, "")
	}

	return runSQL(nil, database.Engine, script)
}

// SetUserPassword changes one account's password.
//
// The panel's record follows only for the account the database was created
// with, which is the one a site's configuration file names. The others are
// nobody's business but the person who made them — the panel does not store
// their passwords, so it shows them once and forgets.
func (s *Service) SetUserPassword(ctx context.Context, databaseID int64, username, password string) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if !SafeIdentifier(username) {
		return errors.New("That is not a username the panel manages.")
	}

	if len(password) < 8 {
		return errors.New("Use a password of at least 8 characters.")
	}

	if username == database.Username {
		return s.SetPassword(ctx, databaseID, password)
	}

	// Not the primary account, so this is the same work as creating it — every
	// statement involved is an upsert.
	return s.CreateUser(ctx, databaseID, username, password)
}

// DropUser removes an account from a database.
func (s *Service) DropUser(ctx context.Context, databaseID int64, username string) error {
	database, err := s.Store.FindDatabase(ctx, databaseID)
	if err != nil {
		return errors.New("The database record no longer exists.")
	}

	if !SafeIdentifier(username) {
		return errors.New("That is not a username the panel manages.")
	}

	// The account the database was created with stays. A site's configuration
	// names it, and dropping it from here would take the site down from a page
	// that says nothing about the site.
	if username == database.Username {
		return errors.New("This is the account the database was created with, and the site " +
			"that uses it is configured with its name. Change its password instead, or drop " +
			"the whole database.")
	}

	var script string

	switch database.Engine {
	case "postgres":
		// The privileges first: PostgreSQL refuses to drop a role that still
		// owns or is granted anything.
		script = fmt.Sprintf(
			"REVOKE ALL PRIVILEGES ON DATABASE %s FROM %s;\nDROP ROLE IF EXISTS %s;\n",
			database.Name, username, username)

	case "mongodb":
		script = mongoPrelude(s.Store) + fmt.Sprintf(`const target = db.getSiblingDB("%s");
if (target.getUser("%s")) {
  target.dropUser("%s");
}
`, database.Name, username, username)

	default:
		script = ""

		for _, host := range mysqlHosts {
			script += fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s';\n", username, host)
		}

		script += "FLUSH PRIVILEGES;\n"
	}

	return runSQL(nil, database.Engine, script)
}
