package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The blocks that used to fall through to nginx's shared log — the one the
// Analytics page reads as the bare server IP — get a log of their own.
func TestAddMissingAccessLogsGivesEachNamedBlockItsOwnLog(t *testing.T) {
	conf := "# Managed by Ubuntu Panel\n" +
		"server {\n" +
		"    listen 80;\n" +
		"    server_name autoconfig.example.com autodiscover.example.com;\n" +
		"\n" +
		"    location / {\n" +
		"        return 404 \"nothing\";\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"server {\n" +
		"    listen 443 ssl;\n" +
		"    server_name example.com;\n" +
		"    access_log /var/log/nginx/example.com-access.log;\n" +
		"}\n" +
		"\n" +
		"server {\n" +
		"    listen 80 default_server;\n" +
		"    server_name _;\n" +
		"}\n"

	updated, changed := AddMissingAccessLogs(conf)
	if !changed {
		t.Fatal("nothing changed")
	}

	if !strings.Contains(updated, "autodiscover.example.com;\n    access_log /var/log/nginx/autoconfig.example.com-access.log;\n\n    location /") {
		t.Errorf("the autoconfig block has no log of its own:\n%s", updated)
	}
	if strings.Count(updated, "example.com-access.log") != 2 {
		t.Errorf("a block that had a log got another:\n%s", updated)
	}
	if strings.Contains(updated, "/_-access.log") {
		t.Errorf("the default server was taken off the shared log:\n%s", updated)
	}

	if again, changed := AddMissingAccessLogs(updated); changed || again != updated {
		t.Error("a second pass changed it again")
	}
}

// What the panel writes today already has a log in every named block.
func TestRenderedVhostsLogEveryBlock(t *testing.T) {
	site := &store.Site{Domain: "example.com", Type: "static"}

	for _, tls := range []bool{false, true} {
		if _, changed := AddMissingAccessLogs(RenderVhost(site, tls, true)); changed {
			t.Errorf("RenderVhost(tls=%v) has a block with no access_log", tls)
		}
	}
}
