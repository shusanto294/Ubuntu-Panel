package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// PHPDirectives are the settings the PHP tab offers, in the order it shows
// them.
//
// A list rather than a free-text box: these are the four an operator actually
// comes looking for — an upload that fails at 2 MB, a script killed at 30
// seconds — and a box that took any directive would be a box that takes
// `disable_functions` and a site that stops working.
var PHPDirectives = []struct {
	Key     string
	Label   string
	Help    string
	Default string
}{
	{"upload_max_filesize", "Maximum upload size", "The largest single file a form may send.", "2M"},
	{"post_max_size", "Maximum POST size", "The whole request. Never smaller than the upload size.", "8M"},
	{"memory_limit", "Memory limit", "How much one request may allocate.", "128M"},
	{"max_execution_time", "Maximum execution time", "Seconds before a script is killed.", "30"},
	{"max_input_vars", "Maximum input variables", "Fields in one request. Large forms need more.", "1000"},
}

// IsPHPDirective reports whether a name is one this panel writes.
func IsPHPDirective(name string) bool {
	for _, directive := range PHPDirectives {
		if directive.Key == name {
			return true
		}
	}

	return false
}

// SetPHPSettings stores a site's php.ini overrides and rewrites its vhost.
//
// Through the vhost, because that is where the panel already passes PHP_VALUE
// to FPM. Written and reloaded here rather than on the queue: it is a file and
// a reload, the operator is watching, and a deployment would rebuild the
// application to change an upload limit.
func (s *Service) SetPHPSettings(ctx context.Context, siteID int64, settings map[string]string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	if !site.IsPHP() {
		return errors.New("this site does not run PHP")
	}

	clean := map[string]string{}

	for name, value := range settings {
		value = strings.TrimSpace(value)

		if value == "" || !IsPHPDirective(name) {
			continue
		}

		// The same rule the vhost applies, checked here so a value that would
		// be silently dropped is refused where somebody can see it.
		if !safeINIToken(value) {
			return fmt.Errorf("%q is not a value PHP will take for %s", value, name)
		}

		clean[name] = value
	}

	encoded, err := json.Marshal(clean)
	if err != nil {
		return err
	}

	s.Store.SetSitePHPSettings(site.ID, string(encoded))

	return s.rewriteVhost(ctx, site.ID)
}

// SetWebDirectory moves a site's document root within its own directory.
//
// Within it, and nowhere else: the value is a path under the site root, so a
// site cannot be pointed at another site's files or at /etc by typing `..` into
// a form.
func (s *Service) SetWebDirectory(ctx context.Context, siteID int64, directory string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	directory = strings.Trim(strings.TrimSpace(directory), "/")

	if directory != "" {
		for _, part := range strings.Split(directory, "/") {
			if part == "" || part == "." || part == ".." {
				return errors.New("the root directory has to be a folder inside the site, " +
					"like public or dist")
			}
		}

		directory = "/" + directory
	}

	if directory == site.WebDirectory {
		return nil
	}

	s.Store.SetSiteWebDirectory(site.ID, directory)

	// Created if it is not there. nginx serves a root that does not exist as a
	// 404 for every request, which reads as the site having been deleted.
	updated, err := s.Store.FindSite(ctx, site.ID)
	if err == nil {
		proc.Ok(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chown -R www-data:www-data %[1]s",
			proc.Quote(updated.DocumentRoot())))
	}

	return s.rewriteVhost(ctx, site.ID)
}

// ---------------------------------------------------------------- the cache

