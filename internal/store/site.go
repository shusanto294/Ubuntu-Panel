package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
)

// Site is one hosted site: a vhost, a directory, and whatever runtime its type
// needs.
type Site struct {
	ID           int64
	UserID       int64
	DNSAccountID int64
	DatabaseID   int64
	Domain       string
	Type         string
	Aliases      []string
	RootPath     string
	WebDirectory string
	PHPVersion   string
	NodeVersion  string
	Status       string
	SSL          bool
	LastError    string
	AppPort      int
	StartCommand string
	BuildCommand string
	Repository   string
	Branch       string
	// The application admin account. Named for WordPress, which was the only
	// application the panel installed when the columns were added; Nextcloud's
	// administrator lives in the same three columns rather than in three more
	// of its own.
	WPAdminUser     string
	WPAdminPassword string // decrypted
	WPAdminEmail    string
	WPTitle         string
	ManageDNS       bool
	DNSZoneID       string
	DNSRecordIDs    string // JSON object of name -> provider record id
	DNSType         string
	DNSContent      string
	DNSProxied      bool

	// PHPSettings is the per-site php.ini overrides, as a JSON object of
	// directive to value. Empty on a site nobody has changed, where PHP's own
	// defaults are what the pool was configured with.
	PHPSettings string

	// CacheMode is which cache this site was given: "off", "nginx" for the
	// page cache in front of the application, or "redis" for the object cache
	// behind it. CacheEnabled is the nginx half of that, kept as its own
	// column because the vhost is written from it.
	//
	// CacheTTL is how long a cached page is served before nginx asks the
	// application again, and means nothing to Redis, which holds what it is
	// given until the application replaces it.
	CacheMode    string
	CacheEnabled bool
	CacheTTL     string

	// TLSMode is who serves this site's certificate. Empty or "letsencrypt" is
	// the panel's own, issued by certbot and renewed here. "edge" is a DNS
	// provider whose proxy terminates TLS in front of the machine — Cloudflare
	// with the orange cloud on — where a certificate at the origin would be
	// issued, renewed and never presented to anybody.
	TLSMode string

	// BackupSchedule is how often the panel takes a backup of this site on its
	// own: "off", "daily", "weekly" or "monthly". BackupKeep is how many to
	// keep before the oldest is deleted, and BackupNextAt is the Unix second
	// the next one is due — recomputed whenever the schedule changes and after
	// every scheduled run, so nothing has to parse a cron expression to decide.
	BackupSchedule string
	BackupKeep     int
	BackupNextAt   int64

	// BackupDestination is where its backups are written: "local" for this
	// server, "s3" for the remote storage set up in Settings.
	BackupDestination string

	// How the panel reaches this site's git remote, and what it does about new
	// commits without being asked.
	//
	// RepoAuth is "none", "key" or "token". RepoDeployKey is the *public* half
	// of the key the panel generated — the private half is a file, never a
	// column. RepoToken and WebhookSecret are credentials and are stored
	// encrypted.
	RepoAuth      string
	RepoToken     string // decrypted
	RepoDeployKey string
	WebhookSecret string // decrypted

	// AutoPull is the polling interval: "off", or a key from
	// sites.PullSchedules. AutoPullNextAt is the Unix second it is next due.
	AutoPull       string
	AutoPullNextAt int64

	// LastCommit is the commit the site is serving, short form.
	LastCommit string

	// StagingOf is the site this one is a staging copy of, or zero.
	//
	// A staging site is an ordinary site in every other respect — its own
	// vhost, its own database, its own certificate — and this is what lets the
	// panel offer the two operations that make the pair useful: clone
	// production down, and push staging back up.
	StagingOf int64

	CreatedAt string
}

// DefaultWPTitle is what a WordPress site is called when the form left it
// blank.
const DefaultWPTitle = "WordPress Site"

const siteColumns = `id, user_id, dns_account_id, database_id, domain, type, aliases,
	root_path, web_directory, php_version, node_version, status, ssl, last_error,
	app_port, start_command, build_command, repository, branch,
	wp_admin_user, wp_admin_password, wp_admin_email, wp_title,
	manage_dns, dns_zone_id, dns_record_ids, dns_type, dns_content, dns_proxied,
	php_settings, cache_enabled, cache_ttl, cache_mode, tls_mode,
	backup_schedule, backup_keep, backup_next_at, staging_of,
	repo_auth, repo_token, repo_deploy_key, webhook_secret, auto_pull,
	auto_pull_next_at, last_commit, created_at, backup_destination`

type siteScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanSite(row siteScanner) (*Site, error) {
	site := &Site{}

	var dnsAccountID, databaseID, userID, appPort nullInt
	var aliases, nodeVersion, lastError, startCommand, buildCommand text
	var repository, wpUser, wpPassword, wpEmail, wpTitle text
	var dnsZoneID, dnsRecordIDs, dnsContent, phpSettings, cacheTTL, cacheMode, tlsMode text
	var backupSchedule text
	var cacheEnabled, backupKeep, backupNextAt, stagingOf nullInt
	var repoAuth, repoToken, repoDeployKey, webhookSecret, autoPull, lastCommit text
	var autoPullNextAt nullInt
	var backupDestination text

	err := row.Scan(&site.ID, &userID, &dnsAccountID, &databaseID, &site.Domain,
		&site.Type, &aliases, &site.RootPath, &site.WebDirectory, &site.PHPVersion,
		&nodeVersion, &site.Status, &site.SSL, &lastError, &appPort,
		&startCommand, &buildCommand, &repository, &site.Branch,
		&wpUser, &wpPassword, &wpEmail, &wpTitle,
		&site.ManageDNS, &dnsZoneID, &dnsRecordIDs, &site.DNSType, &dnsContent,
		&site.DNSProxied, &phpSettings, &cacheEnabled, &cacheTTL, &cacheMode, &tlsMode,
		&backupSchedule, &backupKeep, &backupNextAt, &stagingOf,
		&repoAuth, &repoToken, &repoDeployKey, &webhookSecret, &autoPull,
		&autoPullNextAt, &lastCommit, &site.CreatedAt, &backupDestination)
	if err != nil {
		return nil, notFound(err)
	}

	site.UserID = userID.Int64
	site.DNSAccountID = dnsAccountID.Int64
	site.DatabaseID = databaseID.Int64
	site.AppPort = int(appPort.Int64)
	site.NodeVersion = nodeVersion.String()
	site.LastError = lastError.String()
	site.StartCommand = startCommand.String()
	site.BuildCommand = buildCommand.String()
	site.Repository = repository.String()
	site.WPAdminUser = wpUser.String()
	site.WPAdminPassword = s.decrypt(wpPassword.String())
	site.WPAdminEmail = wpEmail.String()
	site.WPTitle = wpTitle.String()
	site.DNSZoneID = dnsZoneID.String()
	site.DNSRecordIDs = dnsRecordIDs.String()
	site.DNSContent = dnsContent.String()
	site.PHPSettings = phpSettings.String()
	site.CacheEnabled = cacheEnabled.Int64 == 1
	site.CacheTTL = cacheTTL.String()
	site.CacheMode = cacheMode.String()
	site.TLSMode = tlsMode.String()
	site.BackupSchedule = backupSchedule.String()
	site.BackupKeep = int(backupKeep.Int64)
	site.BackupNextAt = backupNextAt.Int64
	site.StagingOf = stagingOf.Int64
	site.RepoAuth = repoAuth.String()
	site.RepoToken = s.decrypt(repoToken.String())
	site.RepoDeployKey = repoDeployKey.String()
	site.WebhookSecret = s.decrypt(webhookSecret.String())
	site.AutoPull = autoPull.String()
	site.AutoPullNextAt = autoPullNextAt.Int64
	site.LastCommit = lastCommit.String()
	site.BackupDestination = orDefault(backupDestination.String(), "local")

	// A site from before the choice existed has the page cache or nothing, and
	// says so rather than reading as "off" with the cache running.
	if site.CacheMode == "" {
		site.CacheMode = "off"

		if site.CacheEnabled {
			site.CacheMode = "nginx"
		}
	}

	site.Aliases = decodeStringList(aliases.String())

	return site, nil
}

// FindSite loads a site, deleted ones included: the worker tearing one down
// still needs everything on the row.
func (s *Store) FindSite(ctx context.Context, id int64) (*Site, error) {
	return s.scanSite(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites WHERE id = ?`, id))
}

// FindSiteByDomain is what stops two sites claiming one hostname.
func (s *Store) FindSiteByDomain(ctx context.Context, domain string) (*Site, error) {
	return s.scanSite(s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites WHERE domain = ? AND deleted_at IS NULL`,
		strings.ToLower(domain)))
}

