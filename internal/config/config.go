// Package config reads the panel's settings from a key=value file and the
// environment, in that order of increasing precedence.
//
// The file is the same /etc/ubuntu-panel/panel.conf the C panel used, with the
// same keys, so a machine can be switched between the two binaries without
// touching its configuration.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// Name is what the panel calls itself on screen.
	Name = "Ubuntu Panel"

	// Version is stamped into the page payload and reported by --version.
	// Bumped in the same commit as the tag that builds a release from it.
	Version = "2.5.47"
)

// Config is the whole of the panel's settings. Every field has a working
// default, so a panel with no configuration file at all still starts.
type Config struct {
	Root         string // Install root; the frontend and helper scripts live under it.
	PublicPath   string // Where public/build is, when not using the embedded copy.
	DatabasePath string // SQLite file.
	LogPath      string // Where the log is written, in addition to stderr.
	BindHost     string
	BindPort     int
	SitesRoot    string
	SystemUser   string
	Repository   string
	UpdateBranch string

	// AppKey is 32 random bytes, base64, and every stored credential is
	// encrypted with it. The installer writes it into panel.conf before the
	// first start: the panel cannot mint one for itself, because the config
	// file is not writable by the user the panel runs as, so a key it
	// generated would be a different key on every restart and would orphan
	// every encrypted column in the database.
	AppKey string

	// Workers is how many queue workers run deployments and installs.
	Workers int

	// InstallApps is what a first start puts on the machine by itself:
	// `all` (everything in the catalogue), `core` (only what a site cannot run
	// without), or `none`. It happens once, on a panel that has never installed
	// anything, so re-running it after somebody removed an app does not put it
	// back.
	InstallApps string

	// Debug serves the frontend from disk and logs every request. Off in
	// production, where the embedded build is both faster and immutable.
	Debug bool
}

// Load reads path (falling back to the standard location when empty), then
// overlays anything set in the environment.
func Load(path string) (*Config, error) {
	if path == "" {
		path = "/etc/ubuntu-panel/panel.conf"
	}

	file, err := readFile(path)
	if err != nil {
		return nil, err
	}

	// The environment wins over the file: a systemd drop-in or a one-off run
	// should not need the file edited.
	get := func(key, fallback string) string {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			return value
		}
		if value, ok := file[key]; ok && value != "" {
			return value
		}
		return fallback
	}

	config := &Config{
		Root:         get("PANEL_ROOT", "/opt/ubuntu-panel"),
		PublicPath:   get("PANEL_PUBLIC", ""),
		DatabasePath: get("PANEL_DATABASE", "/var/lib/ubuntu-panel/panel.db"),
		LogPath:      get("PANEL_LOG", "/var/log/ubuntu-panel/panel.log"),
		BindHost:     get("PANEL_BIND_HOST", "127.0.0.1"),
		SitesRoot:    get("PANEL_SITES_ROOT", "/var/www"),
		SystemUser:   get("PANEL_SYSTEM_USER", "ubuntupanel"),
		Repository:   get("PANEL_REPOSITORY", "https://github.com/shusanto294/Ubuntu-Panel-Go"),
		UpdateBranch: get("PANEL_UPDATE_BRANCH", "main"),
		AppKey:       get("PANEL_APP_KEY", ""),
		InstallApps:  strings.ToLower(get("PANEL_INSTALL_APPS", "all")),
		Debug:        truthy(get("PANEL_DEBUG", "")),
	}

	workers, err := strconv.Atoi(get("PANEL_WORKERS", "2"))
	if err != nil || workers < 1 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}
	config.Workers = workers

	if config.PublicPath == "" {
		config.PublicPath = filepath.Join(config.Root, "public")
	}

	port, err := strconv.Atoi(get("PANEL_BIND_PORT", "8088"))
	if err != nil || port <= 0 || port > 65535 {
		port = 8088
	}
	config.BindPort = port

	return config, nil
}

// Address is what the HTTP server listens on.
func (c *Config) Address() string {
	return c.BindHost + ":" + strconv.Itoa(c.BindPort)
}

// readFile parses a shell-style KEY=value file. A missing file is not an
// error — every key has a default, and the panel has to be able to start on a
// machine where nothing has been configured yet.
func readFile(path string) (map[string]string, error) {
	values := map[string]string{}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Values may be quoted, the way a shell would want them.
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}

		values[key] = value
	}

	return values, scanner.Err()
}

func truthy(value string) bool {
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
