package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/migrate"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Moving a site to another panel.
//
// The old server packs the site the way a backup does — its files as they are
// on disk, ownership and modes included, and its database as the engine's own
// dump — together with the settings the site row holds. The new server makes
// the same site from that: the same root path, the same database name, user
// and password (the site's own configuration names them), the same PHP
// version, vhost and application service. Nothing is reinstalled, so a
// WordPress comes across with its plugins and its uploads exactly as they
// were.
//
// The certificate is the one thing not carried: the new server asks Let's
// Encrypt for its own once the DNS points at it (FinishImport).

// MigrationManifest is what the new server needs to know about a site.
func (s *Service) MigrationManifest(ctx context.Context, site *store.Site) (*migrate.Site, error) {
	manifest := &migrate.Site{
		Domain: site.Domain, Type: site.Type, Aliases: site.Aliases, RootPath: site.RootPath,
		WebDirectory: site.WebDirectory, PHPVersion: site.PHPVersion, NodeVersion: site.NodeVersion,
		SSL: site.SSL, TLSMode: site.TLSMode, AppPort: site.AppPort,
		StartCommand: site.StartCommand, BuildCommand: site.BuildCommand,
		Repository: site.Repository, Branch: site.Branch, RepoAuth: site.RepoAuth, RepoToken: site.RepoToken,
		DeployPublic: site.RepoDeployKey, Webhook: site.WebhookSecret,
		WPAdminUser: site.WPAdminUser, WPAdminPass: site.WPAdminPassword, WPAdminEmail: site.WPAdminEmail,
		WPTitle: site.WPTitle, PHPSettings: site.PHPSettings, CacheMode: site.CacheMode, CacheTTL: site.CacheTTL,
		ManageDNS: site.ManageDNS, DNSType: site.DNSType, DNSProxied: site.DNSProxied,
	}

	for _, job := range s.CronJobs(site) {
		manifest.Cron = append(manifest.Cron, migrate.CronJob{Schedule: job.Schedule, Command: job.Command})
	}

	if site.RepoAuth == "key" {
		if key, err := proc.ReadFile(DeployKeyPath(site), true); err == nil {
			manifest.DeployKey = key
		}
	}

	if database, _, _ := s.backupDatabase(ctx, site); database != nil {
		manifest.Database = &migrate.Database{
			Engine: database.Engine, Name: database.Name, Username: database.Username,
			Password: database.Password, Charset: database.Charset,
		}
	}

	return manifest, nil
}

// MigrationDatabase is the database a site's move carries, if any: the one
// the panel made for it.
func (s *Service) MigrationDatabase(ctx context.Context, site *store.Site) (*store.Database, string, string) {
	return s.backupDatabase(ctx, site)
}

// PackageForMigration adds the steps that write a site's files and database
// dump into directory, which must not exist yet.
func (s *Service) PackageForMigration(ctx context.Context, list *tasks.List, site *store.Site, directory string) {
	database, engine, name := s.backupDatabase(ctx, site)

	list.Add("Make room for the package",
		fmt.Sprintf("sudo mkdir -p %s && sudo chmod 700 %s", proc.Quote(directory), proc.Quote(directory)))

	if database != nil {
		list.AddFn("Dump the "+name+" database", func(runner *tasks.Runner) (string, error) {
			return dumpDatabase(runner, engine, name, directory)
		})
	}

	list.Add("Pack the site's files",
		fmt.Sprintf("sudo tar -czpf %s --numeric-owner --warning=no-file-changed -C %s . || [ $? = 1 ]",
			proc.Quote(directory+"/files.tar.gz"), proc.Quote(site.RootPath)))

	// The panel reads the package to send it, and it runs as its own user.
	list.Add("Hand the package to the panel",
		fmt.Sprintf("sudo chown -R \"$(id -un)\" %s", proc.Quote(directory)))
}

