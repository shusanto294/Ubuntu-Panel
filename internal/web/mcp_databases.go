package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The rest of the Databases pages: one database in full, the two repairs, the
// users table, and the connection string the MongoDB button hands over.

// mcpDatabase resolves the `database` argument, which is an id or a name.
func (a *App) mcpDatabase(ctx context.Context, args mcp.Args) (*store.Database, error) {
	reference := args.StringOr("database", args.String("name"))

	if reference == "" && args.Int("database_id") > 0 {
		reference = strconv.FormatInt(args.Int("database_id"), 10)
	}

	if reference == "" {
		return nil, errors.New("name the database, by id or by name, in the `database` argument")
	}

	if id, err := strconv.ParseInt(reference, 10, 64); err == nil {
		database, err := a.Store.FindDatabase(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("no database with id %d", id)
		}

		return database, nil
	}

	rows, err := a.Store.Databases(ctx)
	if err != nil {
		return nil, errors.New("the panel could not read its database list")
	}

	for _, database := range rows {
		if strings.EqualFold(database.Name, reference) {
			return database, nil
		}
	}

	return nil, fmt.Errorf("no database called %s — list_databases has the ones this panel manages",
		reference)
}

// databasePort is the port an engine answers on.
func databasePort(engine string) int {
	if meta := config.FindEngine(engine); meta != nil {
		return meta.Port
	}

	return 3306
}

func (a *App) mcpGetDatabase() *mcp.Tool {
	return &mcp.Tool{
		Name:       "get_database",
		Capability: capDatabases,
		Title:      "Get a database",
		ReadOnly:   true,
		Description: "One database with everything needed to connect to it — host, port, user, " +
			"password, and the URI for MongoDB — and the accounts that can reach it.",
		Properties: map[string]mcp.Property{
			"database": {Description: "The database's id, or its name."},
		},
		Required: []string{"database"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			database, err := a.mcpDatabase(ctx, args)
			if err != nil {
				return nil, err
			}

			summary := database.Summary()
			summary["credentials"] = databaseCredentials(database, databasePort(database.Engine))

			if users, err := a.Databases.Users(ctx, database.ID); err == nil {
				summary["users"] = users
			}

			return summary, nil
		},
	}
}

func (a *App) mcpDeleteDatabase() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_database",
		Capability:  capDatabases,
		Title:       "Drop a database",
		Destructive: true,
		Description: "Drop a database and the user the panel created with it. Everything in it " +
			"goes, and this cannot be undone. A database belonging to a site goes with the site — " +
			"delete_site does that, and dropping it from underneath a live site breaks the site.",
		Properties: map[string]mcp.Property{
			"database": {Description: "The database's id, or its name."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Required: []string{"database"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			database, err := a.mcpDatabase(ctx, args)
			if err != nil {
				return nil, err
			}

			what := "Dropping the database " + database.Name + " and everything in it"

			if database.ManagedBySite {
				what += ", which belongs to a site the panel deployed"
			}

			if err := a.requireConfirmation(args, what); err != nil {
				return nil, err
			}

			a.Queue.PushID(queue.DatabaseDelete, "database", database.ID)

			return map[string]any{
				"database_id": database.ID,
				"name":        database.Name,
				"queued":      true,
				"next":        "The drop is running on the queue; list_databases says when it is gone.",
			}, nil
		},
	}
}

func (a *App) mcpSetDatabasePassword() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_database_password",
		Capability: capDatabases,
		Title:      "Change a database password",
		Description: "Change the password of the user the panel created with a database, on the " +
			"engine and in the panel's own record. Generated when none is sent, and returned " +
			"either way.\n\n" +
			"An application configured with the old one stops connecting the moment this runs — " +
			"a site's own .env included — so say so before doing it.",
		Properties: map[string]mcp.Property{
			"database": {Description: "The database's id, or its name."},
			"password": {Description: "At least eight characters. Generated when left out."},
		},
		Required: []string{"database"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			database, err := a.mcpDatabase(ctx, args)
			if err != nil {
				return nil, err
			}

			password := args.String("password")
			generated := false

			if password == "" {
				password = crypto.Password(24)
				generated = true
			} else if len(password) < 8 {
				return nil, errors.New("a database password is at least eight characters")
			}

			if err := a.Databases.SetPassword(ctx, database.ID, password); err != nil {
				return nil, err
			}

			return map[string]any{
				"database_id":        database.ID,
				"name":               database.Name,
				"username":           database.Username,
				"password":           password,
				"password_generated": generated,
			}, nil
		},
	}
}

