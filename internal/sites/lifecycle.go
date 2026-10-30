package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// recipe builds the deployment steps for a site, branching on its type.
func (s *Service) recipe(ctx context.Context, site *store.Site) *tasks.List {
	list := &tasks.List{}

	root := proc.Quote(site.RootPath)
	docroot := proc.Quote(site.DocumentRoot())

	list.Add("Create the site directory",
		fmt.Sprintf("sudo mkdir -p %s && sudo chown -R www-data:www-data %s && sudo chmod -R 755 %s",
			docroot, root, root))

	// The hostname first, before anything that can fail.
	//
	// It depends on nothing else, it takes minutes to spread, and the
	// certificate later needs it to have spread already. Published last, a
	// deployment that stopped anywhere earlier left a site with no name at all
	// — and on a site being rebuilt, the record its own deletion had removed —
	// so the operator got NXDOMAIN and no idea which of the two it was.
	if site.ManageDNS {
		list.AddFn("Publish DNS records", s.publishDNS(ctx, site.ID))
	}

	// Before anything is written: nginx will happily load a vhost pointing at
	// a socket that does not exist, `nginx -t` passes, the deployment reports
	// success, and every request to the site is a 502 from an origin where, as
	// far as the panel is concerned, nothing went wrong.
	if site.IsPHP() {
		list.AddFn("Make sure PHP "+site.PHPVersion+" is on this machine",
			s.ensurePHPRuntime(ctx, site.ID))
	}

	if site.NeedsDatabase() {
		list.AddFn("Create the application database", s.ensureDatabase(ctx, site.ID))
	}

	branch := site.Branch
	if branch == "" {
		branch = "main"
	}

	clone := func() {
		// Before the clone, not after it: a repository the panel cannot read
		// is the commonest way a deployment fails, and failing here costs
		// nothing — while failing at the clone itself is a site that already
		// has a vhost, a database and a certificate.
		list.AddFn("Check the repository can be read", s.checkRepository(ctx, site.ID))

		list.Add("Clone the repository", CloneCommand(site, root, branch))

		list.AddFn("Record the commit", s.recordCommit(ctx, site.ID))
	}

	php := site.PHPVersion

	switch {
	case site.Type == "laravel":
		if site.Repository != "" {
			clone()

			list.Add("Install Composer dependencies",
				fmt.Sprintf("cd %s && sudo -u www-data COMPOSER_HOME=/tmp/composer "+
					"composer install --no-dev --optimize-autoloader --no-interaction", root))
		} else {
			list.Add("Install a fresh Laravel application",
				fmt.Sprintf("sudo -u www-data COMPOSER_HOME=/tmp/composer composer "+
					"create-project laravel/laravel %s --no-interaction --prefer-dist", root))
		}

		list.AddFn("Write the .env file", s.writeLaravelEnv(ctx, site.ID))

		list.Add("Prepare the application",
			fmt.Sprintf(
				"cd %s && sudo -u www-data php%s artisan key:generate --force; "+
					"cd %s && sudo -u www-data php%s artisan storage:link || true; "+
					"cd %s && sudo -u www-data php%s artisan migrate --force || echo \"migrations skipped\"; "+
					"cd %s && sudo -u www-data php%s artisan config:cache && "+
					"sudo -u www-data php%s artisan route:cache || true",
				root, php, root, php, root, php, root, php, php))

		list.Add("Set writable permissions",
			fmt.Sprintf("sudo chown -R www-data:www-data %s && sudo chmod -R 775 %s/storage %s/bootstrap/cache",
				root, site.RootPath, site.RootPath))

	case site.Type == "wordpress":
		list.AddFn("Install WordPress", s.installWordPress(ctx, site.ID))

	case site.Type == "nextcloud":
		list.AddFn("Prepare PHP for Nextcloud", s.installNextcloudPHP(ctx, site.ID))
		list.AddFn("Install Nextcloud", s.installNextcloud(ctx, site.ID))

	case site.IsProxied():
		if site.NodeMinimum() > 0 {
			list.AddFn("Make sure Node.js is new enough", s.ensureNodeRuntime(ctx, site.ID))
		}

		switch {
		case site.Type == "n8n":
			list.AddFn("Install n8n", s.installN8N(ctx, site.ID))

		default:
			if site.Repository != "" {
				clone()
			} else {
				list.AddFn("Scaffold a starter app", s.scaffoldNodeApp(ctx, site.ID))
			}

			// Dev dependencies are installed when there is something to
			// build with them, and skipped when there is not.
			//
			// `--omit=dev` for a type that builds is a deployment that cannot
			// work: the build tool is a dev dependency in every scaffold these
			// frameworks ship — `nuxt` is one in Nuxt's own template, and so
			// are the Tailwind and PostCSS a Next.js app is generated with —
			// so the install succeeds, and the build then fails on a command
			// that is not installed. A plain Node service with no build step
			// still gets production dependencies only, which is what it
			// should run with.
			omit := " --omit=dev"
			if site.BuildCommand != "" {
				omit = ""
			}

			list.Add("Install dependencies",
				fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp npm install%s --no-audit --no-fund",
					root, omit))

			if site.BuildCommand != "" {
				list.Add("Build the application",
					fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp %s", root, site.BuildCommand))
			}
		}

		list.AddFn("Create the systemd service", s.createSystemdUnit(ctx, site.ID))

		// Ninety seconds before it is called dead: a Next.js server starts in
		// one, n8n migrates its database first.
		list.AddFn("Start the application", s.startAndWait(ctx, site.ID))

	default:
		// PHP and static, with or without a repository.
		if site.Repository != "" {
			clone()
			list.Add("Fix permissions", "sudo chown -R www-data:www-data "+root)
		} else {
			list.AddFn("Write a placeholder page", s.writePlaceholder(ctx, site.ID))
		}
	}

	list.AddFn("Write the nginx vhost", s.writeVhost(ctx, site.ID))
	// Last before nginx: every step above may have reset the modes, and this
	// is what keeps the site's files from the machine's other accounts.
	list.AddFn("Keep the site's files to itself", func(runner *tasks.Runner) (string, error) {
		return "", s.IsolateSite(site)
	})

	list.Add("Reload nginx", "sudo nginx -t", "sudo systemctl reload nginx")

	// The certificate — unless the site's DNS provider is already serving one.
	//
	// A Cloudflare record with the orange cloud on answers the visitor with
	// Cloudflare's certificate, and on Flexible it fetches from here over plain
	// HTTP. Asking Let's Encrypt for a certificate in that arrangement adds two
	// or three minutes to every deployment, has to wait for DNS to spread
	// first, fails more often than anything else here, and produces a file
	// nobody is ever shown. Full and Strict are the other half of the question:
	// there the proxy dials this machine over HTTPS, so the certificate is
	// needed however the visitor is served.
	if site.SSL {
		if edge := s.edgeTLS(ctx, site); edge.Terminates && !edge.OriginNeedsCertificate {
			list.AddFn("Let the DNS provider serve TLS", s.useEdgeTLS(ctx, site.ID, edge.Mode))
		} else {
			list.AddFn("Request the TLS certificate", s.requestCertificate(ctx, site.ID)).Optional()
			list.AddFn("Serve the site over HTTPS", s.enableHTTPS(ctx, site.ID)).Optional()
		}
	}

	// Both applications store absolute URLs, so they can only be told which
	// scheme won once the certificate step has run.
	switch site.Type {
	case "wordpress":
		list.AddFn("Point WordPress at the final URL", s.pointWordPressAtURL(ctx, site.ID)).Optional()

	case "nextcloud":
		list.AddFn("Point Nextcloud at the final URL", s.pointNextcloudAtURL(ctx, site.ID))

	case "n8n":
		// The environment file names the URL and whether the cookie may be
		// marked secure, and both are only settled now. Rewritten, then the
		// service restarted onto it.
		list.AddFn("Restart the application on its final URL",
			s.restartOnFinalURL(ctx, site.ID)).Optional()
	}

	// The last word on whether the site actually serves anything. A deployment
	// that reports success on a site answering 502 is the panel telling the
	// operator to go and find out for themselves.
	list.AddFn("Check the site answers", s.checkThroughNginx(ctx, site.ID))

	// And on whether anyone can get to it. The step above asks nginx on this
	// machine with `--resolve`, on purpose, so it passes whether or not the
	// domain resolves — which means a site whose name points nowhere finishes
	// every step green and then shows NXDOMAIN in the browser.
	list.AddFn("Check the domain points here", s.checkDomainResolves(ctx, site.ID)).Optional()

	return list
}

