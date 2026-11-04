package web

import (
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func TestNotificationIPs(t *testing.T) {
	carried := &store.Notification{Kind: "attacks", Data: map[string]any{"ips": []any{"203.0.113.9", "nonsense", "203.0.113.9"}}}
	if ips := notificationIPs(carried); len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Errorf("carried: %v", ips)
	}

	// Written before notifications carried their addresses.
	old := &store.Notification{Kind: "attacks", Title: "1 address is attacking the sites",
		Body: "45.115.26.203 — 28 requests for known-vulnerable files", Data: map[string]any{}}
	if ips := notificationIPs(old); len(ips) != 1 || ips[0] != "45.115.26.203" {
		t.Errorf("from the text: %v", ips)
	}

	// A disk alert that happens to contain a dotted number is about no address.
	disk := &store.Notification{Kind: "disk_full", Body: "version 1.2.3.4 of something", Data: map[string]any{}}
	if ips := notificationIPs(disk); len(ips) != 0 {
		t.Errorf("disk: %v", ips)
	}
}

func TestSiteOfLog(t *testing.T) {
	cases := map[string]string{
		"/var/log/nginx/shop.example-access.log":   "shop.example",
		"/var/log/nginx/shop.example-access.log.1": "shop.example",
		"/var/log/nginx/access.log":                unmatchedSite,
	}
	for path, want := range cases {
		if got := siteOfLog(path); got != want {
			t.Errorf("siteOfLog(%q) = %q, want %q", path, got, want)
		}
	}
}
