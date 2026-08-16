package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Backing a site up, and putting it back.
//
// A backup is a directory rather than one file, and the directory holds three
// things at most: `files.tar.gz`, `database.sql.gz` and `manifest.json`. Three
// parts rather than one archive because the alternative is a tar of a tar —
// every byte of a large site copied twice on the way in and twice on the way
// out — and because a person who wants only the database should be able to take
// it without unpacking forty gigabytes of uploads first.
//
// Nothing here is specific to WordPress or Laravel or anything else. A site is
// a directory and, if the panel made it one, a database; those two are what is
// copied and those two are what is put back. That is why it works for every
// type on the Sites page rather than for the two the panel knows the most
// about.

// BackupRoot is where backups live.
//
// Under /var/backups because that is the directory Debian and Ubuntu set aside
// for exactly this, and outside every site's own root on purpose: a backup
// inside the directory being backed up is a copy of itself, growing by a copy
// every time, and it would be served to the internet by the vhost in front of
// it.
const BackupRoot = "/var/backups/ubuntu-panel/sites"

// BackupSchedules are the intervals a site can be put on, and how long each
// one is.
//
// A menu rather than a cron expression. Five fields separated by spaces is a
// language, and somebody who wants a copy of their site every night should not
// have to learn it to get one — the same reason the Cron tab offers a list of
// schedules with the boxes behind Custom.
var BackupSchedules = map[string]time.Duration{
	"daily":   24 * time.Hour,
	"weekly":  7 * 24 * time.Hour,
	"monthly": 30 * 24 * time.Hour,
}

// NextBackupAt is when a site on this schedule should next be backed up.
//
// Zero for a site that is not on one, which is what DueBackups reads as "never
// ask about this site".
func NextBackupAt(schedule string, from time.Time) int64 {
	every, ok := BackupSchedules[schedule]
	if !ok {
		return 0
	}

	return from.Add(every).Unix()
}

// backupDir is where one backup's parts are written.
//
// The id is in the name as well as the timestamp: two backups a second apart
// would otherwise collide, and the id is the thing the panel's own row is found
// by when somebody is reading a directory listing to work out what is safe to
// delete.
func backupDir(site *store.Site, backupID int64, at time.Time) string {
	return fmt.Sprintf("%s/%s/%s-%d", BackupRoot, site.Domain,
		at.UTC().Format("20060102-150405"), backupID)
}

// Backup copies a site's files and its database into a directory of its own.
//
// The two halves are independent: a static site has no database and a site
// whose files could not be read still has a usable dump. Either failing fails
// the backup, though — a copy that is missing half the site is worse than no
// copy at all, because it is the one somebody restores from at three in the
// morning without checking.
func (s *Service) Backup(ctx context.Context, backupID int64) error {
	backup, err := s.Store.FindBackup(ctx, backupID)
	if err != nil {
		return errors.New("that backup no longer exists")
	}

	site, err := s.Store.FindSite(ctx, backup.SiteID)
	if err != nil {
		s.Store.FinishBackup(backupID, "failed", "The site no longer exists.", 0)

		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.backup", site.UserID, site.ID,
		site.Domain)
	if err != nil {
		return err
	}

	directory := backupDir(site, backupID, time.Now())

	s.Store.StartBackup(backupID, directory)

	database, engine, name := s.backupDatabase(ctx, site)

	list := &tasks.List{}

	list.Add("Make room for the backup",
		fmt.Sprintf("sudo mkdir -p %s && sudo chmod 700 %s",
			proc.Quote(directory), proc.Quote(directory)))

	if database != nil {
		list.AddFn("Dump the "+name+" database", func(runner *tasks.Runner) (string, error) {
			return dumpDatabase(runner, engine, name, directory)
		})
	}

	if backup.HasFiles {
		// -p and --numeric-owner so the archive carries the ownership and the
		// modes back with it. A restore that puts every file back as root is a
		// site PHP-FPM cannot write to, which arrives in a browser as a white
		// page long after the restore said it worked.
		list.Add("Archive the site files",
			fmt.Sprintf("sudo tar -czpf %s --numeric-owner --warning=no-file-changed "+
				"-C %s . || [ $? = 1 ]",
				proc.Quote(directory+"/files.tar.gz"), proc.Quote(site.RootPath)))
	}

	list.AddFn("Write the manifest", func(runner *tasks.Runner) (string, error) {
		return s.writeManifest(site, backup, directory, engine, name)
	})

	if !tasks.New(s.Store, taskID).Run(list) {
		message := "The backup did not finish."

		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		// The half-written directory goes with it. Leaving it behind is disk
		// nobody accounts for, holding a copy nothing will restore from.
		proc.Ok("sudo rm -rf " + proc.Quote(directory))

		s.Store.FinishBackup(backupID, "failed", message, 0)

		return errors.New(message)
	}

	s.Store.FinishBackup(backupID, "complete", "", directorySize(directory))

	s.pruneBackups(ctx, site)

	return nil
}

