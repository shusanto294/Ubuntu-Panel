package weblog

import (
	"regexp"
	"strings"
)

// The scanner protection's default lists, in the pattern language of
// security/paths.go: text matched anywhere in the path, ignoring case; * any
// run of characters; $ at the end, the path ends there.

// DefaultSecretPaths are files that hold passwords and keys — and that no
// visitor, no search engine and no application ever asks a web server for.
// One request is an attacker looking for a leak, whatever the answer was.
var DefaultSecretPaths = []string{
	"/.env", "/.git/", "/.git-credentials", "/.gitconfig", "/.aws/", "/.ssh/", "/.docker/",
	"/.htpasswd", "/.npmrc", "/.pgpass",
	"/wp-config.php.", "/wp-config.bak", "/wp-config.old", "/wp-config.txt", "/wp-config.save",
	"/wp-config.orig", "/sftp-config.json",
	// WordPress's installer. The panel installs WordPress from the command
	// line, so nobody opens these — except a bot hoping to find a WordPress
	// nobody finished setting up, and take it over.
	"/wp-admin/install.php", "/wp-admin/setup-config.php",
}

// DefaultScanPaths are files only something looking for a weakness asks for,
// counted when refused, missing or redirected.
var DefaultScanPaths = []string{
	"/.svn/", "/.hg/", "/.ds_store", "/.github/",
	"/vendor/phpunit", "eval-stdin.php", "/cgi-bin/", "/boaform", "/hnap1",
	"/actuator/", "/solr/", "/manager/html", "/telescope/", "/_ignition/", "/server-status",
	"/phpinfo.php", ".sql$", ".sql.gz$", ".sql.zip$", "/wp-content/debug.log",
	"/shell.php", "/wso.php", "/c99.php", "/r57.php", "/alfa.php", "/xleet.php",
	"../",
}

// PatternRegexp is a pattern as a regular expression for a path that is
// already lower case and percent-decoded (decodePath).
func PatternRegexp(pattern string) string {
	var re strings.Builder

	for _, r := range strings.ToLower(pattern) {
		switch r {
		case '*':
			re.WriteString(`[^?]*`)
		case '$':
			re.WriteString(`$`)
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}

	return re.String()
}