// Deploy runs the whole recipe: directory, application, vhost, DNS and
// certificate.
func (s *Service) Deploy(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.deploy", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	s.Store.SetSiteStatus(site.ID, "deploying", "")

	runner := tasks.New(s.Store, taskID)

	if runner.Run(s.recipe(ctx, site)) {
		s.Store.SetSiteStatus(site.ID, "active", "")

		return nil
	}

	message := "Deployment failed."

	if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
		message = task.Message
	}

	s.Store.SetSiteStatus(site.ID, "failed", message)

	return errors.New(message)
}

// Destroy removes the vhost, the service, the files, the database and the DNS
// records, then the row.
func (s *Service) Destroy(ctx context.Context, siteID int64, deleteFiles bool) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.delete", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	list := &tasks.List{}

	if site.IsProxied() || proc.Ok("test -f "+proc.Quote(UnitPath(site))) {
		service := site.ServiceName()

		list.Add("Stop the application service",
			fmt.Sprintf("sudo systemctl disable --now %s || true; sudo rm -f %s; sudo rm -f %s; "+
				"sudo systemctl daemon-reload",
				service, proc.Quote(UnitPath(site)), proc.Quote("/var/log/"+service+".log"))).Optional()
	}

	if site.Type == "nextcloud" {
		list.Add("Remove the Nextcloud cron job",
			"sudo rm -f "+proc.Quote("/etc/cron.d/ubuntu-panel-nextcloud-"+
				strings.ReplaceAll(site.Domain, ".", "-"))).Optional()
	}

	// The cache's zone file and its stored pages go with the vhost that named
	// them. A zone declared for a site that no longer exists still holds its
	// shared memory, and the pages are a copy of a site somebody deleted.
	list.Add("Remove the nginx vhost",
		fmt.Sprintf("sudo rm -f %s; sudo rm -f %s; sudo rm -f %s; sudo rm -rf %s; "+
			"sudo nginx -t && sudo systemctl reload nginx",
			proc.Quote(EnabledPath(site)), proc.Quote(AvailablePath(site)),
			proc.Quote(CacheConfigPath(site)), proc.Quote(CacheDir(site))))

	if site.RepoDeployKey != "" || proc.Exists(DeployKeyPath(site), true) {
		list.AddFn("Remove the deploy key", func(runner *tasks.Runner) (string, error) {
			RemoveDeployKey(site)

			return "The deploy key is gone from this server. It is still listed on the " +
				"repository until somebody removes it there — it can no longer be used " +
				"to read anything, but it is tidier gone.", nil
		}).Optional()
	}

	// Records the panel published are the panel's to take away. A provider
	// that has already lost them is not a reason to keep the site row, so this
	// step is optional.
	if site.ManageDNS && site.DNSAccountID > 0 {
		list.AddFn("Remove the DNS records", func(runner *tasks.Runner) (string, error) {
			return dns.RemoveForSite(ctx, s.Store, site)
		}).Optional()
	}

	// Only databases the panel created for this site are dropped.
	if site.DatabaseID > 0 {
		if database, err := s.Store.FindDatabase(ctx, site.DatabaseID); err == nil && database.ManagedBySite {
			list.AddFn("Drop the site database", func(runner *tasks.Runner) (string, error) {
				if err := s.Databases.Drop(ctx, database.ID, runner); err != nil {
					return "", err
				}

				return "Database dropped.", nil
			}).Optional()
		}
	}

	if deleteFiles {
		list.Add("Delete site files", "sudo rm -rf "+proc.Quote(site.RootPath))
	}

	// The backups go whatever was decided about the files.
	//
	// They are copies of a site that is being removed, so keeping them is disk
	// nobody accounts for holding a site nobody has — and the rows would offer a
	// restore onto a site that no longer exists. Somebody who wants the site
	// back should not delete it.
	list.AddFn("Delete this site's backups", func(runner *tasks.Runner) (string, error) {
		backups, err := s.Store.BackupsForSite(ctx, site.ID)
		if err != nil {
			return "", err
		}

		for _, backup := range backups {
			s.RemoveBackup(backup)
		}

		proc.Ok("sudo rm -rf " + proc.Quote(BackupRoot+"/"+site.Domain))

		if len(backups) == 0 {
			return "There were none.", nil
		}

		return fmt.Sprintf("%d removed.", len(backups)), nil
	}).Optional()

	runner := tasks.New(s.Store, taskID)

	if !runner.Run(list) {
		s.Store.SetSiteStatus(site.ID, "failed", "Deletion did not complete.")

		return errors.New("the site was not fully removed")
	}

	s.Store.DeleteSite(site.ID)

	return nil
}

