package inertia

import (
	"net/http/httptest"
	"testing"
)

// A deferred prop is computed only when a partial reload of the same page
// names it — never on the first paint.
func TestAsked(t *testing.T) {
	first := httptest.NewRequest("GET", "/logs", nil)
	if Asked(first, "Logs/Index", "stats") {
		t.Fatal("a first load asked for a deferred prop")
	}

	partial := httptest.NewRequest("GET", "/logs", nil)
	partial.Header.Set("X-Inertia-Partial-Data", "events, stats")
	partial.Header.Set("X-Inertia-Partial-Component", "Logs/Index")
	if !Asked(partial, "Logs/Index", "stats") {
		t.Fatal("a partial reload naming the prop did not get it")
	}

	other := httptest.NewRequest("GET", "/logs", nil)
	other.Header.Set("X-Inertia-Partial-Data", "stats")
	other.Header.Set("X-Inertia-Partial-Component", "Security/Index")
	if Asked(other, "Logs/Index", "stats") {
		t.Fatal("a partial reload of another page counted")
	}
}
