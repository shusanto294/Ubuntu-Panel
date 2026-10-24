package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Staging: a second copy of a site, on its own hostname, that can be pushed
// back over the first.
//
// The copy is a site row like any other, with `staging_of` pointing at the one
// it came from. That is deliberate and it is most of the design: a staging site
// gets the vhost, the certificate, the PHP version picker, the file manager,
// the backups and the terminal without a line of code, because it is a site.
// What this file adds is the two operations that keep the pair in step, and the
// care each of them needs — which is nearly all in repoint.go, because an
// application that has been copied does not know it has been.

// StagingSuffix is what a suggested staging hostname starts with.
const StagingSuffix = "staging"

// SuggestStagingDomain is the hostname the form is pre-filled with.
//
// A subdomain of the site itself, because it is the one hostname the person
// definitely controls and definitely has DNS for — if the panel manages the
// production domain's records it can publish this one without being asked
// anything. `staging.www.example.com` would be a silly name, so the www comes
// off first.
func SuggestStagingDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "www.")

	if domain == "" {
		return ""
	}

	return StagingSuffix + "." + domain
}

// StagingOptions is what a clone or a push was asked to move.
type StagingOptions struct {
	// Files and Database are the two halves. A push with neither is refused by
	// the caller; a clone always does both, because a copy of a site missing
	// one of them is not a copy of the site.
	Files    bool
	Database bool

	// KeepUploads keeps the destination's own WordPress uploads instead of
	// replacing them with the source's.
	//
	// On a push to production this is what people mean nearly every time, and
	// getting it wrong is expensive in a way the rest of a push is not. Code
	// and settings are things you edited on staging on purpose. The media
	// library is not: it has whatever was uploaded to production since the
	// clone was taken, and replacing the directory wholesale deletes all of
	// it — files no backup of staging contains, because they were never there.
	KeepUploads bool
}

// ------------------------------------------------------------------ cloning

// CreateStaging builds the staging copy whose row already exists.
//
// The row first, in the request, so the page can be redirected to a site that
// is visibly being built — the same arrangement as creating any other site.
// This is the worker's half.
func (s *Service) CreateStaging(ctx context.Context, stagingID int64) error {
	staging, err := s.Store.FindSite(ctx, stagingID)
	if err != nil {
		return errors.New("that staging site no longer exists")
	}

	if !staging.IsStaging() {
		return errors.New("that site is not a staging copy of anything")
	}

	source, err := s.Store.FindSite(ctx, staging.StagingOf)
	if err != nil {
		s.Store.SetSiteStatus(staging.ID, "failed",
			"The site this was being cloned from no longer exists.")

		return errors.New("the site this is a staging copy of no longer exists")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.staging.create", staging.UserID,
		staging.ID, staging.Domain)
	if err != nil {
		return err
	}

	s.Store.SetSiteStatus(staging.ID, "deploying", "")

	list := &tasks.List{}

	list.Add("Create the site directory",
		fmt.Sprintf("sudo mkdir -p %s && sudo chown -R www-data:www-data %s",
			proc.Quote(staging.DocumentRoot()), proc.Quote(staging.RootPath)))

	// The hostname first, for the same reason an ordinary deployment does it
	// first: it takes minutes to spread and the certificate later needs it to
	// have spread already.
	if staging.ManageDNS {
		list.AddFn("Publish DNS records", s.publishDNS(ctx, staging.ID))
	}

	if staging.IsPHP() {
		list.AddFn("Make sure PHP "+staging.PHPVersion+" is on this machine",
			s.ensurePHPRuntime(ctx, staging.ID))
	}

	if staging.NeedsDatabase() {
		list.AddFn("Create the staging database", s.ensureDatabase(ctx, staging.ID))
	}

	list.AddFn("Copy the files from "+source.Domain,
		s.copyFiles(ctx, source.ID, staging.ID, StagingOptions{Files: true}))

	if staging.NeedsDatabase() {
		list.AddFn("Copy the database from "+source.Domain,
			s.copyDatabase(ctx, source.ID, staging.ID))
	}

	// Before anything can serve a request. Until this runs, the copy's
	// configuration names the source's database — and the vhost written below
	// would put a live hostname in front of production's data.
	list.AddFn("Point the copy at its own database",
		s.repointConfigStep(ctx, source.ID, staging.ID, true))

	if staging.IsProxied() {
		// The hostname in the application's own environment, before the
		// application is started on it. Only the hostname — a proxied app's
		// .env is rewritten host-only, never scheme-and-host — so the
		// certificate step further down cannot invalidate what this writes.
		list.AddFn("Point the application at "+staging.Domain,
			s.repointURLsStep(ctx, source.ID, staging.ID, true))

		list.AddFn("Create the systemd service", s.createSystemdUnit(ctx, staging.ID))
		list.AddFn("Start the application", s.startAndWait(ctx, staging.ID))
	}

	list.AddFn("Write the nginx vhost", s.writeVhost(ctx, staging.ID))
	list.Add("Reload nginx", "sudo nginx -t", "sudo systemctl reload nginx")

	if staging.SSL {
		if edge := s.edgeTLS(ctx, staging); edge.Terminates && !edge.OriginNeedsCertificate {
			list.AddFn("Let the DNS provider serve TLS", s.useEdgeTLS(ctx, staging.ID, edge.Mode))
		} else {
			list.AddFn("Request the TLS certificate", s.requestCertificate(ctx, staging.ID)).Optional()
			list.AddFn("Serve the site over HTTPS", s.enableHTTPS(ctx, staging.ID)).Optional()
		}
	}

	// And only now the addresses in the database.
	//
	// After the certificate on purpose. What goes into the database is a scheme
	// as well as a hostname, and until the step above has run nobody knows
	// which scheme this site serves: a certificate that failed to issue leaves
	// it on HTTP, and a database full of https:// URLs would then be a page
	// whose every asset is an address nothing answers.
	if !staging.IsProxied() {
		list.AddFn("Rewrite the addresses in the database",
			s.repointURLsStep(ctx, source.ID, staging.ID, true))
	}

	list.AddFn("Check the site answers", s.checkThroughNginx(ctx, staging.ID))
	list.AddFn("Check the domain points here", s.checkDomainResolves(ctx, staging.ID)).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		message := "The staging site could not be built."

		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		s.Store.SetSiteStatus(staging.ID, "failed", message)

		return errors.New(message)
	}

	s.Store.SetSiteStatus(staging.ID, "active", "")

	return nil
}