// ImportMigrated builds a site that arrived from another panel, from the
// files in directory, as the task taskID. Everything but the certificate:
// that waits for the DNS to point here (FinishImport).
func (s *Service) ImportMigrated(ctx context.Context, siteID, taskID int64, directory string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	s.Store.SetSiteStatus(site.ID, "deploying", "")

	root := proc.Quote(site.RootPath)
	list := &tasks.List{}

	list.Add("Create the site directory",
		fmt.Sprintf("sudo mkdir -p %s && sudo chown www-data:www-data %s", root, root))

	if site.IsPHP() {
		list.AddFn("Make sure PHP "+site.PHPVersion+" is on this machine", s.ensurePHPRuntime(ctx, site.ID))
	}

	if site.Type == "nextcloud" {
		list.AddFn("Prepare PHP for Nextcloud", s.installNextcloudPHP(ctx, site.ID))
	}

	if site.DatabaseID > 0 {
		list.AddFn("Create the database", s.ensureDatabase(ctx, site.ID))
	}

	// The restore's own steps, pointed at the package: unpacked beside the
	// root and swapped in, and the database emptied and loaded.
	unpack := &store.Backup{ID: time.Now().Unix(), Path: directory, HasFiles: true}
	list.AddFn("Unpack the site's files", s.restoreFiles(site, unpack))

	if site.DatabaseID > 0 {
		if database, err := s.Store.FindDatabase(ctx, site.DatabaseID); err == nil {
			dump := &store.Backup{Path: directory, DatabaseName: database.Name, DatabaseEngine: database.Engine}
			charset := database.Charset
			list.AddFn("Load the "+database.Name+" database", func(runner *tasks.Runner) (string, error) {
				return restoreDatabase(runner, dump, charset)
			})
		}
	}

	// The files belong to the web server, as every site's do, and nothing in
	// them runs as anybody else: a set-user-ID bit in a package is dropped.
	list.Add("Give the files to the web server",
		fmt.Sprintf("sudo chown -R www-data:www-data %[1]s && "+
			"sudo find %[1]s -type f -perm /6000 -exec chmod ug-s {} +", root))

	if site.Type == "laravel" {
		list.Add("Let Laravel write to storage",
			fmt.Sprintf("sudo chmod -R 775 %s/storage %s/bootstrap/cache", root, root)).Optional()
	}

	if site.IsProxied() {
		if site.NodeMinimum() > 0 {
			list.AddFn("Make sure Node.js is new enough", s.ensureNodeRuntime(ctx, site.ID))
		}

		// n8n runs from a global install, and its environment is a file
		// outside the site's root; its data came across in .n8n.
		if site.Type == "n8n" {
			list.AddFn("Install n8n", s.installN8N(ctx, site.ID))
		}

		list.AddFn("Create the systemd service", s.createSystemdUnit(ctx, site.ID))
		list.AddFn("Start the application", s.startAndWait(ctx, site.ID))
	}

	if site.Type == "nextcloud" {
		list.AddFn("Schedule Nextcloud's background jobs", func(runner *tasks.Runner) (string, error) {
			return "Nextcloud's cron job is in place.", writeNextcloudCron(site)
		}).Optional()
	}

	list.AddFn("Write the nginx vhost", s.writeVhost(ctx, site.ID))
	list.AddFn("Keep the site's files to itself", func(runner *tasks.Runner) (string, error) {
		return "", s.IsolateSite(site)
	})
	list.Add("Reload nginx", "sudo nginx -t", "sudo systemctl reload nginx")

	// Last, once the files they run are in place.
	if encoded, err := os.ReadFile(directory + "/cron.json"); err == nil {
		var incoming []migrate.CronJob
		if json.Unmarshal(encoded, &incoming) == nil && len(incoming) > 0 {
			list.AddFn("Schedule the site's cron jobs", func(runner *tasks.Runner) (string, error) {
				jobs := make([]CronJob, 0, len(incoming))
				for _, job := range incoming {
					jobs = append(jobs, CronJob{Schedule: job.Schedule, Command: job.Command})
				}
				if err := s.SetCronJobs(ctx, site.ID, jobs); err != nil {
					return "", err
				}
				return fmt.Sprintf("%d cron job(s) scheduled, as they were on the old server.", len(jobs)), nil
			}).Optional()
		}
	}

	list.AddFn("Check the site answers", s.checkThroughNginx(ctx, site.ID)).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		message := "The site could not be set up here."
		if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		s.Store.SetSiteStatus(site.ID, "failed", message)

		return errors.New(message)
	}

	s.Store.SetSiteStatus(site.ID, "active", "")

	return nil
}