// backupDatabase is the database this site's backup should contain, if any.
//
// Only the one the panel made for the site. A site pointed by hand at somebody
// else's database is not one the panel is entitled to dump, and a restore that
// dropped it would be the panel wiping a database it never created.
func (s *Service) backupDatabase(ctx context.Context, site *store.Site) (*store.Database, string, string) {
	if site.DatabaseID <= 0 {
		return nil, "", ""
	}

	database, err := s.Store.FindDatabase(ctx, site.DatabaseID)
	if err != nil || !database.ManagedBySite {
		return nil, "", ""
	}

	return database, database.Engine, database.Name
}

// dumpDatabase writes the engine's own dump, gzipped, into the backup.
//
// Every one of them is run as the engine's superuser rather than as the site's
// database user, so nothing here needs a password on a command line where `ps`
// would show it — the same reason the rest of this package stages its SQL in a
// file.
//
// `set -o pipefail` matters more than it looks: without it a mysqldump that
// fails still ends in a successful gzip, and the step passes having written a
// perfectly valid archive of an error message.
func dumpDatabase(runner *tasks.Runner, engine, name, directory string) (string, error) {
	if !databaseName(name) {
		return "", fmt.Errorf("%q is not a database name the panel will dump", name)
	}

	var command, file string

	switch engine {
	case "postgres":
		file = directory + "/database.sql.gz"
		command = fmt.Sprintf("set -o pipefail; sudo -u postgres pg_dump --clean --if-exists "+
			"--no-owner --no-privileges %s | gzip -c | sudo tee %s > /dev/null",
			name, proc.Quote(file))

	case "mongodb":
		file = directory + "/database.archive.gz"
		command = fmt.Sprintf("sudo mongodump --quiet --db %s --archive=%s --gzip",
			name, proc.Quote(file))

	default:
		file = directory + "/database.sql.gz"
		// --single-transaction so a busy InnoDB site is dumped without locking
		// its tables against the visitors it is still serving.
		command = fmt.Sprintf("set -o pipefail; sudo mysqldump --single-transaction "+
			"--quick --routines --triggers --events %s | gzip -c | sudo tee %s > /dev/null",
			name, proc.Quote(file))
	}

	if err := runner.Must(command, 30*time.Minute); err != nil {
		return "", err
	}

	if !proc.Ok("sudo test -s " + proc.Quote(file)) {
		return "", errors.New("the dump came out empty, so nothing was saved for the database")
	}

	return name + " dumped.", nil
}

// databaseName is the same allow-list the databases package validates with,
// repeated here because this is a name being put on a command line.
func databaseName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}

	return true
}

