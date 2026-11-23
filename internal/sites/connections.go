package sites

// A Node site's connections: the Redis and the database the panel set up for
// it, handed to the app as the environment variables it already reads.
//
// The panel installs Redis with a password and MongoDB with authentication, and
// until this an app had no way to learn either: REDIS_URL defaulted to
// redis://127.0.0.1:6379, which Redis refuses (or, with Redis not installed at
// all, waits on until the client times out). Connecting one here installs what
// is missing, makes what the app needs, and puts the URL in its environment —
// REDIS_URL, MONGODB_URI or DATABASE_URL, the names nearly every Node library
// and framework looks for.
//
// These variables are written by the panel, and they win over a variable of the
// same name on the Environment tab: connecting Redis is the operator saying
// "use this one".

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Redis, as the panel installs it: on this machine only.
const (
	RedisHost = "127.0.0.1"
	RedisPort = 6379

	// redisDatabases is Redis's default `databases 16`. 0 is left to the
	// WordPress object caches, which share it under per-site prefixes.
	redisDatabases = 16
)

// AppDatabaseEngines are the engines a Node site can be given a database in.
var AppDatabaseEngines = []string{"mongodb", "mysql"}

// ValidAppDatabaseEngine reports whether a Node site can be given one.
func ValidAppDatabaseEngine(engine string) bool {
	for _, known := range AppDatabaseEngines {
		if engine == known {
			return true
		}
	}

	return false
}

// ManagedVar is one variable the panel sets, and what set it.
type ManagedVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ConnectRedis gives the site a Redis database of its own. Already connected
// is not an error: it keeps the one it has.
//
// A database of its own rather than a key prefix: a Node app's queue and cache
// libraries choose their own key names, and two apps both using BullMQ's
// default `bull:` prefix in one database would be reading each other's jobs.
// The password is shared — Redis has one — so this keeps apps from colliding,
// not from each other; every site on a server is one operator's anyway.
func (s *Service) ConnectRedis(ctx context.Context, site *store.Site) (int, error) {
	if site.RedisDB > 0 {
		return site.RedisDB, nil
	}

	used := s.Store.UsedRedisDBs(ctx)

	for db := 1; db < redisDatabases; db++ {
		if !used[db] {
			s.Store.SetSiteRedisDB(site.ID, db)
			site.RedisDB = db

			return db, nil
		}
	}

	return 0, fmt.Errorf("all %d Redis databases are taken by other sites", redisDatabases-1)
}

// DisconnectRedis takes the site's Redis database away from its environment.
// The data in it is left alone: disconnecting is not deleting, and a queue's
// unfinished jobs are worth keeping until somebody decides otherwise.
func (s *Service) DisconnectRedis(site *store.Site) {
	s.Store.SetSiteRedisDB(site.ID, 0)
	site.RedisDB = 0
}

// CreateAppDatabase makes the database record a Node site connects to, and
// links it. The deployment, or a connection job, provisions it.
func (s *Service) CreateAppDatabase(ctx context.Context, site *store.Site, engine string) (int64, error) {
	if site.DatabaseID > 0 {
		return site.DatabaseID, nil
	}

	if !ValidAppDatabaseEngine(engine) {
		return 0, fmt.Errorf("%q is not a database the panel can give a Node site", engine)
	}

	base := sanitiseIdentifier(site.Domain)
	if len(base) > 40 {
		base = base[:40]
	}

	user := base
	if len(user) > 20 {
		user = user[:20]
	}

	suffix := strings.ToLower(crypto.Password(4))

	id, err := s.Store.CreateDatabase(ctx, &store.Database{
		UserID:        site.UserID,
		Engine:        engine,
		Name:          base + "_" + suffix,
		Username:      user + "_" + suffix,
		Password:      crypto.Password(24),
		Charset:       "utf8mb4",
		ManagedBySite: true,
	})
	if err != nil || id <= 0 {
		return 0, errors.New("could not create the database record")
	}

	s.Store.SetSiteDatabase(site.ID, id)
	site.DatabaseID = id

	return id, nil
}

// ConnectionServices are the catalogue entries a site's connections need.
func (s *Service) ConnectionServices(ctx context.Context, site *store.Site) []string {
	keys := []string{}

	if site.RedisDB > 0 {
		keys = append(keys, "redis")
	}

	if site.IsProxied() && site.DatabaseID > 0 {
		if database, err := s.Store.FindDatabase(ctx, site.DatabaseID); err == nil {
			keys = append(keys, database.Engine)
		}
	}

	return keys
}

// ManagedEnv is what the panel sets for this site's connections.
func (s *Service) ManagedEnv(ctx context.Context, site *store.Site) []ManagedVar {
	vars := []ManagedVar{}

	if !site.IsProxied() || site.IsManaged() {
		return vars
	}

	if site.RedisDB > 0 {
		if password := s.Store.SecretSetting("redis_password"); password != "" {
			vars = append(vars, ManagedVar{Name: "REDIS_URL", Source: "Redis", Value: (&url.URL{
				Scheme: "redis",
				User:   url.UserPassword("", password),
				Host:   fmt.Sprintf("%s:%d", RedisHost, RedisPort),
				Path:   fmt.Sprintf("/%d", site.RedisDB),
			}).String()})
		}
	}

	if site.DatabaseID > 0 {
		if database, err := s.Store.FindDatabase(ctx, site.DatabaseID); err == nil {
			if v, ok := databaseVar(database); ok {
				vars = append(vars, v)
			}
		}
	}

	return vars
}

