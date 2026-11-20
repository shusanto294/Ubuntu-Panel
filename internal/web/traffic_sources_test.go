package web

import (
	"context"
	"slices"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A mail domain's webmail and autoconfig hostnames answer directly, so they
// are on the Analytics page under their own names — not missing, and not
// filed as the bare server IP.
func TestAnalyticsReadsTheMailHostnamesLogs(t *testing.T) {
	a := cacheApp(t)
	ctx := context.Background()

	if _, err := a.Store.CreateSite(ctx, &store.Site{Domain: "example.com", Type: "static"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.CreateEmailDomain(ctx, &store.EmailDomain{Domain: "example.com"}); err != nil {
		t.Fatal(err)
	}

	names := a.trafficNames(ctx)
	for _, want := range []string{"example.com", "mail.example.com", "autoconfig.example.com"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is not on the Analytics page: %v", want, names)
		}
	}

	for _, source := range a.trafficSources(ctx) {
		if source.Path != "/var/log/nginx/"+source.Name+"-access.log" {
			t.Errorf("%s reads %s", source.Name, source.Path)
		}
		if source.Site != (source.Name == "example.com") {
			t.Errorf("%s marked site=%v", source.Name, source.Site)
		}
	}

	if a.analyticsSite(ctx, "mail.example.com") != "mail.example.com" {
		t.Error("the page cannot be narrowed to the webmail hostname")
	}
}
