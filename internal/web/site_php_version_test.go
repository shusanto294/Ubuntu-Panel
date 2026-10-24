package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// phpSite puts a site straight into the table. The create route would deploy
// it, and a deployment wants nginx, apt and a machine — none of which a test
// has, and none of which this is about.
func phpSite(t *testing.T, p *panel, kind, version string) int64 {
	t.Helper()

	id, err := p.db.CreateSite(context.Background(), &store.Site{
		Domain:     "php.example",
		Type:       kind,
		RootPath:   "/var/www/php.example",
		PHPVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// pageProps is one Inertia render's props, decoded.
func pageProps(t *testing.T, p *panel, path string) map[string]any {
	t.Helper()

	response := p.do(http.MethodGet, path, "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props map[string]any `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}

	return page.Props
}

// The PHP tab's version card is rendered from the page, so the page has to
// carry the choice: every version the panel installs, which of them this
// machine is running, and what a new site would get.
func TestTheSitePageCarriesThePHPVersionChoice(t *testing.T) {
	p := start(t)
	signIn(t, p)

	id := phpSite(t, p, "wordpress", "8.3")

	props := pageProps(t, p, "/sites/"+strconv.FormatInt(id, 10))

	offered, ok := props["phpVersions"].([]any)
	if !ok || len(offered) != len(config.PHPVersions) {
		t.Fatalf("the page does not offer the panel's PHP versions, got %v", props["phpVersions"])
	}

	if offered[0] != config.PHPVersions[0] {
		t.Errorf("the newest version is not first: %v", offered)
	}

	// Present even when empty: the card asks whether each option is installed,
	// and `undefined.includes(…)` takes the whole tab down with it.
	if _, ok := props["installedPhpVersions"].([]any); !ok {
		t.Errorf("installedPhpVersions is %v, want a list", props["installedPhpVersions"])
	}

	if props["defaultPhpVersion"] != p.db.PHPVersion() {
		t.Errorf("defaultPhpVersion is %v, want %q",
			props["defaultPhpVersion"], p.db.PHPVersion())
	}

	site, _ := props["site"].(map[string]any)

	if site["php_version"] != "8.3" {
		t.Errorf("the site's own version is %v, want 8.3", site["php_version"])
	}

	if site["is_php"] != true {
		t.Errorf("a WordPress site is not reported as running PHP: %v", site["is_php"])
	}
}

// A version the panel does not install never reaches a vhost. nginx takes a
// `fastcgi_pass` for a socket that is not there without complaining, and every
// request to the site is a 502 afterwards.
func TestTheSitePageRefusesAPHPVersionThePanelDoesNotInstall(t *testing.T) {
	p := start(t)
	signIn(t, p)

	id := phpSite(t, p, "wordpress", "8.3")

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/sites/"+strconv.FormatInt(id, 10)+"/php",
		`{"php_version":"5.6"}`, headers)
	response.Body.Close()

	site, err := p.db.FindSite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	if site.PHPVersion != "8.3" {
		t.Fatalf("the site was moved to PHP %s", site.PHPVersion)
	}
}

// The directives card and the version card post to the same endpoint, and a
// post carrying one must not disturb the other. Saving an upload limit used to
// be the only thing this route did; it still is, when that is what was sent.
func TestSavingThePHPDirectivesLeavesTheVersionAlone(t *testing.T) {
	p := start(t)
	signIn(t, p)

	id := phpSite(t, p, "wordpress", "8.3")

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/sites/"+strconv.FormatInt(id, 10)+"/php",
		`{"upload_max_filesize":"256M"}`, headers)
	response.Body.Close()

	site, err := p.db.FindSite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	if site.PHPVersion != "8.3" {
		t.Fatalf("saving a directive changed the version to %s", site.PHPVersion)
	}
}

// A static site is served off disk. Offering to move its PHP would be offering
// nothing, so the route says so rather than writing a version onto the row.
func TestAStaticSiteHasNoPHPVersionToSet(t *testing.T) {
	p := start(t)
	signIn(t, p)

	id := phpSite(t, p, "static", "")

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/sites/"+strconv.FormatInt(id, 10)+"/php",
		`{"php_version":"8.4"}`, headers)
	response.Body.Close()

	site, err := p.db.FindSite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	if site.PHPVersion != "" {
		t.Fatalf("a static site was given PHP %s", site.PHPVersion)
	}
}

// A version the panel installs but this machine is not running is not written
// onto the row and then hoped for: it goes on the queue, which installs it
// first and moves the site when PHP is actually answering on that socket.
func TestMovingToAPHPTheMachineLacksIsQueuedRatherThanWritten(t *testing.T) {
	p := start(t)
	signIn(t, p)

	id := phpSite(t, p, "wordpress", "8.3")

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	// The test machine has no PHP-FPM socket at all, so any version is one it
	// is not running.
	response := p.do(http.MethodPost, "/sites/"+strconv.FormatInt(id, 10)+"/php",
		`{"php_version":"8.4"}`, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST .../php = %d, want 303", response.StatusCode)
	}

	site, err := p.db.FindSite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	// Still on the old one: the vhost and the row move together, once the
	// socket is there. A row that ran ahead of the machine is a site answering
	// 502 to every request.
	if site.PHPVersion != "8.3" {
		t.Fatalf("the site was moved to PHP %s before PHP %s was installed",
			site.PHPVersion, "8.4")
	}
}