// databaseVar is the connection string for a database, under the name apps
// look for it by.
func databaseVar(database *store.Database) (ManagedVar, bool) {
	engine := config.FindEngine(database.Engine)
	if engine == nil {
		return ManagedVar{}, false
	}

	host := fmt.Sprintf("127.0.0.1:%d", engine.Port)
	user := url.UserPassword(database.Username, database.Password)

	switch database.Engine {
	case "mongodb":
		return ManagedVar{Name: "MONGODB_URI", Source: engine.Label, Value: (&url.URL{
			Scheme: "mongodb", User: user, Host: host, Path: "/" + database.Name,
			RawQuery: "authSource=" + url.QueryEscape(database.Name),
		}).String()}, true

	case "mysql":
		return ManagedVar{Name: "DATABASE_URL", Source: engine.Label, Value: (&url.URL{
			Scheme: "mysql", User: user, Host: host, Path: "/" + database.Name,
		}).String()}, true

	case "postgres":
		return ManagedVar{Name: "DATABASE_URL", Source: engine.Label, Value: (&url.URL{
			Scheme: "postgresql", User: user, Host: host, Path: "/" + database.Name,
		}).String()}, true
	}

	return ManagedVar{}, false
}

// envFor is everything the site's build and service get: what the operator
// wrote, with the panel's connection variables in place of any of the same
// name.
func (s *Service) envFor(ctx context.Context, site *store.Site) []EnvVar {
	managed := s.ManagedEnv(ctx, site)

	taken := map[string]bool{}
	for _, v := range managed {
		taken[v.Name] = true
	}

	vars := []EnvVar{}

	for _, v := range siteEnv(site) {
		if !taken[v.Name] {
			vars = append(vars, v)
		}
	}

	for _, v := range managed {
		vars = append(vars, EnvVar{Name: v.Name, Value: v.Value})
	}

	return vars
}

// prepareRedis makes Redis safe to keep an app's data in.
//
// The panel installed it as a cache: capped, and allkeys-lru, so a full Redis
// throws away whatever was used least. That is right for a WordPress object
// cache and wrong for anything a Node app keeps there — a BullMQ queue whose
// jobs are evicted loses them without an error, and BullMQ refuses to start
// quietly on it. noeviction makes a full Redis refuse writes instead, which an
// app reports. The WordPress caches on the same Redis set a TTL on every entry,
// so they still empty themselves.
//
// Written to redis.conf and applied live with CONFIG SET, so nothing restarts.
// Run on every deploy of a connected site, because reinstalling Redis writes
// the cache policy back.
func (s *Service) prepareRedis(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		password := s.Store.SecretSetting("redis_password")
		if password == "" {
			return "", errors.New("the panel has no Redis password on record — " +
				"Redis was not installed by the panel")
		}

		conf := "sudo sed -i \"s/^# *maxmemory-policy .*/maxmemory-policy noeviction/; " +
			"s/^maxmemory-policy .*/maxmemory-policy noeviction/\" /etc/redis/redis.conf; " +
			"grep -q '^maxmemory-policy' /etc/redis/redis.conf || " +
			"echo 'maxmemory-policy noeviction' | sudo tee -a /etc/redis/redis.conf > /dev/null"

		if err := runner.Must(conf, time.Minute); err != nil {
			return "", err
		}

		// The password through the environment, which redis-cli reads, rather
		// than -a on a command line the console echoes.
		auth := []string{"REDISCLI_AUTH=" + password}

		if err := runner.MustEnv("redis-cli -h "+RedisHost+" -p "+fmt.Sprint(RedisPort)+
			" CONFIG SET maxmemory-policy noeviction", time.Minute, auth); err != nil {
			return "", err
		}

		if err := runner.MustEnv("redis-cli -h "+RedisHost+" -p "+fmt.Sprint(RedisPort)+" PING",
			time.Minute, auth); err != nil {
			return "", errors.New("Redis did not answer: " + err.Error())
		}

		return "Redis is answering, and keeps app data instead of evicting it.", nil
	}
}

// provisionAppDatabase creates the site's database and user in the engine.
// Safe to repeat: provisioning a database that exists leaves it as it is.
func (s *Service) provisionAppDatabase(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if site.DatabaseID <= 0 {
			return "No database connected.", nil
		}

		if err := s.Databases.Provision(ctx, site.DatabaseID, runner); err != nil {
			return "", err
		}

		return "The app's database is ready.", nil
	}
}

// connectionSteps are the steps that put a site's connections in place.
func (s *Service) connectionSteps(list *tasks.List, site *store.Site) {
	if site.DatabaseID > 0 {
		list.AddFn("Prepare the app's database", s.provisionAppDatabase(context.Background(), site.ID))
	}

	if site.RedisDB > 0 {
		list.AddFn("Prepare Redis", s.prepareRedis(context.Background(), site.ID))
	}
}

// ApplyConnections puts a running site's connections in place and restarts it
// with them: the job behind Connect on the Environment tab.
func (s *Service) ApplyConnections(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.connect", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	list := &tasks.List{}

	s.connectionSteps(list, site)

	list.AddFn("Write the environment", s.refreshServiceEnv(ctx, site.ID))

	service := site.ServiceName()

	if proc.Exists(UnitPath(site), true) {
		list.Add("Restart the service",
			fmt.Sprintf("sudo systemctl restart %s; sleep 2; sudo systemctl is-active %s", service, service))
	}

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the connection was not completed")
	}

	return nil
}
