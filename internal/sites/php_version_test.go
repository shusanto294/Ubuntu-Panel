package sites

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func phpVersionService(t *testing.T) *Service {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return New(st, nil)
}

func makeSite(t *testing.T, st *store.Store, kind, version string) int64 {
	t.Helper()

	id, err := st.CreateSite(context.Background(), &store.Site{
		Domain:     "example.com",
		Type:       kind,
		RootPath:   "/var/www/example.com",
		PHPVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// A site nginx serves off disk has no PHP-FPM to point anywhere, and a form
// that offered to move it would be offering nothing.
func TestSetPHPVersionRefusesASiteThatDoesNotRunPHP(t *testing.T) {
	service := phpVersionService(t)
	id := makeSite(t, service.Store, "static", "")

	err := service.SetPHPVersion(context.Background(), id, "8.4")

	if err == nil {
		t.Fatal("a static site was moved to a PHP version")
	}

	if !strings.Contains(err.Error(), "does not run PHP") {
		t.Errorf("unhelpful refusal: %v", err)
	}
}

// Already there is nothing to do, and nothing to do is not an error — the
// vhost is left alone and nginx is not reloaded for it.
func TestSetPHPVersionIsANoOpWhenTheSiteIsAlreadyOnIt(t *testing.T) {
	service := phpVersionService(t)
	id := makeSite(t, service.Store, "wordpress", "8.3")

	if err := service.SetPHPVersion(context.Background(), id, "8.3"); err != nil {
		t.Fatalf("moving a site to the version it is on failed: %v", err)
	}
}

// The refusal that matters: nginx writes `fastcgi_pass` for a socket that is
// not there without complaining, and the site then answers 502 to every
// request. The caller installs the version first — it does not get to find
// this out from the access log.
func TestSetPHPVersionRefusesAVersionTheMachineIsNotRunning(t *testing.T) {
	service := phpVersionService(t)
	id := makeSite(t, service.Store, "wordpress", "8.3")

	// A version no machine has an FPM socket for.
	err := service.SetPHPVersion(context.Background(), id, "4.0")

	if err == nil {
		t.Fatal("a site was pointed at a PHP that is not on the machine")
	}

	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the refusal does not say what would happen: %v", err)
	}

	// And the row was not changed on the way to refusing.
	site, err := service.Store.FindSite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	if site.PHPVersion != "8.3" {
		t.Errorf("the site was left on PHP %s after a refused move", site.PHPVersion)
	}
}

func TestSetPHPVersionRefusesAnEmptyVersion(t *testing.T) {
	service := phpVersionService(t)
	id := makeSite(t, service.Store, "wordpress", "8.3")

	if err := service.SetPHPVersion(context.Background(), id, "  "); err == nil {
		t.Fatal("an empty version was accepted")
	}
}

// Nothing is ready without a version to be ready for.
func TestPHPVersionReadyRefusesNothing(t *testing.T) {
	if PHPVersionReady("") || PHPVersionReady("   ") {
		t.Fatal("an empty version was reported as running")
	}
}