// manifest is what the backup says about itself.
//
// Written for the person reading a directory on a machine whose panel is gone,
// not for the panel: the panel has a row. Somebody who finds
// /var/backups/ubuntu-panel/sites/example.com/20260816-030000-12 in a year
// should be able to tell from the directory alone which site it is, what is in
// it, and which database the dump belongs in.
type manifest struct {
	Panel        string `json:"panel"`
	Version      string `json:"version"`
	Site         string `json:"site"`
	SiteType     string `json:"site_type"`
	RootPath     string `json:"root_path"`
	WebDirectory string `json:"web_directory"`
	PHPVersion   string `json:"php_version,omitempty"`
	Engine       string `json:"database_engine,omitempty"`
	Database     string `json:"database_name,omitempty"`
	Files        string `json:"files,omitempty"`
	Dump         string `json:"database_dump,omitempty"`
	Kind         string `json:"kind"`
	TakenAt      string `json:"taken_at"`
}

func (s *Service) writeManifest(site *store.Site, backup *store.Backup,
	directory, engine, name string) (string, error) {
	entry := manifest{
		Panel:        config.Name,
		Version:      config.Version,
		Site:         site.Domain,
		SiteType:     site.Type,
		RootPath:     site.RootPath,
		WebDirectory: site.WebDirectory,
		PHPVersion:   site.PHPVersion,
		Engine:       engine,
		Database:     name,
		Kind:         backup.Kind,
		TakenAt:      time.Now().UTC().Format(time.RFC3339),
	}

	if backup.HasFiles {
		entry.Files = "files.tar.gz"
	}

	if name != "" {
		entry.Dump = dumpFile(engine)
	}

	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", err
	}

	if err := proc.PutFile(directory+"/manifest.json", string(encoded)+"\n", true); err != nil {
		return "", err
	}

	return "Manifest written.", nil
}

// dumpFile is what the engine's dump is called inside a backup.
func dumpFile(engine string) string {
	if engine == "mongodb" {
		return "database.archive.gz"
	}

	return "database.sql.gz"
}

// directorySize is what the backup takes up, in bytes.
//
// Best effort: `du` failing is not a reason to fail a backup that worked, and a
// size of zero on the page reads as "not measured" rather than as a claim the
// copy is empty.
func directorySize(directory string) int64 {
	result := proc.Run("sudo du -sb "+proc.Quote(directory)+" 2>/dev/null | cut -f1", time.Minute)
	if !result.OK() {
		return 0
	}

	size, err := strconv.ParseInt(strings.TrimSpace(result.Output), 10, 64)
	if err != nil {
		return 0
	}

	return size
}

// pruneBackups deletes the scheduled backups beyond what the site is set to
// keep, oldest first.
//
// Scheduled ones only, and never the manual ones. Somebody who took a copy
// before upgrading a plugin means to keep it until they say otherwise; a
// nightly run filling the quota is not them saying otherwise.
func (s *Service) pruneBackups(ctx context.Context, site *store.Site) {
	if site.BackupKeep < 1 {
		return
	}

	old, err := s.Store.PrunableBackups(ctx, site.ID, site.BackupKeep)
	if err != nil {
		return
	}

	for _, backup := range old {
		s.RemoveBackup(backup)
	}
}

// RemoveBackup deletes one backup's directory and its row.
//
// The row goes even when the directory will not: a backup whose files are gone
// is not a backup, and leaving the row is offering somebody a restore that
// cannot work.
func (s *Service) RemoveBackup(backup *store.Backup) {
	if insideBackupRoot(backup.Path) {
		proc.Ok("sudo rm -rf " + proc.Quote(backup.Path))
	}

	s.Store.DeleteBackup(backup.ID)
}

// insideBackupRoot is what `rm -rf` is allowed to be pointed at.
//
// The path on the row is taken from the database and handed to a recursive
// delete, so a row whose path is empty — a backup that failed before it wrote
// anything — or edited by hand would otherwise be the panel deleting whatever
// that path names. `..` is refused rather than resolved: a path that has to be
// reasoned about is one to say no to.
func insideBackupRoot(path string) bool {
	return strings.HasPrefix(path, BackupRoot+"/") &&
		len(strings.TrimPrefix(path, BackupRoot+"/")) > 0 &&
		!strings.Contains(path, "..")
}

// ---------------------------------------------------------------- restoring

