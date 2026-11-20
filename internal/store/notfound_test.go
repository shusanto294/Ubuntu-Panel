package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// One address blocked in three zones is one row on the Security page, and
// one in the count; lifting it finds all three.
func TestNotFoundAddressesGroupsAnAddressAcrossZones(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()

	for i, zone := range []string{"z1", "z2", "z3"} {
		st.AddNotFoundBlock(ctx, &NotFoundBlock{IP: "203.0.113.9", Host: "a.example", ZoneID: zone, ZoneName: zone,
			RuleID: "r" + zone, Mode: "block", CreatedAt: int64(100 + i), ExpiresAt: 1000})
	}
	st.AddNotFoundBlock(ctx, &NotFoundBlock{IP: "198.51.100.1", Host: "b.example", ZoneID: "z1", ZoneName: "z1",
		RuleID: "x", Mode: "managed_challenge", CreatedAt: 50, ExpiresAt: 900})

	rows, err := st.NotFoundAddresses(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 2 || rows[0].IP != "203.0.113.9" || rows[0].Zones != 3 || rows[1].Zones != 1 {
		t.Fatalf("grouped rows = %+v %+v", rows[0], rows[len(rows)-1])
	}

	if n := st.CountNotFoundBlocks(ctx); n != 2 {
		t.Errorf("count = %d, want 2 addresses", n)
	}

	if all, _ := st.NotFoundBlocksFor(ctx, "203.0.113.9"); len(all) != 3 {
		t.Errorf("rules for the address = %d, want 3", len(all))
	}
}

// A rule kept until removed is never due, and never sets the expiry clock.
func TestNotFoundBlocksWithNoExpiryAreKept(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()

	st.AddNotFoundBlock(ctx, &NotFoundBlock{IP: "203.0.113.9", Host: "a", ZoneID: "z", ZoneName: "z", RuleID: "r",
		Mode: "block", CreatedAt: 1, ExpiresAt: 0})

	if due, _ := st.ExpiredNotFoundBlocks(ctx, time.Unix(1<<40, 0)); len(due) != 0 {
		t.Errorf("a permanent rule was due for removal")
	}

	if next := st.NextNotFoundExpiry(ctx); !next.IsZero() {
		t.Errorf("a permanent rule set the expiry clock to %v", next)
	}
}

// A 404 protection rule removed from the IP access rules list is no longer
// the panel's to list or to take away.
func TestForgetNotFoundRuleDropsOnlyThatRule(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	for _, zone := range []string{"z1", "z2"} {
		st.AddNotFoundBlock(ctx, &NotFoundBlock{IP: "203.0.113.9", Host: "a", ZoneID: zone, ZoneName: zone,
			RuleID: "r-" + zone, Mode: "block", CreatedAt: 1, ExpiresAt: 0})
	}

	st.ForgetNotFoundRule(ctx, "z1", "r-z1")

	if left, _ := st.NotFoundBlocksFor(ctx, "203.0.113.9"); len(left) != 1 || left[0].ZoneID != "z2" {
		t.Errorf("left = %+v", left)
	}
}
