package notfound

import "strings"

// A 404 for one of these is not somebody following a broken link: no page of a
// real site links to them, and a visitor has no reason to type them. They are
// what a scanner asks every server on the internet for — secrets left in the
// web root, backups, admin tools and known-vulnerable files — so a single one
// is enough to act on. Everything else needs the threshold of 404s.
//
// Kept narrow on purpose. A wrong entry here challenges a real visitor on
// every domain, which is the one thing 404 protection must not do.

// suspiciousSegments are path segments that start a hidden file or folder no
// site serves.
var suspiciousSegments = []string{
	".env", ".git", ".svn", ".hg", ".aws", ".ssh", ".docker", ".htpasswd",
	".ds_store", ".vscode", ".idea", ".config",
}

// suspiciousParts are anywhere in the path.
var suspiciousParts = []string{
	"wp-config", "/phpmyadmin", "/pma/", "/myadmin", "/adminer", "/phpinfo",
	"/server-status", "/vendor/phpunit", "/cgi-bin/", "/eval-stdin", "/boaform",
	"/shell.php", "/wso.php", "/c99.php", "/r57.php", "/alfa.php", "/xleet",
	"/actuator/", "/info.php", "/test.php",
}

// suspiciousPHPDirs hold no PHP a visitor ever requests by name: a .php file
// asked for in one, and missing, is a scanner looking for a web shell it hopes
// was uploaded there.
var suspiciousPHPDirs = []string{"/wp-content/", "/wp-includes/", "/uploads/", "/images/", "/assets/"}

// suspiciousEndings are the dumps and editor backups a web root should never
// have.
var suspiciousEndings = []string{
	".sql", ".sql.gz", ".sql.zip", ".bak", ".old", ".orig", ".save", ".swp", "~",
	".php.txt", ".env.save",
}

// Suspicious is whether a path that was answered with a 404 was a scanner's.
func Suspicious(path string) bool {
	path = strings.ToLower(path)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}

	for _, segment := range strings.Split(path, "/") {
		for _, hidden := range suspiciousSegments {
			if segment == hidden || strings.HasPrefix(segment, hidden+".") || strings.HasPrefix(segment, hidden+"-") {
				return true
			}
		}
	}

	for _, part := range suspiciousParts {
		if strings.Contains(path, part) {
			return true
		}
	}

	if strings.HasSuffix(path, ".php") {
		for _, dir := range suspiciousPHPDirs {
			if strings.Contains(path, dir) {
				return true
			}
		}
	}

	for _, ending := range suspiciousEndings {
		if strings.HasSuffix(path, ending) {
			return true
		}
	}

	return false
}
