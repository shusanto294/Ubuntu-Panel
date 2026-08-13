package config

// The tables that describe what the panel can host. They are the Go form of
// the C panel's config.c, which was the C form of config/panel.php — the same
// keys and labels, because the Vue pages render them.

// PHPVersions are the versions the panel can install and assign to sites,
// newest first.
var PHPVersions = []string{"8.5", "8.4", "8.3", "8.2", "8.1"}

// NodeVersions are the Node.js majors offered when provisioning.
var NodeVersions = []string{"22", "20", "18"}

// SiteDNSTypes are what the site form offers for its own record.
var SiteDNSTypes = []string{"A", "AAAA", "CNAME"}

// DNSRecordTypes are what the zone editor offers.
var DNSRecordTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "NS", "SRV", "CAA"}

// The port range handed out to node-family apps behind the nginx reverse proxy.
const (
	AppPortMin = 3000
	AppPortMax = 3999
)

// Engine is a database engine the panel can create databases on.
type Engine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// Engines is the list.
//
// PostgreSQL is still named here and no longer in the catalogue: nothing this
// panel provisions uses it, so it cannot be installed — but a database created
// against it before that stays in the table, and this is what gives that row a
// label instead of the bare key.
var Engines = []Engine{
	{"mysql", "MariaDB / MySQL", 3306},
	{"postgres", "PostgreSQL", 5432},
	{"mongodb", "MongoDB", 27017},
}

// SiteType is a kind of site the panel knows how to deploy.
type SiteType struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Description  string `json:"description"` // one line, shown on the create form
	WebDirectory string `json:"web_directory"`
	Runtime      string `json:"runtime"`  // php | node | static
	Database     bool   `json:"database"` // gets an application database
	Proxied      bool   `json:"proxied"`  // served through nginx to a long-running process
	Build        string `json:"build"`
	Start        string `json:"start"`

	// Managed is an application the panel installs and configures itself, so
	// the create form has no repository to ask for and the deployment has an
	// admin account to hand back. WordPress was the only one; Nextcloud, n8n
	// and OpenClaw are the same arrangement.
	Managed bool `json:"managed"`

	// AdminAccount is whether that application has a login the panel creates —
	// which decides whether the form asks for a username and password, and
	// whether the site page has credentials to show.
	AdminAccount bool `json:"admin_account"`

	// NodeMinimum is the Node.js major this type refuses to run below. The
	// deployment upgrades Node rather than failing halfway through an install
	// that was never going to work.
	NodeMinimum int `json:"node_minimum"`
}

// SiteTypes is the list, in the order the create form shows them.
var SiteTypes = []SiteType{
	{
		Key: "wordpress", Label: "WordPress",
		Description: "PHP-FPM via nginx · MariaDB · installed and configured",
		Runtime:     "php", Database: true, Managed: true, AdminAccount: true,
	},
	{
		Key: "laravel", Label: "Laravel", WebDirectory: "public",
		Description: "PHP-FPM via nginx · MariaDB · Composer",
		Runtime:     "php", Database: true,
	},
	{
		Key: "nextcloud", Label: "Nextcloud", WebDirectory: "public",
		Description: "PHP-FPM via nginx · MariaDB · files, calendar and contacts",
		Runtime:     "php", Database: true, Managed: true, AdminAccount: true,
	},
	{
		Key: "n8n", Label: "n8n",
		Description: "Workflow automation · systemd service behind nginx",
		Runtime:     "node", Proxied: true, Managed: true,
		Start: "n8n start", NodeMinimum: 20,
	},
	{
		Key: "openclaw", Label: "OpenClaw",
		Description: "Self-hosted AI agent gateway · systemd service behind nginx",
		Runtime:     "node", Proxied: true, Managed: true, AdminAccount: true,
		// 24 rather than the 22.22.3 OpenClaw's own notes allow: the panel
		// checks the major version only, and a machine on 22.5 would pass a
		// check for 22 and then be refused by OpenClaw itself.
		// `gateway run` rather than `gateway`: run is the foreground form, and
		// a systemd unit of Type=simple wants the process it started to be the
		// gateway rather than something that goes away having arranged for one
		// elsewhere. It is also what openclaw itself suggests — "run `openclaw
		// gateway run` for a foreground gateway".
		Start: "openclaw gateway run", NodeMinimum: 24,
	},
	{
		Key: "php", Label: "PHP", WebDirectory: "public",
		Description: "PHP-FPM via nginx",
		Runtime:     "php",
	},
	{
		Key: "nodejs", Label: "Node.js",
		Description: "systemd service behind nginx",
		Runtime:     "node", Proxied: true, Start: "npm run start",
	},
	{
		Key: "nextjs", Label: "Next.js",
		Description: "systemd service behind nginx · built on deploy",
		Runtime:     "node", Proxied: true,
		Build: "npm run build", Start: "npm run start",
	},
	{
		Key: "static", Label: "Static HTML",
		Description: "served straight from disk",
		Runtime:     "static",
	},
}

// FindSiteType returns the type with this key, or nil.
func FindSiteType(key string) *SiteType {
	for i := range SiteTypes {
		if SiteTypes[i].Key == key {
			return &SiteTypes[i]
		}
	}

	return nil
}

// FindEngine returns the engine with this key, or nil.
func FindEngine(key string) *Engine {
	for i := range Engines {
		if Engines[i].Key == key {
			return &Engines[i]
		}
	}

	return nil
}

// EngineLabel is the engine's name, falling back to the bare key for one the
// panel no longer offers.
func EngineLabel(key string) string {
	if engine := FindEngine(key); engine != nil {
		return engine.Label
	}

	return key
}

// IsPHPVersion, IsNodeVersion and IsDNSRecordType are the validators the
// handlers run before anything reaches a command line.
func IsPHPVersion(version string) bool  { return contains(PHPVersions, version) }
func IsNodeVersion(version string) bool { return contains(NodeVersions, version) }
func IsDNSRecordType(kind string) bool  { return contains(DNSRecordTypes, kind) }
func IsSiteDNSType(kind string) bool    { return contains(SiteDNSTypes, kind) }

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}