// Sites is the list the index page renders: everything not being torn down.
func (s *Store) Sites(ctx context.Context) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []*Site

	for rows.Next() {
		site, err := s.scanSite(rows)
		if err != nil {
			return nil, err
		}

		sites = append(sites, site)
	}

	return sites, rows.Err()
}

// SiteCount is what the dashboard shows and what blocks removing nginx.
func (s *Store) SiteCount(ctx context.Context) int {
	var count int

	s.db.QueryRowContext(ctxOrBackground(ctx),
		`SELECT count(*) FROM sites WHERE deleted_at IS NULL`).Scan(&count)

	return count
}

// CreateSite stores a new site row and returns its id.
func (s *Store) CreateSite(ctx context.Context, site *Site) (int64, error) {
	aliases, err := json.Marshal(site.Aliases)
	if err != nil {
		return 0, err
	}

	password := ""
	if site.WPAdminPassword != "" {
		if password, err = s.encrypt(site.WPAdminPassword); err != nil {
			return 0, err
		}
	}

	// The repository credential, where the caller set one — which a staging
	// copy does, so it inherits production's access to the same repository.
	repoToken := ""

	if site.RepoToken != "" {
		if repoToken, err = s.encrypt(site.RepoToken); err != nil {
			return 0, err
		}
	}

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO sites (user_id, dns_account_id, database_id, domain, type, aliases,
			root_path, web_directory, php_version, node_version, status, ssl,
			app_port, start_command, build_command, repository, branch,
			wp_admin_user, wp_admin_password, wp_admin_email, wp_title,
			manage_dns, dns_type, dns_content, dns_proxied, staging_of,
			repo_auth, repo_token)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		         ?, ?)`,
		nullID(site.UserID), nullID(site.DNSAccountID), nullID(site.DatabaseID),
		strings.ToLower(site.Domain), site.Type, string(aliases),
		site.RootPath, site.WebDirectory, site.PHPVersion, site.NodeVersion,
		site.SSL, nullID(int64(site.AppPort)), site.StartCommand, site.BuildCommand,
		site.Repository, site.Branch,
		site.WPAdminUser, password, site.WPAdminEmail, site.WPTitle,
		site.ManageDNS, site.DNSType, site.DNSContent, site.DNSProxied,
		nullID(site.StagingOf), nullText(site.RepoAuth), nullText(repoToken))
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// StagingSitesFor is the staging copies of one site, newest first.
func (s *Store) StagingSitesFor(ctx context.Context, siteID int64) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites
		  WHERE staging_of = ? AND deleted_at IS NULL ORDER BY id DESC`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []*Site

	for rows.Next() {
		site, err := s.scanSite(rows)
		if err != nil {
			return nil, err
		}

		sites = append(sites, site)
	}

	return sites, rows.Err()
}

// SetSiteStatus records where a site got to. An empty error clears the last
// one, so a successful redeploy does not leave the previous failure on screen.
func (s *Store) SetSiteStatus(id int64, status, lastError string) {
	s.db.Exec(
		`UPDATE sites SET status = ?, last_error = ?, updated_at = datetime('now') WHERE id = ?`,
		status, nullText(lastError), id)
}

// SetSiteSSL records that a certificate was issued.
func (s *Store) SetSiteSSL(id int64, ssl bool) {
	s.db.Exec(`UPDATE sites SET ssl = ?, updated_at = datetime('now') WHERE id = ?`, ssl, id)
}

// SetSiteTLSMode records who serves this site's certificate.
func (s *Store) SetSiteTLSMode(id int64, mode string) {
	s.db.Exec(`UPDATE sites SET tls_mode = ?, updated_at = datetime('now') WHERE id = ?`,
		nullText(mode), id)
}

// SetSiteDatabase links the database provisioned for a site.
func (s *Store) SetSiteDatabase(id, databaseID int64) {
	s.db.Exec(`UPDATE sites SET database_id = ?, updated_at = datetime('now') WHERE id = ?`,
		nullID(databaseID), id)
}

