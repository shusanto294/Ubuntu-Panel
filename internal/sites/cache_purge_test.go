package sites

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func TestTheCacheModesSayWhichCachesTheyUse(t *testing.T) {
	cases := map[string][2]bool{
		"off":   {false, false},
		"nginx": {true, false},
		"redis": {false, true},
		"full":  {true, true},
	}

	for mode, want := range cases {
		if CacheUsesPage(mode) != want[0] || CacheUsesObject(mode) != want[1] {
			t.Errorf("%s: page %v object %v, want %v", mode, CacheUsesPage(mode), CacheUsesObject(mode), want)
		}
	}
}

// Both caches at once still puts nginx's page cache in the vhost.
func TestBothCachesTurnThePageCacheOn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	id, err := st.CreateSite(ctx, &store.Site{Domain: "fast.example", Type: "wordpress", RootPath: "/tmp/fast.example"})
	if err != nil {
		t.Fatal(err)
	}

	st.SetSiteCache(id, "full", "12h")
	site, _ := st.FindSite(ctx, id)

	if !CacheActive(site) {
		t.Fatal("the page cache is not on with both caches chosen")
	}
	if !strings.Contains(RenderVhost(site, false, false), "fastcgi_cache "+CacheZone(site)) {
		t.Error("the vhost has no page cache with both caches chosen")
	}
}

// The plugin names this site's cache directory and nothing else, and is PHP
// that parses.
func TestThePurgePluginPointsAtTheSitesCache(t *testing.T) {
	plugin := PagePurgePlugin("/var/cache/ubuntu-panel/fast.example")

	if !strings.Contains(plugin, "const UBUNTU_PANEL_CACHE_DIR = '/var/cache/ubuntu-panel/fast.example';") {
		t.Fatal("the plugin does not name the site's cache directory")
	}

	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here to lint the plugin with")
	}

	path := filepath.Join(t.TempDir(), "plugin.php")
	os.WriteFile(path, []byte(plugin), 0o644)

	if output, err := exec.Command(php, "-l", path).CombinedOutput(); err != nil {
		t.Fatalf("the plugin does not parse: %s", output)
	}
}