func (a *App) mcpRepairDatabase() *mcp.Tool {
	return &mcp.Tool{
		Name:       "repair_database",
		Capability: capDatabases,
		Title:      "Repair a database account",
		Description: "Put a database's own account back at every host the panel creates accounts " +
			"for, with the password the panel holds. This is the answer to credentials that are " +
			"correct and refused from off the machine.",
		Properties: map[string]mcp.Property{
			"database": {Description: "The database's id, or its name."},
		},
		Required: []string{"database"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			database, err := a.mcpDatabase(ctx, args)
			if err != nil {
				return nil, err
			}

			if err := a.Databases.RepairAccount(ctx, database.ID); err != nil {
				return nil, err
			}

			return map[string]any{
				"database_id": database.ID,
				"name":        database.Name,
				"repaired":    true,
				"credentials": databaseCredentials(database, databasePort(database.Engine)),
			}, nil
		},
	}
}

func (a *App) mcpDatabaseUsers() *mcp.Tool {
	return &mcp.Tool{
		Name:       "manage_database_users",
		Capability: capDatabases,
		Title:      "Manage a database's users",
		Description: "The accounts that can reach one database: list them, add one, change one's " +
			"password, or take one's access away. `action` says which — leave it out to list.\n\n" +
			"A created or changed password is returned once and not stored, so pass it on.",
		Properties: map[string]mcp.Property{
			"database": {Description: "The database's id, or its name."},
			"action": {Description: "What to do.", Default: "list",
				Enum: []string{"list", "create", "password", "delete"}},
			"username": {Description: "The account, for anything but `list`."},
			"password": {Description: "For `create` and `password`. Generated when left out."},
		},
		Required: []string{"database"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			database, err := a.mcpDatabase(ctx, args)
			if err != nil {
				return nil, err
			}

			action := args.StringOr("action", "list")

			if action == "list" {
				users, err := a.Databases.Users(ctx, database.ID)
				if err != nil {
					return nil, err
				}

				return map[string]any{
					"database_id": database.ID,
					"name":        database.Name,
					"users":       users,
				}, nil
			}

			username := args.String("username")
			if username == "" {
				return nil, errors.New("`username` is which account this is about")
			}

			password := args.String("password")
			generated := false

			if password == "" {
				password = crypto.Password(24)
				generated = true
			}

			answer := map[string]any{
				"database_id": database.ID,
				"name":        database.Name,
				"username":    username,
				"action":      action,
			}

			switch action {
			case "create":
				if err := a.Databases.CreateUser(ctx, database.ID, username, password); err != nil {
					return nil, err
				}

				answer["password"] = password
				answer["password_generated"] = generated
				answer["created"] = true

			case "password":
				if err := a.Databases.SetUserPassword(ctx, database.ID, username, password); err != nil {
					return nil, err
				}

				answer["password"] = password
				answer["password_generated"] = generated

			case "delete":
				if err := a.requireConfirmation(args,
					"Removing "+username+"'s access to "+database.Name); err != nil {
					return nil, err
				}

				if err := a.Databases.DropUser(ctx, database.ID, username); err != nil {
					return nil, err
				}

				answer["deleted"] = true

			default:
				return nil, errors.New("`action` is list, create, password or delete")
			}

			return answer, nil
		},
	}
}

func (a *App) mcpMongoConnection() *mcp.Tool {
	return &mcp.Tool{
		Name:       "mongo_connection",
		Capability: capDatabases,
		Title:      "MongoDB administrator connection",
		ReadOnly:   true,
		Description: "The server-wide MongoDB connection string — the panel's own administrator " +
			"account, which sees every database on the machine rather than the one a site was " +
			"given. This is what MongoDB Compass is pointed at.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			if !a.Store.ServiceInstalled("mongodb") {
				return nil, errors.New("MongoDB is not installed on this machine — install_apps " +
					"with `mongodb` puts it there")
			}

			password := a.Store.SecretSetting(databases.MongoSecret)

			if password == "" {
				return nil, errors.New("MongoDB is installed, but the panel has not set up its " +
					"administrator account yet — that happens the first time a MongoDB database " +
					"is created")
			}

			host := mongoHost()

			return map[string]any{
				"host":     host,
				"port":     databases.MongoPort,
				"username": databases.MongoAdmin,
				"password": password,
				"auth_db":  "admin",
				// Escaped rather than concatenated: a generated password can hold
				// characters a URI reads as its own punctuation.
				"uri": fmt.Sprintf("mongodb://%s@%s:%d/?authSource=admin",
					url.UserPassword(databases.MongoAdmin, password).String(), host,
					databases.MongoPort),
			}, nil
		},
	}
}