// FinishImport is what waits for the DNS to point here: the certificate, and
// the applications that need to know whether they ended up on HTTPS.
func (s *Service) FinishImport(ctx context.Context, siteID, taskID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	list := &tasks.List{}

	switch {
	case site.EdgeTLS():
		list.AddFn("Let the DNS provider serve TLS", func(runner *tasks.Runner) (string, error) {
			return "The DNS provider's proxy serves this site's certificate, as it did on the old server.", nil
		})

	case site.SSL:
		if edge := s.edgeTLS(ctx, site); edge.Terminates && !edge.OriginNeedsCertificate {
			list.AddFn("Let the DNS provider serve TLS", s.useEdgeTLS(ctx, site.ID, edge.Mode))
		} else {
			list.AddFn("Request the TLS certificate", s.requestCertificate(ctx, site.ID)).Optional()
			list.AddFn("Serve the site over HTTPS", s.enableHTTPS(ctx, site.ID)).Optional()
		}
	}

	switch site.Type {
	case "nextcloud":
		list.AddFn("Point Nextcloud at its URL", s.pointNextcloudAtURL(ctx, site.ID)).Optional()
	case "n8n":
		list.AddFn("Restart the application on its URL", s.restartOnFinalURL(ctx, site.ID)).Optional()
	}

	list.AddFn("Check the site answers", s.checkThroughNginx(ctx, site.ID)).Optional()
	list.AddFn("Check the domain points here", s.checkDomainResolves(ctx, site.ID)).Optional()

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the site's certificate could not be set up")
	}

	return nil
}

// InstallDeployKey puts a site's repository key from the old server in place,
// owned and protected as GenerateDeployKey leaves a new one, so pulls keep
// working without the key being added to the repository again.
func InstallDeployKey(site *store.Site, private, public string) error {
	path := DeployKeyPath(site)

	proc.Run("sudo mkdir -p "+proc.Quote(DeployKeyRoot), time.Minute)

	if err := proc.PutFile(path, private, true); err != nil {
		return err
	}
	if err := proc.PutFile(path+".pub", public, true); err != nil {
		return err
	}

	command := strings.Join([]string{
		"sudo chown root:www-data " + proc.Quote(DeployKeyRoot),
		"sudo chmod 710 " + proc.Quote(DeployKeyRoot),
		"sudo chown root:www-data " + proc.Quote(path) + " " + proc.Quote(path+".pub"),
		"sudo chmod 640 " + proc.Quote(path),
		"sudo chmod 644 " + proc.Quote(path+".pub"),
		"sudo touch " + proc.Quote(KnownHostsPath()),
		"sudo chown root:www-data " + proc.Quote(KnownHostsPath()),
		"sudo chmod 660 " + proc.Quote(KnownHostsPath()),
	}, " && ")

	if result := proc.Run(command, time.Minute); !result.OK() {
		return fmt.Errorf("could not protect the deploy key: %s", result.Output)
	}

	return nil
}

// writeNextcloudCron is Nextcloud's background jobs, every five minutes.
func writeNextcloudCron(site *store.Site) error {
	cron := fmt.Sprintf(
		"# Managed by Ubuntu Panel — Nextcloud background jobs for %s\n"+
			"*/5 * * * * www-data php%s -f %s/cron.php\n",
		site.Domain, site.PHPVersion, site.DocumentRoot())

	return proc.PutFile("/etc/cron.d/ubuntu-panel-nextcloud-"+strings.ReplaceAll(site.Domain, ".", "-"), cron, true)
}