// ------------------------------------------------------------------ pushing

// Push copies a staging site back over the site it came from.
//
// Production is backed up first, every time, and the push does not start if the
// backup did not finish. That is not a precaution that can be switched off:
// this operation replaces a live site's files and database with a copy of
// somebody's test environment, and the one thing that must be true afterwards
// is that the previous state still exists somewhere.
func (s *Service) Push(ctx context.Context, stagingID int64, options StagingOptions) error {
	if !options.Files && !options.Database {
		return errors.New("a push has to move the files, the database, or both")
	}

	staging, err := s.Store.FindSite(ctx, stagingID)
	if err != nil {
		return errors.New("that staging site no longer exists")
	}

	if !staging.IsStaging() {
		return errors.New("that site is not a staging copy, so there is nothing to push to")
	}

	target, err := s.Store.FindSite(ctx, staging.StagingOf)
	if err != nil {
		return errors.New("the site this was a staging copy of no longer exists")
	}

	if target.Status == "deleting" {
		return errors.New("the production site is being removed")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.staging.push", staging.UserID,
		target.ID, target.Domain)
	if err != nil {
		return err
	}

	runner := tasks.New(s.Store, taskID)

	s.Store.SetSiteStatus(target.ID, "deploying", "")

	list := &tasks.List{}

	// The backup, as the first step and as a step that may not be skipped.
	list.AddFn("Back "+target.Domain+" up first", s.backupBeforePush(ctx, target.ID))

	// Whatever production currently asks search engines to do, before the
	// staging database overwrites it. A staging copy has blog_public off, and
	// loading it into production without putting this back is how a push takes
	// a live site out of Google.
	indexing := ""

	if options.Database && target.Type == "wordpress" {
		list.AddFn("Note how "+target.Domain+" is indexed", func(runner *tasks.Runner) (string, error) {
			indexing = readWordPressOption(target, "blog_public")

			if indexing == "" {
				return "Could not read the current setting; it will be set to indexed " +
					"after the push, which is the safe direction for a live site.", nil
			}

			return "blog_public is " + indexing + " and will be put back after the push.", nil
		}).Optional()
	}

	service := ""

	if target.IsProxied() && proc.Ok("test -f "+proc.Quote(UnitPath(target))) {
		service = target.ServiceName()

		list.Add("Stop "+service, "sudo systemctl stop "+service+" || true").Optional()
	}

	if options.Files {
		list.AddFn("Copy the files to "+target.Domain,
			s.copyFiles(ctx, staging.ID, target.ID, options))
	}

	if options.Database && target.NeedsDatabase() {
		list.AddFn("Copy the database to "+target.Domain,
			s.copyDatabase(ctx, staging.ID, target.ID))
	}

	// The configuration first: a files push has just put the staging site's
	// wp-config.php or .env onto production, naming the staging database.
	list.AddFn("Point "+target.Domain+" back at its own database",
		s.repointConfigStep(ctx, staging.ID, target.ID, false))

	// Then the addresses. Production's scheme is already settled — it has been
	// serving — so one pass is right.
	list.AddFn("Rewrite the addresses in the database",
		s.repointURLsStep(ctx, staging.ID, target.ID, false))

	if options.Database && target.Type == "wordpress" {
		list.AddFn("Put the indexing setting back", func(runner *tasks.Runner) (string, error) {
			// Indexed unless production itself said otherwise. Being wrong
			// this way costs a live site nothing; being wrong the other way
			// deindexes it, and nobody notices for weeks.
			wanted := "1"
			if indexing == "0" {
				wanted = "0"
			}

			root := proc.Quote(target.DocumentRoot())

			runner.Exec(fmt.Sprintf(
				"sudo -u www-data wp --path=%s --skip-plugins --skip-themes "+
					"option update blog_public %s --raw", root, wanted), 2*time.Minute)

			if wanted == "1" {
				return "Search engines may index " + target.Domain + " again.", nil
			}

			return target.Domain + " was already asking search engines to stay away, " +
				"and still is.", nil
		}).Optional()
	}

	if service != "" {
		list.Add("Start "+service, "sudo systemctl start "+service+
			"; sleep 2; sudo systemctl status "+service+" --no-pager --lines=20")
	}

	list.Add("Reload nginx", "sudo nginx -t && sudo systemctl reload nginx").Optional()

	list.AddFn("Check "+target.Domain+" answers", s.checkThroughNginx(ctx, target.ID))

	if !runner.Run(list) {
		message := "The push did not finish."

		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		// Failed, and production is in whatever state the step that failed
		// left it. The backup from the first step is the way back, and the
		// message says so rather than leaving somebody to work it out.
		s.Store.SetSiteStatus(target.ID, "failed", message+
			" The backup taken before the push is on the Backups tab.")

		return errors.New(message)
	}

	s.Store.SetSiteStatus(target.ID, "active", "")

	return nil
}