// SetSiteDNS records what was published, so deleting the site can take it back
// down again.
func (s *Store) SetSiteDNS(id int64, zoneID, recordIDs string) {
	s.db.Exec(`UPDATE sites SET dns_zone_id = ?, dns_record_ids = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(zoneID), nullText(recordIDs), id)
}

// SetSiteAppPort records the port the application is actually listening on.
//
// The panel hands a proxied app a port and expects it to be used. An app that
// takes its own default instead is not going to be argued out of it, and the
// vhost is written after the app has started — so the honest thing is to
// follow it rather than to serve a 502 with the port the panel preferred.
// SetSitePHPSettings stores the per-site php.ini overrides. The vhost is
// rewritten from them, so this is only half the change.
func (s *Store) SetSitePHPSettings(id int64, encoded string) {
	s.db.Exec(`UPDATE sites SET php_settings = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(encoded), id)
}

// SetSiteCache stores which cache the site was given and how long a page
// lives. The vhost, the conf.d zone and — for Redis — the site's own
// configuration are written from it, so this is only part of the change.
func (s *Store) SetSiteCache(id int64, mode, ttl string) {
	s.db.Exec(`UPDATE sites SET cache_mode = ?, cache_enabled = ?, cache_ttl = ?,
	           updated_at = datetime('now') WHERE id = ?`,
		mode, mode == "nginx", nullText(ttl), id)
}

// SetSiteAliases replaces the extra hostnames a site answers on. The vhost,
// the certificate and the published DNS records are all written from
// Hostnames(), so this is the row half of the change and never the whole of it.
func (s *Store) SetSiteAliases(id int64, aliases []string) error {
	if aliases == nil {
		aliases = []string{}
	}

	encoded, err := json.Marshal(aliases)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE sites SET aliases = ?, updated_at = datetime('now') WHERE id = ?`,
		string(encoded), id)

	return err
}

// SetSitePHPVersion moves a site to another PHP-FPM. The vhost names the
// socket, so this is the row half of the change and never the whole of it.
func (s *Store) SetSitePHPVersion(id int64, version string) {
	s.db.Exec(`UPDATE sites SET php_version = ?, updated_at = datetime('now')
	           WHERE id = ?`, version, id)
}

// SetSiteWebDirectory moves the document root inside the site directory.
func (s *Store) SetSiteWebDirectory(id int64, directory string) {
	s.db.Exec(`UPDATE sites SET web_directory = ?, updated_at = datetime('now')
	           WHERE id = ?`, directory, id)
}

func (s *Store) SetSiteAppPort(id int64, port int) {
	s.db.Exec(`UPDATE sites SET app_port = ?, updated_at = datetime('now') WHERE id = ?`,
		nullID(int64(port)), id)
}

// SetSiteWPPassword stores a generated WordPress password.
func (s *Store) SetSiteWPPassword(id int64, password string) error {
	encrypted, err := s.encrypt(password)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE sites SET wp_admin_password = ?, updated_at = datetime('now') WHERE id = ?`,
		encrypted, id)

	return err
}

// MarkSiteDeleted hides a site immediately.
//
// Deleting a site means removing a vhost, a systemd unit, DNS records at a
// provider, a database and a directory tree, and the operator has already
// decided all of that. Waiting for it before the row disappears left the site
// sitting in the list marked "deleting" for however long the provider took to
// answer. The row is stamped here and hidden; the worker still has everything
// it needs to take the machine apart, and removes the row when it is done.
func (s *Store) MarkSiteDeleted(id int64) {
	s.db.Exec(`UPDATE sites SET deleted_at = datetime('now'), status = 'deleting',
	           updated_at = datetime('now') WHERE id = ?`, id)
}

// DeleteSite removes the row once the teardown finished.
func (s *Store) DeleteSite(id int64) {
	s.db.Exec(`DELETE FROM sites WHERE id = ?`, id)
}