// Restart restarts a node-family app.
func (s *Service) Restart(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	if !site.IsProxied() {
		return errors.New("this kind of site has no service to restart")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.restart", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	service := site.ServiceName()

	list := &tasks.List{}
	list.Add("Restart "+service,
		fmt.Sprintf("sudo systemctl restart %s; sleep 2; sudo systemctl status %s --no-pager --lines=20",
			service, service))

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the service did not restart")
	}

	return nil
}

// Pull fetches the latest commit and rebuilds.
func (s *Service) Pull(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	if strings.TrimSpace(site.Repository) == "" {
		return errors.New("this site was not deployed from a repository")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.pull", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	branch := site.Branch
	if branch == "" {
		branch = "main"
	}

	root := proc.Quote(site.RootPath)

	list := &tasks.List{}

	list.Add("Pull the latest commit", FetchCommand(site, root, branch))

	if site.Type == "laravel" {
		list.Add("Update dependencies and migrate",
			fmt.Sprintf(
				"cd %s && sudo -u www-data COMPOSER_HOME=/tmp/composer composer install "+
					"--no-dev --optimize-autoloader --no-interaction && "+
					"sudo -u www-data php%s artisan migrate --force && "+
					"sudo -u www-data php%s artisan config:cache && "+
					"sudo -u www-data php%s artisan route:cache",
				root, site.PHPVersion, site.PHPVersion, site.PHPVersion))
	}

	if site.IsProxied() {
		// Dev dependencies when there is something to build with them, the
		// same rule the deployment follows: the build tool is a dev dependency
		// in every scaffold these frameworks ship, so `--omit=dev` here is an
		// install that succeeds and a rebuild that fails.
		omit := " --omit=dev"
		if site.BuildCommand != "" {
			omit = ""
		}

		list.Add("Reinstall dependencies",
			fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp npm install%s --no-audit --no-fund",
				root, omit))

		if site.BuildCommand != "" {
			list.Add("Rebuild", fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp %s", root, site.BuildCommand))
		}

		service := site.ServiceName()

		list.Add("Restart the service",
			fmt.Sprintf("sudo systemctl restart %s; sleep 2; sudo systemctl is-active %s", service, service))
	}

	list.AddFn("Record the commit", s.recordCommit(ctx, site.ID))

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the pull did not complete")
	}

	return nil
}

// checkRepository is the pre-flight, as a step.
func (s *Service) checkRepository(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		access := CheckAccess(site, site.Branch)

		if !access.OK || !access.Found {
			return "", errors.New(access.Message)
		}

		return access.Message, nil
	}
}

// recordCommit stores what the site is serving, so the page can say it and a
// polled pull can tell whether there is anything to do.
//
// Optional in effect — it returns no error — because a site that deployed is
// deployed whether or not the panel managed to read its git log.
func (s *Service) recordCommit(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", nil
		}

		commit := LocalHead(site)
		if commit == "" {
			return "Could not read the commit from the checkout.", nil
		}

		s.Store.SetSiteLastCommit(site.ID, commit)

		if subject := LocalSubject(site); subject != "" {
			return "Serving " + commit + " — " + subject, nil
		}

		return "Serving " + commit + ".", nil
	}
}

// SyncDNS republishes a site's records on demand, as its own task.
func (s *Service) SyncDNS(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.dns", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	list := &tasks.List{}
	list.AddFn("Publish DNS records", s.publishDNS(ctx, site.ID))

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the DNS records were not published")
	}

	return nil
}
