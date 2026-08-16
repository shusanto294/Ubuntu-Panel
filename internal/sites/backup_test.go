package sites

import (
	"strings"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A schedule is an interval from now, and "off" is not a schedule.
//
// Zero is what DueBackups reads as "never ask about this site", so a spelling
// the panel does not know has to come back as zero rather than as some default
// interval — a typo should leave a site alone, not put it on a daily copy
// nobody asked for.
func TestOnlyAKnownScheduleGivesASiteADueTime(t *testing.T) {
	from := time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)

	for schedule, want := range map[string]time.Duration{
		"daily":   24 * time.Hour,
		"weekly":  7 * 24 * time.Hour,
		"monthly": 30 * 24 * time.Hour,
	} {
		if got := NextBackupAt(schedule, from); got != from.Add(want).Unix() {
			t.Errorf("%s was due at %d, wanted %d", schedule, got, from.Add(want).Unix())
		}
	}

	for _, schedule := range []string{"", "off", "hourly", "Daily", "every day"} {
		if got := NextBackupAt(schedule, from); got != 0 {
			t.Errorf("%q gave a due time of %d, wanted none", schedule, got)
		}
	}
}

// Every database name reaches a command line, so the only safe form is the
// allow-list — the same one the databases package validates with.
//
// This is the backstop rather than the guard: the names being checked are ones
// the panel generated itself. It exists because the two commands it protects
// are a dump and a DROP DATABASE, and neither is a good place to find out that
// somebody edited a row by hand.
func TestOnlyAPlainNameIsDumpedOrRestoredInto(t *testing.T) {
	for _, name := range []string{"wp_a1b2", "site_db", "A_1"} {
		if !databaseName(name) {
			t.Errorf("%q is a perfectly ordinary database name and was refused", name)
		}
	}

	for _, name := range []string{
		"", "has space", "quote'name", "back`tick", "semi;colon", "dash-name",
		"drop`; DROP DATABASE x; --", strings.Repeat("a", 65),
	} {
		if databaseName(name) {
			t.Errorf("%q was accepted as a database name", name)
		}
	}
}

// Where each engine's dump lives inside a backup, so the restore looks for the
// file the backup actually wrote.
func TestEachEngineIsRestoredFromTheFileItDumpedInto(t *testing.T) {
	for engine, want := range map[string]string{
		"mysql":    "database.sql.gz",
		"postgres": "database.sql.gz",
		"mongodb":  "database.archive.gz",
		"":         "database.sql.gz",
	} {
		if got := dumpFile(engine); got != want {
			t.Errorf("a %q dump was looked for at %q, wanted %q", engine, got, want)
		}
	}
}

// A backup goes under /var/backups, never inside the site it is a copy of.
//
// A backup in the site's own directory is a copy of itself — included in the
// next one, doubling every night — and it is served to the internet by the
// vhost sitting in front of it, which makes every copy of the site's
// wp-config.php a public download.
func TestABackupIsKeptOutsideTheSiteItCopies(t *testing.T) {
	site := &store.Site{ID: 4, Domain: "example.com", RootPath: "/var/www/example.com"}

	directory := backupDir(site, 12, time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC))

	if !strings.HasPrefix(directory, BackupRoot+"/example.com/") {
		t.Errorf("a backup was put at %q, outside the panel's backup root", directory)
	}

	if strings.HasPrefix(directory, site.RootPath) {
		t.Errorf("a backup was put inside the site it copies: %q", directory)
	}

	// The id is in the name as well as the timestamp: two backups a second
	// apart would otherwise land in the same directory and overwrite each
	// other's parts.
	if !strings.HasSuffix(directory, "-12") {
		t.Errorf("a backup's directory does not name it: %q", directory)
	}

	if second := backupDir(site, 13, time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)); second == directory {
		t.Errorf("two backups taken in the same second share a directory: %q", directory)
	}
}

// RemoveBackup only ever deletes inside the backup root.
//
// The path on the row is what `rm -rf` is pointed at, so a row whose path was
// empty or edited would be the panel recursively deleting whatever that path
// happens to name. The row still goes: a backup whose files are gone is not a
// backup, and keeping it is offering a restore that cannot work.
func TestRemovingABackupWillNotDeleteOutsideTheBackupRoot(t *testing.T) {
	for _, path := range []string{
		// The one that actually happens: a backup that failed before it wrote
		// anything has an empty path, and `rm -rf ''` is not the thing to run.
		"",
		"/", "/etc", "/var/www/example.com", "/var/backups", BackupRoot,
		BackupRoot + "/", BackupRoot + "/../../etc",
	} {
		if insideBackupRoot(path) {
			t.Errorf("the panel would recursively delete %q", path)
		}
	}

	for _, path := range []string{
		BackupRoot + "/example.com/20260816-030000-12",
		BackupRoot + "/a.example.com/20260101-000000-1",
	} {
		if !insideBackupRoot(path) {
			t.Errorf("a real backup at %q would not be deleted with its row", path)
		}
	}
}