// backupBeforePush takes the copy the push can be undone from.
func (s *Service) backupBeforePush(ctx context.Context, targetID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		target, err := s.Store.FindSite(ctx, targetID)
		if err != nil {
			return "", errors.New("the production site no longer exists")
		}

		backup := &store.Backup{
			SiteID:   target.ID,
			UserID:   target.UserID,
			Kind:     "manual",
			Note:     "Taken automatically before a push from staging",
			HasFiles: true,
		}

		if target.DatabaseID > 0 {
			if database, err := s.Store.FindDatabase(ctx, target.DatabaseID); err == nil &&
				database.ManagedBySite {
				backup.HasDatabase = true
				backup.DatabaseEngine = database.Engine
				backup.DatabaseName = database.Name
			}
		}

		backupID, err := s.Store.CreateBackup(ctx, backup)
		if err != nil || backupID <= 0 {
			return "", errors.New("the panel could not record a backup, and a push " +
				"does not start without one")
		}

		// Run here rather than queued: the push must not begin until the copy
		// exists, and a job pushed onto the queue from inside a queue worker
		// would run alongside it.
		if err := s.Backup(ctx, backupID); err != nil {
			return "", fmt.Errorf("backing up %s failed, so the push was not started: %w",
				target.Domain, err)
		}

		return fmt.Sprintf("%s was backed up before anything was replaced. It is on the "+
			"site's Backups tab, and restoring it undoes this push.", target.Domain), nil
	}
}

// readWordPressOption reads one option, or empty when it cannot be read.
func readWordPressOption(site *store.Site, option string) string {
	result := proc.Run(fmt.Sprintf(
		"sudo -u www-data wp --path=%s --skip-plugins --skip-themes option get %s 2>/dev/null",
		proc.Quote(site.DocumentRoot()), proc.Quote(option)), 2*time.Minute)

	if !result.OK() {
		return ""
	}

	return strings.TrimSpace(result.Output)
}

// -------------------------------------------------------------- the copying