// NextFreePort hands out a local port for a node-family app.
//
// A port stays taken while its site is being torn down: the service is still
// running until the worker gets to it.
func (s *Store) NextFreePort(ctx context.Context) int {
	taken := map[int]bool{}

	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT app_port FROM sites WHERE app_port IS NOT NULL`)
	if err == nil {
		defer rows.Close()

		for rows.Next() {
			var port int
			if rows.Scan(&port) == nil {
				taken[port] = true
			}
		}
	}

	for port := config.AppPortMin; port <= config.AppPortMax; port++ {
		if !taken[port] {
			return port
		}
	}

	return config.AppPortMin
}

// ---------------------------------------------------------------- derived

// DocumentRoot is what nginx serves from.
func (site *Site) DocumentRoot() string {
	root := strings.TrimRight(site.RootPath, "/")
	web := site.WebDirectory

	if web == "" || web == "/" {
		return root
	}

	if !strings.HasPrefix(web, "/") {
		web = "/" + web
	}

	return root + web
}

// ServiceName is the systemd unit a proxied app runs as.
func (site *Site) ServiceName() string {
	return "ubuntu-panel-" + strings.ReplaceAll(site.Domain, ".", "-")
}

// URL is where the site answers.
//
// A site whose TLS ends at its DNS provider has no certificate here and is
// still an https address: the visitor's connection is encrypted to the proxy,
// and it is the visitor's address that goes into a link, a WordPress option or
// an OAuth callback.
func (site *Site) URL() string {
	if site.SSL || site.EdgeTLS() {
		return "https://" + site.Domain
	}

	return "http://" + site.Domain
}

// EdgeTLS reports whether the certificate a visitor sees is the DNS provider's
// rather than this machine's.
func (site *Site) EdgeTLS() bool { return site.TLSMode == "edge" }

// TLSModeOrDefault is the mode with the empty value spelled out, because every
// site has one and "" is not a word the pages can print.
func (site *Site) TLSModeOrDefault() string {
	if site.TLSMode == "" {
		return "letsencrypt"
	}

	return site.TLSMode
}

// TypeLabel, IsProxied, IsPHP and NeedsDatabase read the site type table.
func (site *Site) TypeLabel() string {
	if kind := config.FindSiteType(site.Type); kind != nil {
		return kind.Label
	}

	return site.Type
}

func (site *Site) IsProxied() bool {
	kind := config.FindSiteType(site.Type)

	return kind != nil && kind.Proxied
}

func (site *Site) IsPHP() bool {
	kind := config.FindSiteType(site.Type)

	return kind != nil && kind.Runtime == "php"
}

func (site *Site) NeedsDatabase() bool {
	kind := config.FindSiteType(site.Type)

	return kind != nil && kind.Database
}

// IsStaging reports whether this site is a staging copy of another.
func (site *Site) IsStaging() bool { return site.StagingOf > 0 }

// IsManaged reports whether the panel installs the application itself, rather
// than deploying whatever the operator's repository contains.
func (site *Site) IsManaged() bool {
	kind := config.FindSiteType(site.Type)

	return kind != nil && kind.Managed
}

// HasAdminAccount reports whether the deployment creates a login for the
// application, which the site page then has to show.
func (site *Site) HasAdminAccount() bool {
	kind := config.FindSiteType(site.Type)

	return kind != nil && kind.AdminAccount
}

// NodeMinimum is the Node.js major this site's type refuses to run below.
func (site *Site) NodeMinimum() int {
	kind := config.FindSiteType(site.Type)
	if kind == nil {
		return 0
	}

	return kind.NodeMinimum
}

// AdminURL is where the button on the site page goes.
//
// Where somebody wants to arrive, which is not always the administration. A
// WordPress site opens at wp-admin, because that is what a WordPress login is
// for. Nextcloud opens at its front door: it used to open at /settings/admin,
// the server-administration screen, which is a page for changing how the
// server is set up rather than for using it — Nextcloud sends a login on to
// the dashboard by itself, and that is where anybody clicking Open Nextcloud
// meant to go.
func (site *Site) AdminURL() string {
	switch site.Type {
	case "wordpress":
		return site.URL() + "/wp-admin"
	default:
		return site.URL()
	}
}

// AdminLabel names the credentials on the site page.
func (site *Site) AdminLabel() string {
	return site.TypeLabel() + " administrator"
}

// Hostnames is every name the site answers on, space-separated, for the nginx
// server_name line and for the certificate request.
//
// An alias equal to the domain would make nginx refuse the vhost as a
// conflicting server name.
func (site *Site) Hostnames() []string {
	names := []string{site.Domain}

	for _, alias := range site.Aliases {
		alias = strings.TrimSpace(strings.ToLower(alias))

		if alias == "" || alias == site.Domain {
			continue
		}

		names = append(names, alias)
	}

	return names
}

// PHPOverrides is PHPSettings decoded. An unreadable value is no overrides
// rather than an error: it is a vhost being rendered, and a site that serves
// with PHP's defaults is better than one that does not serve.
func (site *Site) PHPOverrides() map[string]string {
	settings := map[string]string{}

	if strings.TrimSpace(site.PHPSettings) != "" {
		json.Unmarshal([]byte(site.PHPSettings), &settings)
	}

	return settings
}

// RecordType and RecordContent are the record this site needs at its name: the
// one the panel publishes when it holds the zone, and the one it asks the
// operator for when it does not.
//
// Both are here rather than at either caller because the sentence the site page
// prints, the sentence the deployment writes into its console and the record
// create_site hands back to an assistant all have to be the same record. They
// were three separate guesses at it, and a panel that suggests a record other
// than the one it would have written is a panel arguing with itself.
func (site *Site) RecordType() string {
	if kind := strings.ToUpper(strings.TrimSpace(site.DNSType)); kind != "" {
		return kind
	}

	return "A"
}

// RecordContent is what that record points at. Empty means the panel never
// recorded one, and the caller — which knows this machine's public address —
// fills it in.
func (site *Site) RecordContent() string {
	return strings.TrimSpace(site.DNSContent)
}

// RecordIDs is the map of published DNS records, by fully qualified name.
func (site *Site) RecordIDs() map[string]string {
	ids := map[string]string{}

	if strings.TrimSpace(site.DNSRecordIDs) != "" {
		json.Unmarshal([]byte(site.DNSRecordIDs), &ids)
	}

	return ids
}

// Summary is the shape the Vue pages read. `detailed` adds what only the site
// page shows.
func (site *Site) Summary(detailed bool) map[string]any {
	aliases := site.Aliases
	if aliases == nil {
		aliases = []string{}
	}

	summary := map[string]any{
		"id":            site.ID,
		"domain":        site.Domain,
		"url":           site.URL(),
		"type":          site.Type,
		"type_label":    site.TypeLabel(),
		"aliases":       aliases,
		"status":        site.Status,
		"ssl":           site.SSL,
		"tls_mode":      site.TLSModeOrDefault(),
		"edge_tls":      site.EdgeTLS(),
		"php_version":   site.PHPVersion,
		"app_port":      nullableInt(site.AppPort),
		"root_path":     site.RootPath,
		"document_root": site.DocumentRoot(),
		"manage_dns":    site.ManageDNS,
		"repository":    site.Repository,
		"branch":        site.Branch,
		"is_proxied":    site.IsProxied(),
		// Whether PHP-FPM is what serves this site, which is what decides
		// whether a PHP version is a thing it has. `is_proxied` is not the
		// opposite of it: a static site is neither.
		"is_php": site.IsPHP(),
		// The list needs these to offer a way into the application without
		// opening the site's own page first: whether this type has a login the
		// panel knows about, and where that login lives.
		"managed":    site.IsManaged(),
		"admin_url":  site.AdminURL(),
		"last_error": site.LastError,
		"created_at": site.CreatedAt,

		// Which half of a production/staging pair this is. On the list as well
		// as the site page, because a staging site among a page of live ones
		// needs saying — it is the row somebody would otherwise point a
		// customer at.
		"is_staging": site.IsStaging(),
		"staging_of": nullableInt64(site.StagingOf),
	}

	if detailed {
		serviceName := any(nil)
		if site.IsProxied() {
			serviceName = site.ServiceName()
		}

		summary["dns_type"] = site.DNSType
		summary["dns_content"] = site.DNSContent
		summary["dns_proxied"] = site.DNSProxied
		summary["dns_zone_id"] = site.DNSZoneID
		summary["start_command"] = site.StartCommand
		summary["build_command"] = site.BuildCommand
		summary["service_name"] = serviceName
		summary["web_directory"] = site.WebDirectory
		summary["cache_mode"] = site.CacheMode
		summary["cache_enabled"] = site.CacheEnabled
		summary["cache_ttl"] = site.CacheTTL
		summary["wp_admin_user"] = site.WPAdminUser
		summary["wp_admin_email"] = site.WPAdminEmail

		// The application's own credentials, which the panel generated and
		// nobody else has. The site page is the only place they exist.
		if site.HasAdminAccount() && site.WPAdminPassword != "" {
			app := map[string]any{
				"label":    site.AdminLabel(),
				"url":      site.AdminURL(),
				"user":     site.WPAdminUser,
				"password": site.WPAdminPassword,
			}

			summary["app"] = app
		}
	}

	return summary
}

func nullableInt(value int) any {
	if value <= 0 {
		return nil
	}

	return value
}

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}

	return value
}

func decodeStringList(encoded string) []string {
	var list []string

	if strings.TrimSpace(encoded) != "" {
		json.Unmarshal([]byte(encoded), &list)
	}

	if list == nil {
		return []string{}
	}

	return list
}

func nullText(value string) any {
	if value == "" {
		return nil
	}

	return value
}

// ---------------------------------------------------------- git deployment

// SetSiteRepository changes where a site is deployed from.
//
// Settable after creation on purpose. A repository typed wrong used to mean
// deleting the site and making it again, which takes the database and the
// certificate with it — and a URL is the single most likely thing on the create
// form to be wrong, because it is the one field nobody can check by reading it.
func (s *Store) SetSiteRepository(id int64, repository, branch string) {
	s.db.Exec(`UPDATE sites SET repository = ?, branch = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(repository), branch, id)
}