// Restore puts a backup back: the files, the database, or both.
//
// Everything the site has now is replaced. That is the whole point of it and it
// is what the confirmation says out loud — a restore that merged the two would
// leave a WordPress with the plugins of one version and the database of
// another, which is a site that half works in ways nobody can find.
//
// The files go back through a swap rather than in place. The archive is
// unpacked into a directory beside the site's root, and only when that has
// worked is the old root moved aside and the new one moved in. An extraction
// that dies halfway then leaves the site exactly as it was, instead of leaving
// it half restored and serving whichever of the two versions each file happens
// to be.
func (s *Service) Restore(ctx context.Context, backupID int64) error {
	backup, err := s.Store.FindBackup(ctx, backupID)
	if err != nil {
		return errors.New("that backup no longer exists")
	}

	if backup.Status != "complete" {
		return errors.New("that backup did not finish, so there is nothing to restore from")
	}

	site, err := s.Store.FindSite(ctx, backup.SiteID)
	if err != nil {
		return errors.New("the site this backup belongs to no longer exists")
	}

	if !proc.Ok("sudo test -d " + proc.Quote(backup.Path)) {
		return errors.New("this backup's files are no longer on disk")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.restore", site.UserID, site.ID,
		site.Domain)
	if err != nil {
		return err
	}

	s.Store.SetSiteStatus(site.ID, "deploying", "")

	list := &tasks.List{}

	// A Node or Python application holds its files open and would go on serving
	// the old ones out of memory. Stopped first, started last, and the site is
	// down for the length of the restore either way.
	service := ""

	if site.IsProxied() && proc.Ok("test -f "+proc.Quote(UnitPath(site))) {
		service = site.ServiceName()

		list.Add("Stop "+service, "sudo systemctl stop "+service+" || true").Optional()
	}

	if backup.HasFiles && proc.Ok("sudo test -f "+proc.Quote(backup.Path+"/files.tar.gz")) {
		list.AddFn("Replace the site files", s.restoreFiles(site, backup))
	}

	if backup.HasDatabase && backup.DatabaseName != "" {
		// The charset comes off the live row rather than out of the backup: it
		// is a property of the database the site is configured against, and the
		// dump is being loaded back into that same one.
		charset := "utf8mb4"

		if database, err := s.Store.FindDatabase(ctx, site.DatabaseID); err == nil && database.Charset != "" {
			charset = database.Charset
		}

		list.AddFn("Replace the "+backup.DatabaseName+" database", func(runner *tasks.Runner) (string, error) {
			return restoreDatabase(runner, backup, charset)
		})
	}

	list.Add("Fix ownership",
		fmt.Sprintf("sudo chown -R www-data:www-data %s", proc.Quote(site.RootPath))).Optional()

	// Laravel writes into these two and nothing else, and an archive taken
	// before the site was ever deployed can be missing the modes.
	if site.Type == "laravel" {
		list.Add("Let Laravel write to storage",
			fmt.Sprintf("sudo chmod -R 775 %s/storage %s/bootstrap/cache",
				proc.Quote(site.RootPath), proc.Quote(site.RootPath))).Optional()
	}

	if service != "" {
		list.Add("Start "+service, "sudo systemctl start "+service+
			"; sleep 2; sudo systemctl status "+service+" --no-pager --lines=20")
	}

	list.Add("Reload nginx", "sudo nginx -t && sudo systemctl reload nginx").Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		message := "The restore did not finish."

		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		s.Store.SetSiteStatus(site.ID, "failed", message)

		return errors.New(message)
	}

	s.Store.SetSiteStatus(site.ID, "active", "")

	return nil
}