// copyFiles replaces one site's root with a copy of another's.
//
// Built beside the destination and swapped in, the same way a restore is: a
// copy that dies halfway through leaves the destination exactly as it was,
// rather than serving a directory that is half one site and half the other.
// On a push that matters more than anywhere else, because the destination is
// production.
func (s *Service) copyFiles(ctx context.Context, fromID, toID int64,
	options StagingOptions) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		from, err := s.Store.FindSite(ctx, fromID)
		if err != nil {
			return "", errors.New("the site being copied from no longer exists")
		}

		to, err := s.Store.FindSite(ctx, toID)
		if err != nil {
			return "", errors.New("the site being copied to no longer exists")
		}

		if !proc.Ok("sudo test -d " + proc.Quote(from.RootPath)) {
			return "", fmt.Errorf("%s has no files on disk at %s", from.Domain, from.RootPath)
		}

		staged := fmt.Sprintf("%s.copying-%d", to.RootPath, time.Now().Unix())
		replaced := fmt.Sprintf("%s.replaced-%d", to.RootPath, time.Now().Unix())

		// cp -a rather than rsync: rsync is not on a minimal Ubuntu, and
		// nothing here needs the delta — the destination is being replaced
		// whole. -a keeps the ownership, the modes and the symlinks, which is
		// the difference between a copy PHP-FPM can write to and a white page.
		if err := runner.Must(fmt.Sprintf(
			"sudo rm -rf %[1]s && sudo mkdir -p %[1]s && sudo cp -a %[2]s/. %[1]s/",
			proc.Quote(staged), proc.Quote(from.RootPath)), 60*time.Minute); err != nil {
			proc.Ok("sudo rm -rf " + proc.Quote(staged))

			return "", err
		}

		kept := ""

		// The destination's own media, moved into the copy before the swap.
		if options.KeepUploads && to.Type == "wordpress" {
			kept = s.keepUploads(runner, to, staged)
		}

		// Two renames on one filesystem, so the window where the destination's
		// root does not exist is as short as it can be.
		if err := runner.Must(fmt.Sprintf("sudo mv %s %s && sudo mv %s %s",
			proc.Quote(to.RootPath), proc.Quote(replaced),
			proc.Quote(staged), proc.Quote(to.RootPath)), 10*time.Minute); err != nil {
			proc.Ok(fmt.Sprintf("sudo test -d %s && sudo rm -rf %s && sudo mv %s %s",
				proc.Quote(replaced), proc.Quote(to.RootPath),
				proc.Quote(replaced), proc.Quote(to.RootPath)))
			proc.Ok("sudo rm -rf " + proc.Quote(staged))

			return "", err
		}

		proc.Ok("sudo rm -rf " + proc.Quote(replaced))

		proc.Ok(fmt.Sprintf("sudo chown -R www-data:www-data %s", proc.Quote(to.RootPath)))

		if to.Type == "laravel" {
			proc.Ok(fmt.Sprintf("sudo chmod -R 775 %s/storage %s/bootstrap/cache",
				proc.Quote(to.RootPath), proc.Quote(to.RootPath)))
		}

		note := fmt.Sprintf("%s now holds a copy of %s's files. Everything that was "+
			"there before has been removed.", to.Domain, from.Domain)

		if kept != "" {
			note += " " + kept
		}

		return note, nil
	}
}

// keepUploads moves the destination's wp-content/uploads into the staged copy,
// in place of the source's.
//
// Moved rather than merged. Merging two media libraries by filename produces a
// directory where some files are the source's and some the destination's, which
// is a library that matches neither database — and the destination's database
// is the one about to be in charge, unless the push is replacing that too.
func (s *Service) keepUploads(runner *tasks.Runner, to *store.Site, staged string) string {
	live := strings.TrimRight(to.DocumentRoot(), "/") + "/wp-content/uploads"

	// The staged copy's document root is at the same offset inside it as the
	// destination's is inside the destination.
	offset := strings.TrimPrefix(to.DocumentRoot(), to.RootPath)
	stagedUploads := strings.TrimRight(staged+offset, "/") + "/wp-content/uploads"

	if !proc.Ok("sudo test -d " + proc.Quote(live)) {
		return ""
	}

	ok := proc.Ok(fmt.Sprintf("sudo rm -rf %[1]s && sudo mkdir -p %[2]s && sudo cp -a %[3]s %[1]s",
		proc.Quote(stagedUploads),
		proc.Quote(strings.TrimSuffix(stagedUploads, "/uploads")),
		proc.Quote(live)))

	if !ok {
		runner.Write("Could not keep the existing uploads directory; the copy's own " +
			"uploads were used instead.\n")

		return ""
	}

	return "The media library already on " + to.Domain + " was kept rather than replaced."
}

