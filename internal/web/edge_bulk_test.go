package web_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Changing a rule in every zone that has it names each zone's own copy; the
// panel refuses a copy without its rule id, an id that is not one, and a
// credential that is not the user's, before anything reaches Cloudflare.
func TestBulkRuleChangesNeedEachZonesCopy(t *testing.T) {
	p := signedOut(t)
	p.login(adminPassword).Body.Close()

	const zone = "0123456789abcdef0123456789abcdef"

	cases := []struct {
		name, path, body, want string
	}{
		{
			"no rule id", "/edge/bulk/rules/delete",
			`{"phase":"custom","copies":[{"account":1,"zone_id":"` + zone + `","zone_name":"example.com"}]}`,
			"not on a Cloudflare account", // the credential is checked first
		},
		{
			"a rule id that is not one", "/edge/bulk/access/delete",
			`{"copies":[{"account":1,"zone_id":"` + zone + `","rule_id":"../../x"}]}`,
			"not a rule",
		},
		{
			"no zones", "/edge/bulk/level",
			`{"level":"high","targets":[]}`,
			"Choose at least one zone",
		},
		{
			"a level Cloudflare has not got", "/edge/bulk/level",
			`{"level":"paranoid","targets":[{"account":1,"zone_id":"` + zone + `"}]}`,
			"Choose a security level",
		},
		{
			"reading zones of a credential that is not the user's", "/edge/all",
			`{"targets":[{"account":99,"zone_id":"` + zone + `"}]}`,
			"not on a Cloudflare account",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			response := p.do(http.MethodPost, c.path, c.body, map[string]string{"X-XSRF-TOKEN": p.csrf()})
			defer response.Body.Close()

			raw, _ := io.ReadAll(response.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)

			if response.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("POST %s = %d, want 422: %s", c.path, response.StatusCode, raw)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, c.want) {
				t.Fatalf("POST %s said %q, want it to mention %q", c.path, msg, c.want)
			}
		})
	}
}
