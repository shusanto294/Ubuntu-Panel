package config

import "testing"

// The panel's port is its own, and never one of the sites'.
//
// 80 and 443 are what every site on the machine is served from. The panel
// taking one back is both the thing this port exists to stop — a stranger
// typing the server's bare address and being shown a login form — and a way to
// break every site at once, because nginx refuses a configuration with two
// default servers on one address rather than warning about it.
func TestThePanelPortIsNeverASitesPort(t *testing.T) {
	for _, value := range []string{"80", "443", "", "0", "-1", "70000", "not a port"} {
		if port := PanelPort(value); port != DefaultPanelPort {
			t.Errorf("PanelPort(%q) = %d, want the default %d", value, port, DefaultPanelPort)
		}
	}

	for value, want := range map[string]int{"8443": 8443, "9000": 9000, " 2083 ": 2083} {
		if port := PanelPort(value); port != want {
			t.Errorf("PanelPort(%q) = %d, want %d", value, port, want)
		}
	}
}
