package web

import (
	"strings"
	"testing"
)

func TestTrustReason(t *testing.T) {
	reasons := map[string]string{
		"103.91.52.3":     trustSignedIn,
		"198.51.100.0/24": trustNeverBan,
		"192.0.2.10":      trustThisServer,
	}

	cases := []struct {
		address, viewer, want string
	}{
		{"203.0.113.5", "203.0.113.5", trustYou},
		{"103.91.52.3", "", trustSignedIn},
		{"198.51.100.77", "", trustNeverBan},
		{"192.0.2.10", "", trustThisServer},
		{"203.0.113.9", "103.91.52.3", ""},
		{"", "", ""},
	}

	for _, c := range cases {
		if got := trustReason(reasons, c.address, c.viewer); got != c.want {
			t.Errorf("trustReason(%q, viewer %q) = %q, want %q", c.address, c.viewer, got, c.want)
		}
	}

	// Banning a range that holds a trusted address is refused too.
	if got := trustReason(reasons, "103.91.52.0/24", ""); !strings.Contains(got, "103.91.52.3") {
		t.Errorf("a range holding a trusted address: %q", got)
	}
	if got := trustReason(reasons, "203.0.113.0/24", ""); got != "" {
		t.Errorf("an untrusted range: %q", got)
	}
}