// SetCache turns nginx's page cache on or off for a site and sets how long a
// cached page lives.
//
// Both halves are written before nginx is asked to take them: the zone in
// conf.d, then the vhost that names it. A vhost naming a zone that does not
// exist is a configuration nginx refuses — and it refuses the whole of it, for
// every site on the machine, so the order matters more than it looks.
func (s *Service) SetCache(ctx context.Context, siteID int64, enabled bool, ttl string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	if enabled && !Cacheable(site) {
		return fmt.Errorf("the panel does not cache a %s site — see the Cache tab for why",
			strings.ToLower(site.TypeLabel()))
	}

	if strings.TrimSpace(ttl) == "" {
		ttl = DefaultCacheTTL
	}

	if !ValidCacheTTL(ttl) {
		return fmt.Errorf("%q is not one of the lifetimes the panel offers", ttl)
	}

	s.Store.SetSiteCache(site.ID, enabled, ttl)

	updated, err := s.Store.FindSite(ctx, site.ID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	restore := func() {
		s.Store.SetSiteCache(site.ID, site.CacheEnabled, site.CacheTTL)

		if previous, err := s.Store.FindSite(ctx, site.ID); err == nil {
			s.writeCacheConfig(previous)
		}
	}

	if err := s.writeCacheConfig(updated); err != nil {
		restore()

		return err
	}

	if err := s.rewriteVhost(ctx, site.ID); err != nil {
		restore()

		return err
	}

	return nil
}

// writeCacheConfig writes — or removes — the conf.d file that declares this
// site's cache zone, and makes the directory nginx writes into.
//
// Removed rather than left behind when the cache is turned off: a zone
// declared for a vhost that no longer names it still reserves its shared
// memory, and a stale one for a deleted site is a file nobody will ever think
// to look at.
func (s *Service) writeCacheConfig(site *store.Site) error {
	if !CacheActive(site) {
		proc.Ok("sudo rm -f " + proc.Quote(CacheConfigPath(site)))

		return nil
	}

	directory := proc.Quote(CacheDir(site))

	if !proc.Ok(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chown -R www-data:www-data %[1]s",
		directory)) {
		return errors.New("the cache directory could not be created")
	}

	return proc.PutFile(CacheConfigPath(site), RenderCacheConfig(site), true)
}

// PurgeCache throws away everything nginx has stored for a site.
//
// The files are the cache: nginx notices they are gone and renders the next
// request from the application. This is what somebody publishes a change and
// then reaches for, so it works whether or not the cache is currently on —
// turning it off does not empty it.
func (s *Service) PurgeCache(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	directory := proc.Quote(CacheDir(site))

	// mkdir first, so a site that has never cached anything is emptied
	// successfully rather than failing on a directory that is not there.
	if !proc.Ok(fmt.Sprintf("sudo mkdir -p %[1]s && sudo find %[1]s -mindepth 1 -delete",
		directory)) {
		return errors.New("the cache could not be emptied")
	}

	return nil
}

// rewriteVhost writes the site's vhost from its current record and reloads
// nginx, putting the old file back if nginx will not have the new one.
//
// The rollback is the point. This runs from a form, on a machine serving other
// sites, and a vhost nginx refuses freezes the running configuration for every
// one of them.
func (s *Service) rewriteVhost(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	path := AvailablePath(site)
	previous := proc.Run("sudo cat "+proc.Quote(path)+" 2>/dev/null", 30*time.Second).Output

	// Whether the certificate is there decides which form is written; asking
	// is cheaper than being wrong and having nginx refuse the file.
	secure := site.SSL && proc.Ok("sudo test -f "+proc.Quote(CertificatePath(site)+"/fullchain.pem"))

	if err := proc.PutFile(path, RenderVhost(site, secure, http2Supported()), true); err != nil {
		return err
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		if previous != "" {
			proc.PutFile(path, previous, true)
		}

		return fmt.Errorf("nginx refused the new configuration, so the old one was put back:\n%s",
			check.Output)
	}

	if !proc.Ok("sudo systemctl reload nginx") {
		return errors.New("nginx accepted the configuration but would not reload")
	}

	return nil
}

