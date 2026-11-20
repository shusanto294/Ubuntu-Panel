package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// signedIn is a panel with its first account made and signed in, and the
// headers a request from the page carries.
func signedIn(t *testing.T) (*panel, map[string]string) {
	t.Helper()

	p := start(t)
	p.do(http.MethodGet, "/setup", "", nil).Body.Close()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/setup", `{"name":"Admin","email":"admin@example.com",
		"password":"a-long-enough-password","password_confirmation":"a-long-enough-password"}`, headers)
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup = %d", response.StatusCode)
	}

	return p, map[string]string{"X-XSRF-TOKEN": p.csrf(), "Accept": "application/json"}
}

// Opening Security → Cloudflare is answered from the panel's copy: the zone
// list and the rules come back exactly as kept, with no request to
// Cloudflare — whose answer, with this made-up token, would be an error.
func TestTheCloudflareTabOpensFromThePanelsCopy(t *testing.T) {
	p, headers := signedIn(t)
	ctx := context.Background()

	users, err := p.db.Users(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %v, %v", users, err)
	}
	user := users[0]

	accountID, err := p.db.CreateDNSAccount(ctx, &store.DNSAccount{
		UserID: user.ID, Provider: "cloudflare", Label: "Main", APIToken: "not-a-real-token",
	})
	if err != nil {
		t.Fatal(err)
	}

	read := time.Unix(1_700_000_000, 0)
	p.db.PutEdgeCache(ctx, "zones:"+itoa(accountID), `[{"id":"023e105f4ecef8ad9ca31a8372d0c353","name":"example.com"}]`, read)
	p.db.PutEdgeCache(ctx, "zone:023e105f4ecef8ad9ca31a8372d0c353", `{"zone_id":"023e105f4ecef8ad9ca31a8372d0c353","zone_name":"example.com","level":"high",
		"custom":[],"ratelimit":[],"access":[{"id":"a1","mode":"block","target":"ip","value":"203.0.113.9","scope":"zone"}],
		"errors":{},"fetched_at":1700000000}`, read)

	response := p.do(http.MethodGet, "/edge/zones", "", headers)
	var zones struct {
		Accounts []struct {
			Zones     []struct{ ID, Name string }
			Error     string
			FetchedAt int64 `json:"fetched_at"`
		}
	}
	json.NewDecoder(response.Body).Decode(&zones)
	response.Body.Close()

	if len(zones.Accounts) != 1 || zones.Accounts[0].Error != "" || len(zones.Accounts[0].Zones) != 1 ||
		zones.Accounts[0].FetchedAt != read.Unix() {
		t.Fatalf("zones = %+v, want the kept list", zones)
	}

	body := `{"targets":[{"account":` + itoa(accountID) + `,"zone_id":"023e105f4ecef8ad9ca31a8372d0c353","zone_name":"example.com"}],"refresh":"0"}`
	response = p.do(http.MethodPost, "/edge/all", body, headers)
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("edge/all = %d: %s", response.StatusCode, raw)
	}
	var all struct {
		Zones []struct {
			Level     string
			Access    []struct{ ID string }
			Errors    map[string]string
			FetchedAt int64 `json:"fetched_at"`
		}
	}
	json.NewDecoder(response.Body).Decode(&all)
	response.Body.Close()

	if len(all.Zones) != 1 {
		t.Fatalf("edge/all = %d, %+v", response.StatusCode, all)
	}
	zone := all.Zones[0]
	if zone.Level != "high" || len(zone.Access) != 1 || len(zone.Errors) != 0 || zone.FetchedAt != read.Unix() {
		t.Errorf("zone = %+v, want the kept copy untouched", zone)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
