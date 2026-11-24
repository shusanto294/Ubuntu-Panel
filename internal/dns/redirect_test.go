package dns

import "testing"

// roundTrip is a redirect written as Cloudflare stores it and read back.
func roundTrip(t *testing.T, r Redirect) Redirect {
	t.Helper()
	if err := CheckRedirect(&r); err != nil {
		t.Fatalf("CheckRedirect: %v", err)
	}
	rule := RedirectRule(r)
	rule.ID = "abc"
	rule.Raw = map[string]any{"action": "redirect", "action_parameters": rule.ActionParameters}
	// Cloudflare sends numbers back as JSON numbers.
	from := rule.ActionParameters["from_value"].(map[string]any)
	from["status_code"] = float64(from["status_code"].(int))
	return ParseRedirect(rule)
}

func TestRedirectRoundTrip(t *testing.T) {
	cases := []Redirect{
		{Hosts: []string{"Example.com."}, Target: "https://other.com", StatusCode: 301, KeepQuery: true, Enabled: true},
		{Hosts: []string{"example.com", "www.example.com"}, Target: "https://other.com/", StatusCode: 302, KeepPath: true, Enabled: true},
		{Hosts: []string{"mail.example.com"}, PathMode: PathExact, Path: "/old", Target: "https://x.io/new", StatusCode: 308},
		{Hosts: []string{"example.com"}, PathMode: PathPrefix, Path: "/blog/*", Target: "https://blog.x.io", StatusCode: 301, KeepPath: true},
	}

	for _, c := range cases {
		want := c
		CheckRedirect(&want)
		got := roundTrip(t, c)

		if !got.Managed {
			t.Fatalf("%v: not read back as the panel's own (expression %s)", c.Hosts, got.Expression)
		}
		if len(got.Hosts) != len(want.Hosts) || got.Hosts[0] != want.Hosts[0] {
			t.Errorf("hosts: got %v want %v", got.Hosts, want.Hosts)
		}
		if got.PathMode != want.PathMode || got.Path != want.Path {
			t.Errorf("path: got %s %q want %s %q", got.PathMode, got.Path, want.PathMode, want.Path)
		}
		wantTarget := want.Target
		if want.KeepPath {
			wantTarget = trimSlash(wantTarget)
		}
		if got.Target != wantTarget || got.KeepPath != want.KeepPath || got.KeepQuery != want.KeepQuery || got.StatusCode != want.StatusCode {
			t.Errorf("target: got %+v want %+v", got, want)
		}
	}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func TestRedirectRefusals(t *testing.T) {
	bad := []Redirect{
		{Hosts: []string{"*.example.com"}, Target: "https://x.io", StatusCode: 301},
		{Hosts: []string{"example.com"}, Target: "ftp://x.io", StatusCode: 301},
		{Hosts: []string{"example.com"}, Target: `https://x.io/"`, StatusCode: 301},
		{Hosts: []string{"example.com"}, Target: "https://x.io", StatusCode: 200},
		{Hosts: []string{"example.com"}, PathMode: PathExact, Path: `/a"b`, Target: "https://x.io", StatusCode: 301},
	}
	for _, r := range bad {
		if err := CheckRedirect(&r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestRedirectExpression(t *testing.T) {
	r := Redirect{Hosts: []string{"a.com", "www.a.com"}, PathMode: PathPrefix, Path: "/x/"}
	want := `(http.host in {"a.com" "www.a.com"} and starts_with(http.request.uri.path, "/x/"))`
	if got := RedirectExpression(r); got != want {
		t.Errorf("got %s", got)
	}
}

func TestDashboardRuleIsNotManaged(t *testing.T) {
	got := ParseRedirect(EdgeRule{Action: "redirect", Expression: `(http.request.full_uri wildcard "*")`,
		Raw: map[string]any{"action_parameters": map[string]any{"from_value": map[string]any{
			"target_url": map[string]any{"value": "https://x.io"}, "status_code": float64(302)}}}})
	if got.Managed || got.Target != "https://x.io" || got.StatusCode != 302 {
		t.Errorf("got %+v", got)
	}
}
