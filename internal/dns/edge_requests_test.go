package dns

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// fakeCloudflare answers each request from a script, and records them.
type fakeCloudflare struct {
	calls   []string
	answers func(method, path string) (int, string)
}

func (f *fakeCloudflare) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	status, body := f.answers(r.Method, r.URL.Path)

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Request:    r,
	}, nil
}

func fakeClient(f *fakeCloudflare) *Client {
	return &Client{
		Account: &store.DNSAccount{Provider: "cloudflare", APIToken: "t"},
		http:    &http.Client{Transport: f},
	}
}

const rulesetAnswer = `{"success":true,"result":{"id":"rs1","rules":[
	{"id":"r1","description":"one","expression":"true","action":"block","enabled":true},
	{"id":"r2","description":"two","expression":"false","action":"block","enabled":true}]}}`

// With the ruleset known, adding a rule is the one request that adds it, and
// the answer is the ruleset as Cloudflare now has it.
func TestAddEdgeRuleWithAKnownRulesetIsOneRequest(t *testing.T) {
	f := &fakeCloudflare{answers: func(method, path string) (int, string) { return 200, rulesetAnswer }}

	set, err := fakeClient(f).AddEdgeRule("z", PhaseCustom, &Ruleset{ID: "rs1"}, EdgeRule{Description: "two", Action: "block"})
	if err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 1 || f.calls[0] != "POST /client/v4/zones/z/rulesets/rs1/rules" {
		t.Errorf("calls = %v, want one POST", f.calls)
	}
	if set.ID != "rs1" || len(set.Rules) != 2 || set.Rules[1].Raw == nil {
		t.Errorf("ruleset = %+v", set)
	}
}

// A copy that says the zone has no ruleset is never believed: making one
// replaces the whole list, so Cloudflare is asked first.
func TestAddEdgeRuleNeverPutsOnTheWordOfACopy(t *testing.T) {
	f := &fakeCloudflare{answers: func(method, path string) (int, string) { return 200, rulesetAnswer }}

	if _, err := fakeClient(f).AddEdgeRule("z", PhaseCustom, &Ruleset{}, EdgeRule{Action: "block"}); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "GET ") || !strings.HasPrefix(f.calls[1], "POST ") {
		t.Errorf("calls = %v, want a read and then a POST to the ruleset it found", f.calls)
	}
}

// Editing a rule the copy holds is one PATCH; a copy Cloudflare says is out
// of date is read again, once, and the edit made from that.
func TestChangeEdgeRuleUsesTheCopyAndRecoversFromAStaleOne(t *testing.T) {
	known := &Ruleset{ID: "rs1", Rules: []EdgeRule{{ID: "r2", Raw: map[string]any{"id": "r2", "action": "block"}}}}

	f := &fakeCloudflare{answers: func(method, path string) (int, string) { return 200, rulesetAnswer }}
	if _, err := fakeClient(f).ChangeEdgeRule("z", PhaseCustom, known, "r2", EdgeRule{Action: "block"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "PATCH ") {
		t.Errorf("calls = %v, want one PATCH", f.calls)
	}

	first := true
	stale := &fakeCloudflare{answers: func(method, path string) (int, string) {
		if method == "PATCH" && first {
			first = false
			return 404, `{"success":false,"errors":[{"message":"not found"}]}`
		}
		return 200, rulesetAnswer
	}}
	if _, err := fakeClient(stale).ChangeEdgeRule("z", PhaseCustom, known, "r2", EdgeRule{Action: "block"}); err != nil {
		t.Fatal(err)
	}
	if len(stale.calls) != 3 || !strings.HasPrefix(stale.calls[1], "GET ") {
		t.Errorf("calls = %v, want PATCH, GET, PATCH", stale.calls)
	}
}

// A new access rule comes back whole, so it can go into the copy as it is.
func TestAddAccessRuleAnswersTheRuleMade(t *testing.T) {
	f := &fakeCloudflare{answers: func(method, path string) (int, string) {
		return 200, `{"success":true,"result":{"id":"a1","mode":"block","notes":"n",
			"configuration":{"target":"ip","value":"203.0.113.9"},"scope":{"type":"zone"}}}`
	}}

	rule, err := fakeClient(f).AddAccessRule("z", AccessRule{Mode: "block", Target: "ip", Value: "203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID != "a1" || rule.Value != "203.0.113.9" || rule.Scope != "zone" || len(f.calls) != 1 {
		t.Errorf("rule = %+v, calls = %v", rule, f.calls)
	}
}
