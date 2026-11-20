package web

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func cacheApp(t *testing.T) *App {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return &App{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Every change the panel makes goes into its copy, so the page shows it
// without reading Cloudflare again.
func TestZoneCopyFollowsTheChangesMade(t *testing.T) {
	a := cacheApp(t)
	ctx := context.Background()

	a.storeZone(ctx, edgeZoneRules{ZoneID: "z", ZoneName: "example.com", Level: "medium",
		Errors: map[string]string{"custom": "was unreadable"}, FetchedAt: 1})

	a.keepAccessRule(ctx, "z", dns.AccessRule{ID: "a1", Mode: "block", Target: "ip", Value: "203.0.113.9"})
	a.changeAccessRule(ctx, "z", "a1", "managed_challenge", "noted")
	a.keepRuleset(ctx, "z", dns.PhaseCustom, dns.Ruleset{ID: "rs1", Rules: []dns.EdgeRule{{ID: "r1"}}})
	a.keepLevel(ctx, "z", "high")

	zone, ok := a.cachedZone(ctx, "z")
	if !ok {
		t.Fatal("the copy is gone")
	}

	if len(zone.Access) != 1 || zone.Access[0].Mode != "managed_challenge" || zone.Access[0].Notes != "noted" ||
		zone.Access[0].Scope != "zone" {
		t.Errorf("access = %+v", zone.Access)
	}
	if zone.CustomRuleset != "rs1" || len(zone.Custom) != 1 || zone.Errors["custom"] != "" {
		t.Errorf("custom = %q %+v, errors %v", zone.CustomRuleset, zone.Custom, zone.Errors)
	}
	if zone.Level != "high" {
		t.Errorf("level = %q", zone.Level)
	}

	a.dropAccessRule(ctx, "z", "a1")
	if zone, _ := a.cachedZone(ctx, "z"); len(zone.Access) != 0 {
		t.Errorf("the deleted rule is still in the copy: %+v", zone.Access)
	}

	// The copy's ruleset is what the next write starts from.
	if known := a.knownRuleset(ctx, "z", dns.PhaseCustom); known == nil || known.ID != "rs1" {
		t.Errorf("known ruleset = %+v", known)
	}
}

// A zone the panel has never read gets no half-made copy from a change: the
// next look reads it whole.
func TestChangesToAZoneWithNoCopyMakeNone(t *testing.T) {
	a := cacheApp(t)
	ctx := context.Background()

	a.keepAccessRule(ctx, "never-read", dns.AccessRule{ID: "a1", Mode: "block"})

	if _, ok := a.cachedZone(ctx, "never-read"); ok {
		t.Error("a change made a copy of a zone that was never read")
	}
	if a.knownRuleset(ctx, "never-read", dns.PhaseCustom) != nil {
		t.Error("a ruleset was known for a zone never read")
	}
}