// restoreFiles unpacks the archive beside the site's root and swaps it in.
func (s *Service) restoreFiles(site *store.Site, backup *store.Backup) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		staging := fmt.Sprintf("%s.restoring-%d", site.RootPath, backup.ID)
		replaced := fmt.Sprintf("%s.replaced-%d", site.RootPath, time.Now().Unix())

		// Unpacked first, and into a directory of its own. Nothing about the
		// live site has been touched at this point, so a corrupt archive costs
		// the disk space and nothing else.
		if err := runner.Must(fmt.Sprintf(
			"sudo rm -rf %[1]s && sudo mkdir -p %[1]s && "+
				"sudo tar -xzpf %[2]s --numeric-owner -C %[1]s",
			proc.Quote(staging), proc.Quote(backup.Path+"/files.tar.gz")),
			30*time.Minute); err != nil {
			proc.Ok("sudo rm -rf " + proc.Quote(staging))

			return "", err
		}

		// Two renames on the same filesystem, so the window in which the site's
		// root does not exist is as short as it can be made.
		if err := runner.Must(fmt.Sprintf(
			"sudo mv %s %s && sudo mv %s %s",
			proc.Quote(site.RootPath), proc.Quote(replaced),
			proc.Quote(staging), proc.Quote(site.RootPath)),
			5*time.Minute); err != nil {
			// The move back, in the order that leaves the site as it was.
			proc.Ok(fmt.Sprintf("sudo test -d %s && sudo rm -rf %s && sudo mv %s %s",
				proc.Quote(replaced), proc.Quote(site.RootPath),
				proc.Quote(replaced), proc.Quote(site.RootPath)))
			proc.Ok("sudo rm -rf " + proc.Quote(staging))

			return "", err
		}

		// Only now, with the new root in place and serving.
		proc.Ok("sudo rm -rf " + proc.Quote(replaced))

		return "The site's files are the ones from this backup. Everything that was there before " +
			"has been removed.", nil
	}
}

// restoreDatabase empties the database and loads the dump into it.
//
// Emptied rather than loaded over. A dump only replaces the tables it contains,
// so a table added since the backup would survive a plain load and the site
// would come back with a schema that is neither version — which is exactly the
// half-restored state the file swap above exists to avoid.
//
// On MariaDB and MySQL that means dropping the database and making it again.
// The grants that let the site log in are not stored inside it — they live in
// the server's own `mysql.db` table, keyed by user and database name — so they
// survive, and the username and password in the restored wp-config.php or .env
// still work. Dropping the whole thing rather than each table is also what
// takes the views, routines and triggers with it, which a list of DROP TABLEs
// quietly leaves behind.
func restoreDatabase(runner *tasks.Runner, backup *store.Backup, charset string) (string, error) {
	name := backup.DatabaseName

	if !databaseName(name) {
		return "", fmt.Errorf("%q is not a database name the panel will restore into", name)
	}

	if !databaseName(strings.ReplaceAll(charset, "-", "_")) {
		charset = "utf8mb4"
	}

	dump := backup.Path + "/" + dumpFile(backup.DatabaseEngine)

	if !proc.Ok("sudo test -s " + proc.Quote(dump)) {
		return "", errors.New("this backup has no database dump in it")
	}

	var command string

	switch backup.DatabaseEngine {
	case "postgres":
		// pg_dump --clean --if-exists already drops what it is about to
		// recreate, so the load is the whole operation.
		command = fmt.Sprintf("set -o pipefail; sudo gunzip -c %s | "+
			"sudo -u postgres psql -v ON_ERROR_STOP=1 -q -d %s",
			proc.Quote(dump), name)

	case "mongodb":
		command = fmt.Sprintf("sudo mongorestore --quiet --drop --gzip --archive=%s "+
			"--nsInclude %s", proc.Quote(dump), proc.Quote(name+".*"))

	default:
		command = fmt.Sprintf(
			"set -o pipefail; "+
				"sudo mysql -e 'DROP DATABASE IF EXISTS %[1]s; "+
				"CREATE DATABASE %[1]s CHARACTER SET %[3]s;' && "+
				"sudo gunzip -c %[2]s | sudo mysql %[1]s",
			name, proc.Quote(dump), charset)
	}

	if err := runner.Must(command, 30*time.Minute); err != nil {
		return "", err
	}

	return name + " is the database from this backup. What was in it has been replaced.", nil
}