// Certificate requests or renews the site's TLS certificate as its own task.
//
// Its own action rather than a redeploy: a certificate that did not issue the
// first time is usually a DNS record that has since been fixed, and rebuilding
// the whole application to find out is minutes of work to run certbot.
func (s *Service) Certificate(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return err
	}

	if !site.SSL {
		return errors.New("this site was created without HTTPS")
	}

	taskID, err := s.Store.CreateTask(ctx, "site", "site.certificate", site.UserID, site.ID, site.Domain)
	if err != nil {
		return err
	}

	list := &tasks.List{}
	list.AddFn("Request the TLS certificate", s.requestCertificate(ctx, site.ID))
	list.AddFn("Serve the site over HTTPS", s.enableHTTPS(ctx, site.ID))

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the certificate was not issued")
	}

	return nil
}

// ---------------------------------------------------------------- cron jobs

// CronPath is the file a site's scheduled commands live in.
//
// One file per site under /etc/cron.d, rather than a user crontab: it is
// readable, it says which site it belongs to, and deleting a site takes its
// schedule with it.
func CronPath(site *store.Site) string {
	return "/etc/cron.d/ubuntu-panel-site-" + strings.ReplaceAll(site.Domain, ".", "-")
}

// CronJob is one line of that file.
type CronJob struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

// CronJobs reads a site's schedule back.
//
// Parsed from the file rather than kept in the database, so a line added by
// hand shows up here instead of being silently overwritten the next time the
// panel writes one.
func (s *Service) CronJobs(site *store.Site) []CronJob {
	contents := proc.Run("sudo cat "+proc.Quote(CronPath(site))+" 2>/dev/null", 30*time.Second).Output

	jobs := []CronJob{}

	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "=") &&
			!strings.HasPrefix(line, "*") && len(strings.Fields(line)) < 6 {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		// Five schedule fields, then the user this runs as, then the command.
		jobs = append(jobs, CronJob{
			Schedule: strings.Join(fields[0:5], " "),
			Command:  strings.Join(fields[6:], " "),
		})
	}

	return jobs
}

// SetCronJobs writes the whole file.
//
// Whole rather than appended: the page shows every line and posts back every
// line, so what is on screen and what is on disk are the same list, and a job
// removed on the page is a job removed on the machine.
func (s *Service) SetCronJobs(ctx context.Context, siteID int64, jobs []CronJob) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	var file strings.Builder

	fmt.Fprintf(&file, "# Managed by Ubuntu Panel — scheduled commands for %s.\n"+
		"# Edited from the site's Cron jobs tab. Lines added here by hand are kept.\n"+
		"SHELL=/bin/sh\n"+
		"PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin\n\n", site.Domain)

	written := 0

	for _, job := range jobs {
		schedule := strings.Join(strings.Fields(job.Schedule), " ")
		command := strings.TrimSpace(job.Command)

		if schedule == "" || command == "" {
			continue
		}

		if len(strings.Fields(schedule)) != 5 {
			return fmt.Errorf("%q is not a schedule: it needs five fields, like `0 3 * * *`", job.Schedule)
		}

		// A newline would end the line and start another one nobody typed, and
		// a percent is cron's own line separator — the two ways a command in a
		// crontab becomes two commands.
		if strings.ContainsAny(command, "\n\r%") {
			return errors.New("a command cannot contain a newline or a % — cron reads both as the end of it")
		}

		// As www-data, which owns the site's files: a scheduled command that
		// runs as root is a way to make a web application root.
		fmt.Fprintf(&file, "%s www-data cd %s && %s\n",
			schedule, proc.Quote(site.RootPath), command)

		written++
	}

	if written == 0 {
		// An empty file rather than none, so cron has nothing to run and the
		// tab has something to read.
		proc.Ok("sudo rm -f " + proc.Quote(CronPath(site)))

		return nil
	}

	if err := proc.PutFile(CronPath(site), file.String(), true); err != nil {
		return err
	}

	// cron refuses a file in /etc/cron.d that anybody but root can write, and
	// says nothing about it anywhere the operator will look.
	proc.Ok(fmt.Sprintf("sudo chown root:root %[1]s && sudo chmod 644 %[1]s",
		proc.Quote(CronPath(site))))

	return nil
}
