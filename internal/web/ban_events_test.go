package web

import (
	"slices"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
)

func TestApplyBanEvents(t *testing.T) {
	before := map[string]security.JailStatus{
		"panel-web-scan": {Running: true, CurrentlyBanned: 1, TotalBanned: 4, Banned: []string{"203.0.113.1"}},
	}

	after := applyBanEvents(before, []banEvent{
		{jail: "panel-web-scan", ban: true, address: "203.0.113.2"},
		{jail: "panel-web-scan", ban: true, address: "203.0.113.2"}, // a repeat changes nothing
		{jail: "panel-web-scan", ban: false, address: "203.0.113.1"},
		{jail: "not-in-the-copy", ban: true, address: "203.0.113.3"},
	})

	got := after["panel-web-scan"]
	if !slices.Equal(got.Banned, []string{"203.0.113.2"}) || got.CurrentlyBanned != 1 || got.TotalBanned != 5 {
		t.Fatalf("after the events: %+v", got)
	}

	if _, ok := after["not-in-the-copy"]; ok {
		t.Fatal("a jail the copy did not have was made up")
	}

	// The copy it came from is shared with readers and must not change.
	if !slices.Equal(before["panel-web-scan"].Banned, []string{"203.0.113.1"}) {
		t.Fatalf("the original copy changed: %v", before["panel-web-scan"].Banned)
	}
}
