package web

import "testing"

func TestTrustReason(t *testing.T) {
	reasons := map[string]string{
		"103.91.52.3": trustSignedIn,
		"10.0.0.0/8":  trustPrivate,
		"192.0.2.10":  trustThisServer,
	}

	cases := []struct {
		address, viewer, want string
	}{
		{"203.0.113.5", "203.0.113.5", trustYou},
		{"103.91.52.3", "", trustSignedIn},
		{"10.1.2.3", "", trustPrivate},
		{"192.0.2.10", "", trustThisServer},
		{"104.16.0.1", "", trustCloudflare},
		{"203.0.113.9", "103.91.52.3", ""},
		{"", "", ""},
	}

	for _, c := range cases {
		if got := trustReason(reasons, c.address, c.viewer); got != c.want {
			t.Errorf("trustReason(%q, viewer %q) = %q, want %q", c.address, c.viewer, got, c.want)
		}
	}
}
