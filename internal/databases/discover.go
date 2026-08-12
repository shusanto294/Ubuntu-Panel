package databases

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
)

// systemDatabases are the ones every engine keeps for itself. Listing them
// would be listing the machinery rather than the operator's data, and offering
// to drop `mysql` or `admin` is offering to break the server.
var systemDatabases = map[string]map[string]bool{
	"mysql": {
		"information_schema": true,
		"performance_schema": true,
		"mysql":              true,
		"sys":                true,
	},
	"postgres": {
		"postgres":  true,
		"template0": true,
		"template1": true,
	},
	"mongodb": {
		"admin":  true,
		"local":  true,
		"config": true,
	},
}

// discovered caches the answer for a few seconds.
//
// The Databases page reloads itself every few seconds while anything is
// running, and each reload asks every installed engine. `SHOW DATABASES` is
// cheap; starting mongosh is the better part of a second, three times over on
// a page that is only redrawing a task's progress.
var discovered = struct {
	sync.Mutex
	entries map[string]discoveredNames
}{entries: map[string]discoveredNames{}}

type discoveredNames struct {
	names []string
	asked time.Time
}

// ForgetDiscovered drops the cache, for when the panel has just created or
// dropped a database itself and the next page load should not still show the
// old answer.
func ForgetDiscovered() {
	discovered.Lock()
	discovered.entries = map[string]discoveredNames{}
	discovered.Unlock()
}

// Discover asks an engine which databases it actually holds.
//
// The panel's own table only knows what the panel created. A database made in
// phpMyAdmin, or by a MongoDB client like Compass connecting with the
// administrator string the panel hands out, exists on the server and nowhere
// in the panel — so the Databases page said there were none on a server with
// several, which reads as the panel having lost them.
//
// Best effort by design: an engine that is installed but not running, or a
// client that is not on PATH, returns nothing rather than an error. This
// decorates a page; it is not allowed to be the reason the page fails.
func (s *Service) Discover(engine string) []string {
	discovered.Lock()

	if entry, ok := discovered.entries[engine]; ok && time.Since(entry.asked) < 10*time.Second {
		discovered.Unlock()

		return entry.names
	}

	discovered.Unlock()

	names := s.discover(engine)

	discovered.Lock()
	discovered.entries[engine] = discoveredNames{names: names, asked: time.Now()}
	discovered.Unlock()

	return names
}

func (s *Service) discover(engine string) []string {
	var result proc.Result

	switch engine {
	case "mysql":
		// Batch and skip-column-names, so the output is one name per line with
		// no table drawing around it.
		result = proc.Run("sudo mysql -N -B -e 'SHOW DATABASES' 2>/dev/null", 30*time.Second)

	case "postgres":
		result = proc.Run("sudo -u postgres psql -Atqc "+
			proc.Quote("SELECT datname FROM pg_database WHERE NOT datistemplate")+
			" 2>/dev/null", 30*time.Second)

	case "mongodb":
		// listDatabases through the admin database, authenticated by the same
		// prelude every other MongoDB script here uses.
		script := mongoPrelude(s.Store) +
			"const result = db.getSiblingDB(\"admin\").adminCommand({listDatabases: 1, nameOnly: true});\n" +
			"for (const entry of result.databases || []) { print(entry.name); }\n"

		result = mongoRun(script, 30*time.Second)

	default:
		return nil
	}

	if !result.OK() {
		return nil
	}

	system := systemDatabases[engine]

	names := []string{}

	for _, line := range strings.Split(result.Output, "\n") {
		name := strings.TrimSpace(line)

		// mongosh prints the odd notice on stdout — a deprecation, a telemetry
		// line — and neither is a database name.
		if name == "" || system[name] || strings.ContainsAny(name, " \t") {
			continue
		}

		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
