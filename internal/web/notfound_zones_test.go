package web

import "testing"

func TestZoneHasSiteMatchesTheZoneAndNamesInsideIt(t *testing.T) {
	sites := []string{"shop.example.com", "blog.other.org"}

	for zone, want := range map[string]bool{
		"example.com":      true,
		"other.org":        true,
		"ample.com":        false, // a suffix of the text, not a parent zone
		"unrelated.net":    false,
		"shop.example.com": true,
	} {
		if got := zoneHasSite(zone, sites); got != want {
			t.Errorf("zoneHasSite(%q) = %v, want %v", zone, got, want)
		}
	}
}
