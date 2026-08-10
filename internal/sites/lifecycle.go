package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// recipe builds the deployment steps for a site, branching on its type.
func (s *Service) recipe(ctx context.Context, site *store.Site) *tasks.List {
	list := &tasks.List{}

	root := proc.Quote(site.RootPath)
	docroot := proc.Quote(site.DocumentRoot())

	list.Add("Create the site directory",
		fmt.Sprintf("sudo mkdir -p %s && sudo chown -R www-data:www-data %s && sudo chmod -R 755 %s",
			docroot, root, root))

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
		list.Add("Clone the repository",
			fmt.Sprintf("sudo rm -rf %s && sudo mkdir -p %s && sudo chown www-data:www-data %s",
				root, root, root),
			fmt.Sprintf("sudo -u www-data git clone --depth 1 --branch %s %s %s",
				proc.Quote(branch), proc.Quote(site.Repository), root))
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

	case site.IsProxied():
		if site.Repository != "" {
			clone()
		} else {
			list.AddFn("Scaffold a starter app", s.scaffoldNodeApp(ctx, site.ID))
		}

		list.Add("Install dependencies",
			fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp npm install --omit=dev --no-audit --no-fund", root))

		if site.BuildCommand != "" {
			list.Add("Build the application",
				fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp %s", root, site.BuildCommand))
		}

		list.AddFn("Create the systemd service", s.createSystemdUnit(ctx, site.ID))

		service := site.ServiceName()

		list.Add("Start the application",
			fmt.Sprintf(
				"sudo systemctl restart %s; sleep 3; sudo systemctl is-active %s; "+
					"curl -sS -o /dev/null -w \"app responded with HTTP %%{http_code}\\n\" "+
					"http://127.0.0.1:%d/ || echo \"app not answering yet — check the service log\"",
				service, service, site.AppPort))

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
	list.Add("Reload nginx", "sudo nginx -t", "sudo systemctl reload nginx")

	if site.ManageDNS {
		list.AddFn("Publish DNS records", s.publishDNS(ctx, site.ID))
	}

	if site.SSL {
		list.AddFn("Request the TLS certificate", s.requestCertificate(ctx, site.ID)).Optional()
		list.AddFn("Serve the site over HTTPS", s.enableHTTPS(ctx, site.ID)).Optional()
	}

	if site.Type == "wordpress" {
		list.AddFn("Point WordPress at the final URL", s.pointWordPressAtURL(ctx, site.ID)).Optional()
	}

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

	if site.IsProxied() {
		service := site.ServiceName()

		list.Add("Stop the application service",
			fmt.Sprintf("sudo systemctl disable --now %s || true; sudo rm -f %s; sudo rm -f %s; "+
				"sudo systemctl daemon-reload",
				service, proc.Quote(UnitPath(site)), proc.Quote("/var/log/"+service+".log"))).Optional()
	}

	list.Add("Remove the nginx vhost",
		fmt.Sprintf("sudo rm -f %s; sudo rm -f %s; sudo nginx -t && sudo systemctl reload nginx",
			proc.Quote(EnabledPath(site)), proc.Quote(AvailablePath(site))))

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

	list.Add("Pull the latest commit",
		fmt.Sprintf("cd %s && sudo -u www-data git fetch --depth 1 origin %s && "+
			"sudo -u www-data git reset --hard %s",
			root, proc.Quote(branch), proc.Quote("origin/"+branch)))

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
		list.Add("Reinstall dependencies",
			fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp npm install --omit=dev --no-audit --no-fund", root))

		if site.BuildCommand != "" {
			list.Add("Rebuild", fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp %s", root, site.BuildCommand))
		}

		service := site.ServiceName()

		list.Add("Restart the service",
			fmt.Sprintf("sudo systemctl restart %s; sleep 2; sudo systemctl is-active %s", service, service))
	}

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the pull did not complete")
	}

	return nil
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
