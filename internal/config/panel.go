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
	WebDirectory string `json:"web_directory"` // relative, no leading slash
	Runtime      string `json:"runtime"`       // php | node | static
	Database     bool   `json:"database"`      // gets an application database
	Proxied      bool   `json:"proxied"`       // served through nginx to a long-running process
	Build        string `json:"build"`
	Start        string `json:"start"`
}

// SiteTypes is the list, in the order the create form shows them.
var SiteTypes = []SiteType{
	{"wordpress", "WordPress", "", "php", true, false, "", ""},
	{"laravel", "Laravel", "public", "php", true, false, "", ""},
	{"php", "PHP", "public", "php", false, false, "", ""},
	{"nodejs", "Node.js", "", "node", false, true, "", "npm run start"},
	{"nextjs", "Next.js", "", "node", false, true, "npm run build", "npm run start"},
	{"static", "Static HTML", "", "static", false, false, "", ""},
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
