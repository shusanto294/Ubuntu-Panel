package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
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
	// administrator and OpenClaw's gateway token live in the same three
	// columns rather than in three more of their own.
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
	php_settings, created_at`

type siteScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanSite(row siteScanner) (*Site, error) {
	site := &Site{}

	var dnsAccountID, databaseID, userID, appPort nullInt
	var aliases, nodeVersion, lastError, startCommand, buildCommand text
	var repository, wpUser, wpPassword, wpEmail, wpTitle text
	var dnsZoneID, dnsRecordIDs, dnsContent, phpSettings text

	err := row.Scan(&site.ID, &userID, &dnsAccountID, &databaseID, &site.Domain,
		&site.Type, &aliases, &site.RootPath, &site.WebDirectory, &site.PHPVersion,
		&nodeVersion, &site.Status, &site.SSL, &lastError, &appPort,
		&startCommand, &buildCommand, &repository, &site.Branch,
		&wpUser, &wpPassword, &wpEmail, &wpTitle,
		&site.ManageDNS, &dnsZoneID, &dnsRecordIDs, &site.DNSType, &dnsContent,
		&site.DNSProxied, &phpSettings, &site.CreatedAt)
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

	result, err := s.db.ExecContext(ctxOrBackground(ctx),
		`INSERT INTO sites (user_id, dns_account_id, database_id, domain, type, aliases,
			root_path, web_directory, php_version, node_version, status, ssl,
			app_port, start_command, build_command, repository, branch,
			wp_admin_user, wp_admin_password, wp_admin_email, wp_title,
			manage_dns, dns_type, dns_content, dns_proxied)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(site.UserID), nullID(site.DNSAccountID), nullID(site.DatabaseID),
		strings.ToLower(site.Domain), site.Type, string(aliases),
		site.RootPath, site.WebDirectory, site.PHPVersion, site.NodeVersion,
		site.SSL, nullID(int64(site.AppPort)), site.StartCommand, site.BuildCommand,
		site.Repository, site.Branch,
		site.WPAdminUser, password, site.WPAdminEmail, site.WPTitle,
		site.ManageDNS, site.DNSType, site.DNSContent, site.DNSProxied)
	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
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
func (site *Site) URL() string {
	if site.SSL {
		return "https://" + site.Domain
	}

	return "http://" + site.Domain
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

// AdminLabel names the credentials on the site page. OpenClaw's gateway takes
// a shared secret rather than a username, so calling it a password would be
// telling the operator to look for a login form that is not there.
func (site *Site) AdminLabel() string {
	switch site.Type {
	case "openclaw":
		return "OpenClaw login"
	default:
		return site.TypeLabel() + " administrator"
	}
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
		"php_version":   site.PHPVersion,
		"app_port":      nullableInt(site.AppPort),
		"root_path":     site.RootPath,
		"document_root": site.DocumentRoot(),
		"manage_dns":    site.ManageDNS,
		"repository":    site.Repository,
		"branch":        site.Branch,
		"is_proxied":    site.IsProxied(),
		"last_error":    site.LastError,
		"created_at":    site.CreatedAt,
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
		summary["wp_admin_user"] = site.WPAdminUser
		summary["wp_admin_email"] = site.WPAdminEmail
		summary["managed"] = site.IsManaged()

		// The application's own credentials, which the panel generated and
		// nobody else has. The site page is the only place they exist.
		if site.HasAdminAccount() && site.WPAdminPassword != "" {
			app := map[string]any{
				"label":    site.AdminLabel(),
				"url":      site.AdminURL(),
				"user":     site.WPAdminUser,
				"password": site.WPAdminPassword,
				"token":    false,
			}

			// OpenClaw's dashboard asks for the gateway's WebSocket address as
			// well as the token, and gets it wrong on its own: it offers `ws://`
			// even on a page served over HTTPS, which the browser then refuses
			// to open as mixed content. Handing over the right one is the
			// difference between a dashboard that connects and a token that
			// looks like it was rejected.
			if site.Type == "openclaw" {
				scheme := "ws"
				if site.SSL {
					scheme = "wss"
				}

				app["websocket"] = scheme + "://" + site.Domain
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
