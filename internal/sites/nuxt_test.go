package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func nuxtSite() *store.Site {
	return &store.Site{
		ID:       2,
		Domain:   "app.example.com",
		Type:     "nuxt",
		RootPath: "/var/www/app.example.com",
		AppPort:  3001,
	}
}

// Nuxt is started from its own build output, and this is the detail that
// decides whether a Nuxt site runs at all.
//
// Every other Node type in the panel starts with `npm run start`. Nuxt's own
// scaffold has no `start` script — it has `preview`, which runs `nuxt preview`
// and is a development convenience. A unit built around `npm run start` on a
// Nuxt site fails, restarts, and fails again for ever, which on the page is a
// deployment that finished and a site answering 502.
func TestANuxtSiteIsStartedFromItsBuildOutput(t *testing.T) {
	meta := config.FindSiteType("nuxt")
	if meta == nil {
		t.Fatal("there is no nuxt site type")
	}

	if meta.Start != "node .output/server/index.mjs" {
		t.Errorf("a Nuxt site starts with %q, which is not its Nitro build output",
			meta.Start)
	}

	if meta.Build == "" {
		t.Error("a Nuxt site has no build command, so .output would never exist")
	}

	if !meta.Proxied || meta.Runtime != "node" {
		t.Error("a Nuxt site has to be a Node application behind nginx")
	}

	// And the unit actually carries it, rather than falling through to the
	// package default.
	if unit := RenderUnit(nuxtSite()); !strings.Contains(unit, "node .output/server/index.mjs") {
		t.Errorf("the systemd unit does not start Nuxt's build output:\n%s", unit)
	}
}

// Nitro reads the port out of the environment, which is how the panel puts a
// site on a port of its own and nginx in front of it.
func TestANuxtUnitIsGivenItsPort(t *testing.T) {
	unit := RenderUnit(nuxtSite())

	for _, wanted := range []string{"Environment=PORT=3001", "Environment=HOST=127.0.0.1"} {
		if !strings.Contains(unit, wanted) {
			t.Errorf("the unit is missing %q:\n%s", wanted, unit)
		}
	}
}

// The hashed client bundle is served off disk rather than proxied.
//
// Dozens of requests a page, every one of them a file whose name changes when
// its contents do — so a year of caching is safe and there is nothing for the
// Node process to decide. Left to the proxy they wake a single-threaded server
// that could be answering something that matters.
func TestANuxtVhostServesTheClientBundleFromDisk(t *testing.T) {
	vhost := RenderVhost(nuxtSite(), false, true)

	for _, wanted := range []string{
		"location /_nuxt/ {",
		"alias /var/www/app.example.com/.output/public/_nuxt/;",
		"expires 365d;",
	} {
		if !strings.Contains(vhost, wanted) {
			t.Errorf("the vhost is missing %q:\n%s", wanted, vhost)
		}
	}

	// And it still proxies everything else, or the site serves assets and
	// nothing else.
	if !strings.Contains(vhost, "proxy_pass http://") {
		t.Errorf("the vhost does not proxy to the application:\n%s", vhost)
	}
}

// Next.js keeps its bundle somewhere else under another prefix; a site type
// with neither gets no alias block at all rather than one pointing at a
// directory that does not exist.
func TestOnlyTheFrameworksWithABuiltBundleGetAnAlias(t *testing.T) {
	cases := map[string]string{
		"nuxt":   "/_nuxt/",
		"nextjs": "/_next/static/",
		"nodejs": "",
		"n8n":    "",
	}

	for kind, prefix := range cases {
		site := nuxtSite()
		site.Type = kind

		_, got := staticOutput(site)

		if got != prefix {
			t.Errorf("a %s site serves static assets under %q, wanted %q", kind, got, prefix)
		}

		vhost := RenderVhost(site, false, true)

		if prefix == "" && strings.Contains(vhost, "expires 365d;") {
			t.Errorf("a %s site got an alias for a bundle it does not build:\n%s", kind, vhost)
		}
	}
}

// A type that builds cannot be installed with production dependencies only.
//
// The build tool is a dev dependency in the scaffold every one of these
// frameworks ships — `nuxt` is one in Nuxt's own template — so `--omit=dev`
// produces an install that succeeds and a build that fails on a command that
// is not there. A plain Node service with no build step still gets production
// dependencies alone, which is what it should run with.
func TestOnlyAServiceWithNothingToBuildSkipsDevDependencies(t *testing.T) {
	cases := map[string]bool{
		"nuxt":   false,
		"nextjs": false,
		"nodejs": true,
	}

	for kind, omitted := range cases {
		meta := config.FindSiteType(kind)
		if meta == nil {
			t.Fatalf("there is no %s site type", kind)
		}

		if (meta.Build == "") != omitted {
			t.Errorf("a %s site has build command %q, so the install step would "+
				"omit dev dependencies: %v — which is the wrong way round",
				kind, meta.Build, omitted)
		}
	}
}
