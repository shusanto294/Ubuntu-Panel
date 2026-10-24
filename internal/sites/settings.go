package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
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

// SetPHPVersion points one site at another PHP-FPM.
//
// Per site rather than per machine: the panel's default decides what a new site
// is built against, and a server hosting eight sites has no reason for all
// eight to move together. A WordPress install that wants 8.4 and a Laravel app
// pinned to 8.2 are both ordinary, and both are one vhost apart.
//
// The version has to be on the machine already. nginx is perfectly happy to
// write `fastcgi_pass unix:/run/php/php8.5-fpm.sock` for a socket that is not
// there, and the site then answers 502 to every request — so this refuses
// rather than reload a configuration it knows is broken, and the caller is the
// one that installs a missing version first.
//
// Written and reloaded here rather than queued: it is a file and a reload, and
// a deployment would rebuild the whole application to change a socket path.
func (s *Service) SetPHPVersion(ctx context.Context, siteID int64, version string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	if !site.IsPHP() {
		return errors.New("this site does not run PHP")
	}

	version = strings.TrimSpace(version)

	if version == "" {
		return errors.New("choose a PHP version")
	}

	if version == site.PHPVersion {
		return nil
	}

	if !PHPVersionReady(version) {
		return fmt.Errorf("PHP %s is not running on this machine, so a site pointed at it "+
			"would answer 502 to every request", version)
	}

	s.Store.SetSitePHPVersion(site.ID, version)

	if err := s.rewriteVhost(ctx, site.ID); err != nil {
		// nginx refused it and kept serving the old file, so the row goes back
		// to matching what is actually on disk.
		s.Store.SetSitePHPVersion(site.ID, site.PHPVersion)

		return err
	}

	return nil
}

// PHPVersionReady reports whether this version's FPM socket is on the machine
// — which is the only thing a vhost pointed at it depends on.
func PHPVersionReady(version string) bool {
	if strings.TrimSpace(version) == "" {
		return false
	}

	return proc.Ok("test -S /run/php/php" + version + "-fpm.sock")
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

// ------------------------------------------------------------- the hostnames

// SetAliases replaces the extra hostnames a site answers on and writes the
// vhost from them.
//
// The vhost half only. A new hostname also wants a DNS record published and a
// certificate that covers it, and both of those are minutes of work against
// somebody else's server — so the caller queues them, the same way the site
// page's own buttons do. `changed` is false when the list is the one already
// stored, which is what keeps a saved form from asking Let's Encrypt for a
// certificate it issued a moment ago.
//
// A name that nginx will not take never reaches a served configuration:
// rewriteVhost asks `nginx -t` before reloading and puts the previous file back
// if the answer is no.
func (s *Service) SetAliases(ctx context.Context, siteID int64, aliases []string) (bool, error) {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return false, errors.New("that site no longer exists")
	}

	clean := []string{}
	seen := map[string]bool{strings.ToLower(site.Domain): true}

	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))

		// The site's own domain is already the first name in the server_name,
		// and repeating it is a vhost nginx refuses as a conflicting name.
		if alias == "" || seen[alias] {
			continue
		}

		seen[alias] = true
		clean = append(clean, alias)
	}

	if equalStrings(clean, site.Aliases) {
		return false, nil
	}

	if err := s.Store.SetSiteAliases(site.ID, clean); err != nil {
		return false, errors.New("the panel could not store the hostnames")
	}

	if err := s.rewriteVhost(ctx, site.ID); err != nil {
		// nginx refused it and kept serving the old file, so the row is put
		// back to match what is actually on disk.
		s.Store.SetSiteAliases(site.ID, site.Aliases)

		return false, err
	}

	return true, nil
}

// equalStrings compares two lists in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// ---------------------------------------------------------------- the cache

// SetCache gives a site one cache, or none.
//
// One rather than both, because the two answer different problems and the
// choice between them is the interesting part: the page cache serves the same
// finished HTML to everybody and cannot be put in front of pages that are
// personal; the object cache keeps what the application looked up while
// building a page, so every visitor still gets their own page and the database
// is asked once instead of once each.
//
// Both halves of a change are written before nginx is asked to take them: the
// zone in conf.d, then the vhost that names it. A vhost naming a zone that
// does not exist is a configuration nginx refuses — and it refuses the whole
// of it, for every site on the machine, so the order matters more than it
// looks.
func (s *Service) SetCache(ctx context.Context, siteID int64, mode, ttl string) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	switch mode {
	case "off":
	case "nginx":
		if !Cacheable(site) {
			return fmt.Errorf("the panel does not put a page cache in front of a %s site — "+
				"the Cache tab says why", strings.ToLower(site.TypeLabel()))
		}
	case "redis":
		if !RedisSupported(site) {
			return fmt.Errorf("the panel cannot wire a %s site into Redis by itself — "+
				"the Cache tab says why", strings.ToLower(site.TypeLabel()))
		}
	default:
		return errors.New("that is not a cache the panel offers")
	}

	if strings.TrimSpace(ttl) == "" {
		ttl = DefaultCacheTTL
	}

	if !ValidCacheTTL(ttl) {
		return fmt.Errorf("%q is not one of the lifetimes the panel offers", ttl)
	}

	previous := site.CacheMode

	s.Store.SetSiteCache(site.ID, mode, ttl)

	updated, err := s.Store.FindSite(ctx, site.ID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	// Written from the stored settings, so changing the lifetime goes through
	// the same path as turning it on — and put back the way it was if the
	// machine will not take it.
	if mode == "redis" {
		if err := s.EnableRedisCache(ctx, updated); err != nil {
			s.Store.SetSiteCache(site.ID, previous, site.CacheTTL)

			return err
		}
	}

	restore := func() {
		s.Store.SetSiteCache(site.ID, previous, site.CacheTTL)

		if before, err := s.Store.FindSite(ctx, site.ID); err == nil {
			s.writeCacheConfig(before)
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

	// Only once the rest of it stuck. A site left with the drop-in removed and
	// the record still saying Redis would be a cache nobody could see was off.
	if previous == "redis" && mode != "redis" {
		s.DisableRedisCache(ctx, updated)
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

// PurgeCache throws away whatever this site has cached, in whichever cache it
// has.
//
// For nginx the files are the cache: it notices they are gone and renders the
// next request from the application. For Redis it is this site's keys and
// nobody else's, which is what the per-site prefix is for. This is what
// somebody publishes a change and then reaches for, so it works whether or not
// a cache is currently on — turning one off does not empty it.
func (s *Service) PurgeCache(ctx context.Context, siteID int64) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	if site.CacheMode == "redis" {
		return s.FlushRedisCache(site)
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
