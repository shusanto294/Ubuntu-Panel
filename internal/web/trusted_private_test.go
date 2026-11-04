package web

import "testing"

// A private network the server is on is trusted as a range, and an address in
// it is never banned; a public address that merely shares nothing is not.
func TestPrivateNetworksAreTrusted(t *testing.T) {
	reasons := map[string]string{"172.17.0.0/16": trustPrivate}

	if trustReason(reasons, "172.17.0.5", "") != trustPrivate {
		t.Error("a container on the Docker bridge is not trusted")
	}
	if trustReason(reasons, "198.51.100.7", "") != "" {
		t.Error("a public address was trusted")
	}
}
