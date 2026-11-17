package dns

import "testing"

func TestEdgeRuleBodyKeepsWhatThePanelDoesNotSet(t *testing.T) {
	existing := map[string]any{
		"id": "abc", "version": "3", "last_updated": "x",
		"action": "block", "expression": "true", "description": "old", "enabled": true,
		"action_parameters": map[string]any{"response": map[string]any{"status_code": 403}},
		"logging":           map[string]any{"enabled": false},
	}

	body := edgeRuleBody(existing, EdgeRule{Description: "new", Expression: "ip.src eq 1.2.3.4", Action: "block", Enabled: false})
	if body["id"] != nil || body["version"] != nil || body["last_updated"] != nil {
		t.Fatalf("bookkeeping sent back: %v", body)
	}
	if body["action_parameters"] == nil || body["logging"] == nil {
		t.Fatalf("unknown fields lost: %v", body)
	}
	if body["description"] != "new" || body["enabled"] != false {
		t.Fatalf("fields not written: %v", body)
	}

	// A different action drops the old one's parameters.
	body = edgeRuleBody(existing, EdgeRule{Description: "n", Expression: "true", Action: "managed_challenge", Enabled: true})
	if body["action_parameters"] != nil {
		t.Fatalf("block's parameters kept for a challenge: %v", body)
	}

	// A new skip rule says what it skips.
	body = edgeRuleBody(nil, EdgeRule{Description: "n", Expression: "true", Action: "skip", Enabled: true})
	if params, _ := body["action_parameters"].(map[string]any); params["ruleset"] != "current" {
		t.Fatalf("skip without parameters: %v", body)
	}
}

func TestEdgeRuleBodyMergesRateLimit(t *testing.T) {
	existing := map[string]any{
		"action": "block",
		"ratelimit": map[string]any{
			"characteristics": []any{"cf.colo.id", "http.request.headers[\"x-key\"]"},
			"period":          60, "requests_per_period": 100, "mitigation_timeout": 600,
			"requests_to_origin": true,
		},
	}

	body := edgeRuleBody(existing, EdgeRule{Action: "block", RateLimit: &RateLimit{Period: 10, RequestsPerPeriod: 5, MitigationTimeout: 10}})
	limit := body["ratelimit"].(map[string]any)
	if limit["requests_to_origin"] != true || limit["period"] != 10 || limit["requests_per_period"] != 5 {
		t.Fatalf("rate limit not merged: %v", limit)
	}
	if chars, _ := limit["characteristics"].([]any); len(chars) != 2 {
		t.Fatalf("characteristics not kept: %v", limit["characteristics"])
	}
}

func TestNormaliseAccessTarget(t *testing.T) {
	cases := []struct{ target, in, want string }{
		{"ip", " 203.0.113.9 ", "203.0.113.9"},
		{"ip6", "2001:db8::1", "2001:db8::1"},
		{"ip_range", "203.0.113.7/24", "203.0.113.0/24"},
		{"asn", "as13335", "AS13335"},
		{"asn", "13335", "AS13335"},
		{"country", "de", "DE"},
		{"country", "T1", "T1"},
	}
	for _, c := range cases {
		got, err := NormaliseAccessTarget(c.target, c.in)
		if err != nil || got != c.want {
			t.Errorf("%s %q = %q, %v; want %q", c.target, c.in, got, err, c.want)
		}
	}

	for _, bad := range [][2]string{{"ip", "2001:db8::1"}, {"ip6", "1.2.3.4"}, {"ip_range", "10.0.0.0/8"}, {"asn", "x"}, {"country", "USA"}, {"nope", "1"}} {
		if _, err := NormaliseAccessTarget(bad[0], bad[1]); err == nil {
			t.Errorf("%s %q accepted", bad[0], bad[1])
		}
	}
}
