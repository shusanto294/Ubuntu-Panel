package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func fileSite() *store.Site {
	return &store.Site{ID: 9, Domain: "files.example.com", Type: "php",
		RootPath: "/var/www/files.example.com"}
}

// The file manager takes a path from a browser, so every way out of the site
// directory has to be closed before the path reaches a command line.
//
// A traversal is contained rather than refused: the path is cleaned as if the
// site root were `/`, so `../../etc/shadow` becomes `/etc/shadow` inside the
// site and points at a file that is almost certainly not there. That is the
// standard rooted-clean, and what matters is the property asserted here —
// whatever comes back is under the site root.
func TestFileManagerKeepsEveryPathInsideTheSite(t *testing.T) {
	site := fileSite()
	root := site.RootPath

	for _, attempt := range []string{
		"../../etc/shadow",
		"/../../etc/shadow",
		"public/../../../etc/passwd",
		"..",
		"../",
		"./../../",
		"/etc/passwd",
		"wp-content/../../../../root/.ssh/id_rsa",
	} {
		got, err := resolve(site, attempt)
		if err != nil {
			continue
		}

		if got != root && !strings.HasPrefix(got, root+"/") {
			t.Errorf("%q resolved to %q, which is outside %q", attempt, got, root)
		}
	}
}

// The two that are refused outright, because neither is a path anybody meant
// to type: a null byte truncates the string for anything written in C, and a
// backslash is a separator in half the archives a site is unpacked from.
func TestFileManagerRefusesPathsItCannotReasonAbout(t *testing.T) {
	site := fileSite()

	for _, attempt := range []string{"\x00/etc/passwd", "public\\..\\..\\etc"} {
		if _, err := resolve(site, attempt); err == nil {
			t.Errorf("%q was accepted", attempt)
		}
	}
}

// And the ordinary ones are not refused along with them.
func TestFileManagerAcceptsPathsInsideTheSite(t *testing.T) {
	site := fileSite()

	for path, want := range map[string]string{
		"":                    "/var/www/files.example.com",
		"/":                   "/var/www/files.example.com",
		"public":              "/var/www/files.example.com/public",
		"/public/index.php":   "/var/www/files.example.com/public/index.php",
		"public/./index.php":  "/var/www/files.example.com/public/index.php",
		"a/b/../c":            "/var/www/files.example.com/a/c",
		"wp-content/uploads/": "/var/www/files.example.com/wp-content/uploads",
	} {
		got, err := resolve(site, path)
		if err != nil {
			t.Errorf("%q was refused: %v", path, err)
			continue
		}

		if got != want {
			t.Errorf("%q resolved to %q, wanted %q", path, got, want)
		}
	}
}