// SetSiteRepoAuth stores how the panel authenticates to the remote.
//
// An empty token means "keep the one already stored", which is what the form
// sends when the field was not touched — the browser never receives it back, so
// it cannot send it again.
func (s *Store) SetSiteRepoAuth(id int64, method, token string) error {
	if token == "" {
		_, err := s.db.Exec(`UPDATE sites SET repo_auth = ?, updated_at = datetime('now')
		                     WHERE id = ?`, nullText(method), id)

		return err
	}

	encrypted, err := s.encrypt(token)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE sites SET repo_auth = ?, repo_token = ?,
	                    updated_at = datetime('now') WHERE id = ?`,
		nullText(method), nullText(encrypted), id)

	return err
}

// SetSiteDeployKey stores the public half of the key the panel generated.
func (s *Store) SetSiteDeployKey(id int64, publicKey string) {
	s.db.Exec(`UPDATE sites SET repo_deploy_key = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(publicKey), id)
}

// SetSiteWebhookSecret stores what a pushed webhook is signed with.
func (s *Store) SetSiteWebhookSecret(id int64, secret string) error {
	if secret == "" {
		_, err := s.db.Exec(`UPDATE sites SET webhook_secret = NULL,
		                     updated_at = datetime('now') WHERE id = ?`, id)

		return err
	}

	encrypted, err := s.encrypt(secret)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`UPDATE sites SET webhook_secret = ?, updated_at = datetime('now')
	                    WHERE id = ?`, nullText(encrypted), id)

	return err
}