// copyDatabase loads one site's database into another's.
//
// Through a dump on disk rather than a pipe between two mysql processes. A pipe
// is one command shorter and gives up the one thing worth having: with the dump
// written first, a source that fails to dump has not touched the destination,
// and the destination is only dropped once there is something to put in it.
func (s *Service) copyDatabase(ctx context.Context, fromID, toID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		from, err := s.Store.FindSite(ctx, fromID)
		if err != nil {
			return "", errors.New("the site being copied from no longer exists")
		}

		to, err := s.Store.FindSite(ctx, toID)
		if err != nil {
			return "", errors.New("the site being copied to no longer exists")
		}

		source, sourceEngine, sourceName := s.backupDatabase(ctx, from)
		if source == nil {
			return "", fmt.Errorf("%s has no database the panel created, so there is "+
				"nothing to copy", from.Domain)
		}

		destination, destinationEngine, destinationName := s.backupDatabase(ctx, to)
		if destination == nil {
			return "", fmt.Errorf("%s has no database the panel created, so there is "+
				"nowhere to copy to", to.Domain)
		}

		if sourceEngine != destinationEngine {
			return "", fmt.Errorf("%s is on %s and %s is on %s; the panel will not copy "+
				"between engines", from.Domain, sourceEngine, to.Domain, destinationEngine)
		}

		// Its own directory under the backup root, so it is covered by the
		// same `rm -rf` guard as everything else there and is nowhere a vhost
		// could serve it from.
		directory := fmt.Sprintf("%s/.transfers/%s-%d", BackupRoot, to.Domain, time.Now().UnixNano())

		if err := runner.Must(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chmod 700 %[1]s",
			proc.Quote(directory)), 2*time.Minute); err != nil {
			return "", err
		}

		defer proc.Ok("sudo rm -rf " + proc.Quote(directory))

		if _, err := dumpDatabase(runner, sourceEngine, sourceName, directory); err != nil {
			return "", fmt.Errorf("dumping %s failed, so %s was left alone: %w",
				sourceName, destinationName, err)
		}

		charset := destination.Charset
		if charset == "" {
			charset = "utf8mb4"
		}

		// The same loader a restore uses, pointed at the destination: the
		// database is dropped and made again rather than loaded over, so a
		// table the source does not have cannot survive into a schema that is
		// then neither site's.
		transfer := &store.Backup{
			Path:           directory,
			DatabaseEngine: destinationEngine,
			DatabaseName:   destinationName,
		}

		if _, err := restoreDatabase(runner, transfer, charset); err != nil {
			return "", err
		}

		return fmt.Sprintf("%s now holds a copy of %s. The URLs in it are rewritten in "+
			"the next step.", destinationName, sourceName), nil
	}
}

// repointConfigStep and repointURLsStep are the two halves of re-pointing, each
// as a step, because they belong at opposite ends of a copy. See the comment
// above repointConfig.
func (s *Service) repointConfigStep(ctx context.Context, fromID, toID int64,
	staging bool) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		args, err := s.repointArgsFor(ctx, fromID, toID, staging)
		if err != nil {
			return "", err
		}

		return s.repointConfig(runner, args)
	}
}

func (s *Service) repointURLsStep(ctx context.Context, fromID, toID int64,
	staging bool) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		args, err := s.repointArgsFor(ctx, fromID, toID, staging)
		if err != nil {
			return "", err
		}

		return s.repointURLs(runner, args)
	}
}

// repointArgsFor reads both sites back out of the store at the moment the step
// runs.
//
// Read now rather than captured when the list was built, and that is the whole
// reason this is a function: the destination's SSL column is written by the
// certificate steps, which run between the list being built and this being
// called. A site whose certificate did not issue has SSL false by now, and
// URL() therefore says http — which is the scheme that has to go into the
// database. Captured earlier it would still say https, and every asset on the
// site would be an address nothing answers.
func (s *Service) repointArgsFor(ctx context.Context, fromID, toID int64,
	staging bool) (repointArgs, error) {
	from, err := s.Store.FindSite(ctx, fromID)
	if err != nil {
		return repointArgs{}, errors.New("the site being copied from no longer exists")
	}

	to, err := s.Store.FindSite(ctx, toID)
	if err != nil {
		return repointArgs{}, errors.New("the site being copied to no longer exists")
	}

	var database *store.Database

	if to.DatabaseID > 0 {
		if found, err := s.Store.FindDatabase(ctx, to.DatabaseID); err == nil {
			database = found
		}
	}

	return repointArgs{
		Site:     to,
		FromURL:  from.URL(),
		ToURL:    to.URL(),
		Database: database,
		Staging:  staging,
	}, nil
}