// SetSiteAutoPull stores the polling interval and when it is next due.
func (s *Store) SetSiteAutoPull(id int64, schedule string, nextAt int64) {
	s.db.Exec(`UPDATE sites SET auto_pull = ?, auto_pull_next_at = ?,
	           updated_at = datetime('now') WHERE id = ?`, nullText(schedule), nextAt, id)
}

// SetSiteAutoPullNext moves the due time on without touching the interval.
func (s *Store) SetSiteAutoPullNext(id, nextAt int64) {
	s.db.Exec(`UPDATE sites SET auto_pull_next_at = ? WHERE id = ?`, nextAt, id)
}

// SetSiteLastCommit records the commit the site is now serving.
func (s *Store) SetSiteLastCommit(id int64, commit string) {
	s.db.Exec(`UPDATE sites SET last_commit = ?, updated_at = datetime('now')
	           WHERE id = ?`, nullText(commit), id)
}

// DuePulls is every live site whose polled pull is due.
//
// The same arrangement as DueBackups, and for the same reason: the panel's own
// ticker rather than cron, so a machine that was switched off catches up as
// soon as it is back instead of waiting for the next window.
func (s *Store) DuePulls(ctx context.Context, now time.Time) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctxOrBackground(ctx),
		`SELECT `+siteColumns+` FROM sites
		 WHERE deleted_at IS NULL AND repository IS NOT NULL AND repository != ''
		   AND auto_pull IS NOT NULL AND auto_pull != '' AND auto_pull != 'off'
		   AND auto_pull_next_at > 0 AND auto_pull_next_at <= ?
		 ORDER BY id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var due []*Site

	for rows.Next() {
		site, err := s.scanSite(rows)
		if err != nil {
			return nil, err
		}

		due = append(due, site)
	}

	return due, rows.Err()
}
