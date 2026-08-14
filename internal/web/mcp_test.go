package web_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// signIn creates the first account, which signs the client in.
func signIn(t *testing.T, p *panel) {
	t.Helper()

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/setup", `{
		"name": "Admin",
		"email": "admin@example.com",
		"password": "a-long-enough-password",
		"password_confirmation": "a-long-enough-password"
	}`, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup = %d, want 303", response.StatusCode)
	}
}

// issueToken asks the AI page for a key and reads it back off the page that
// follows, which is the one and only place it is ever readable.
//
// serverName is optional and is the name the panel introduces itself by — set
// on this form because this is the only form it is on.
func issueToken(t *testing.T, p *panel, serverName ...string) string {
	t.Helper()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	body := `{"name":"Claude"}`

	if len(serverName) > 0 {
		body = `{"name":"Claude","server_name":` + strconv.Quote(serverName[0]) + `}`
	}

	response := p.do(http.MethodPost, "/ai/tokens", body, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /ai/tokens = %d, want 303", response.StatusCode)
	}

	response = p.do(http.MethodGet, "/ai", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Issued *string          `json:"issued"`
			Tokens []map[string]any `json:"tokens"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the AI page: %v", err)
	}

	if page.Props.Issued == nil || *page.Props.Issued == "" {
		t.Fatal("the page that issued a key did not show it")
	}

	if len(page.Props.Tokens) != 1 {
		t.Fatalf("the key list has %d rows, want 1", len(page.Props.Tokens))
	}

	return *page.Props.Issued
}

// rpc sends one JSON-RPC body to the MCP endpoint with a bearer token.
func rpc(t *testing.T, p *panel, token, body string) (int, map[string]any) {
	t.Helper()

	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}

	response := p.do(http.MethodPost, "/mcp", body, headers)
	defer response.Body.Close()

	answer := map[string]any{}
	json.NewDecoder(response.Body).Decode(&answer)

	return response.StatusCode, answer
}

func TestMCPRefusesARequestWithNoKey(t *testing.T) {
	p := start(t)
	signIn(t, p)

	status, answer := rpc(t, p, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if status != http.StatusUnauthorized {
		t.Fatalf("an MCP call with no key = %d, want 401", status)
	}

	// The reason has to be inside a JSON-RPC error, or the client shows a bare
	// 401 the person cannot act on.
	if _, ok := answer["error"]; !ok {
		t.Errorf("the refusal carried no JSON-RPC error: %v", answer)
	}
}

func TestMCPRefusesAKeyThePanelNeverIssued(t *testing.T) {
	p := start(t)
	signIn(t, p)

	status, _ := rpc(t, p, "up_not-a-real-key", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if status != http.StatusUnauthorized {
		t.Fatalf("an invented key = %d, want 401", status)
	}
}

func TestMCPAnswersAnIssuedKey(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	if !strings.HasPrefix(token, "up_") {
		t.Errorf("the key is %q; every issued key starts with up_", token)
	}

	status, answer := rpc(t, p, token,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)

	if status != http.StatusOK {
		t.Fatalf("initialize = %d, want 200", status)
	}

	result, _ := answer["result"].(map[string]any)
	info, _ := result["serverInfo"].(map[string]any)

	if info["name"] != "Ubuntu Panel" {
		t.Errorf("the server named itself %v", info)
	}

	// The tools an assistant is meant to find. Their names are what a person's
	// saved prompt refers to, so a rename is a breaking change.
	_, list := rpc(t, p, token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	tools, _ := list["result"].(map[string]any)["tools"].([]any)

	found := map[string]bool{}

	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok {
			found[tool["name"].(string)] = true
		}
	}

	for _, name := range []string{"create_site", "list_sites", "get_site", "delete_site",
		"deploy_site", "get_task", "list_apps", "install_apps"} {
		if !found[name] {
			t.Errorf("the tool list is missing %s", name)
		}
	}
}

func TestMCPValidatesBeforeItCreatesASite(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	_, answer := rpc(t, p, token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":
		{"name":"create_site","arguments":{"type":"wordpress","domain":"not a domain"}}}`)

	result, _ := answer["result"].(map[string]any)

	if result["isError"] != true {
		t.Fatalf("a site with a broken domain was accepted: %v", answer)
	}

	// And no site was stored on the way to refusing.
	_, listed := rpc(t, p, token, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":
		{"name":"list_sites","arguments":{}}}`)

	content, _ := listed["result"].(map[string]any)["content"].([]any)
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)

	if !strings.Contains(text, `"count": 0`) {
		t.Errorf("the site list is not empty after a refused creation: %s", text)
	}
}

// toolText runs a tool and hands back its content block and whether it failed.
func toolText(t *testing.T, p *panel, token, name, arguments string) (string, bool) {
	t.Helper()

	_, answer := rpc(t, p, token, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":
		{"name":"`+name+`","arguments":`+arguments+`}}`)

	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s answered %v", name, answer)
	}

	content, _ := result["content"].([]any)
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)

	return text, result["isError"] == true
}

// saveSettings posts the AI page's own form.
func saveSettings(t *testing.T, p *panel, body string) {
	t.Helper()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/ai/settings", body, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /ai/settings = %d, want 303", response.StatusCode)
	}
}

func TestTurningTheServerOffClosesTheEndpoint(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	saveSettings(t, p, `{"enabled":false,"confirm_destructive":true,"allow_all":true}`)

	status, answer := rpc(t, p, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if status != http.StatusServiceUnavailable {
		t.Fatalf("a switched-off server answered %d, want 503", status)
	}

	if _, ok := answer["error"]; !ok {
		t.Errorf("the refusal carried no JSON-RPC error: %v", answer)
	}

	// And back on again, because a switch that only goes one way is a trap.
	saveSettings(t, p, `{"enabled":true,"confirm_destructive":true,"allow_all":true}`)

	if status, _ := rpc(t, p, token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); status != http.StatusOK {
		t.Fatalf("after switching it back on, tools/list = %d", status)
	}
}

// A capability that is not ticked is neither offered nor run. Both halves
// matter: a client lists the tools once and keeps them.
func TestAnUntickedCapabilityIsNeitherOfferedNorRun(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	saveSettings(t, p, `{"enabled":true,"confirm_destructive":true,"allow_all":false,
		"permissions":["sites.read"]}`)

	_, list := rpc(t, p, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools, _ := list["result"].(map[string]any)["tools"].([]any)

	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok && tool["name"] == "run_command" {
			t.Fatal("run_command was offered with the shell capability off")
		}
	}

	text, failed := toolText(t, p, token, "run_command", `{"command":"whoami"}`)

	if !failed || !strings.Contains(text, "does not allow") {
		t.Errorf("calling it anyway gave %q (error: %v)", text, failed)
	}

	// The one that is ticked still works.
	if _, failed := toolText(t, p, token, "list_sites", `{}`); failed {
		t.Error("list_sites was refused with sites.read ticked")
	}
}

func TestDestructiveWorkAsksBeforeItRuns(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	// Reading is free even with the switch on.
	if _, failed := toolText(t, p, token, "run_command", `{"command":"id"}`); failed {
		t.Error("a read-only command was refused")
	}

	text, failed := toolText(t, p, token, "run_command", `{"command":"rm -rf /tmp/panel-test-nothing"}`)

	if !failed || !strings.Contains(text, "confirm: true") {
		t.Fatalf("a destructive command ran without confirmation: %q", text)
	}

	// With the person's yes it runs, and the exit status comes back rather than
	// being reported as a broken tool.
	text, failed = toolText(t, p, token, "run_command",
		`{"command":"rm -rf /tmp/panel-test-nothing","confirm":true}`)

	if failed || !strings.Contains(text, `"exit_code": 0`) {
		t.Fatalf("the confirmed command answered %q", text)
	}

	// And with the switch off, nothing is asked.
	saveSettings(t, p, `{"enabled":true,"confirm_destructive":false,"allow_all":true}`)

	if _, failed := toolText(t, p, token, "run_command",
		`{"command":"rm -rf /tmp/panel-test-nothing"}`); failed {
		t.Error("it still asked with the confirm switch off")
	}
}

// The panel owns its vhosts, its units and its certificates. A command that
// redoes one of those by hand is refused with the tool that owns it.
func TestTheShellWillNotRedoThePanelsOwnWork(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	for _, command := range []string{
		`echo x | sudo tee /etc/nginx/sites-available/example.com`,
		`sudo certbot --nginx -d example.com`,
	} {
		text, failed := toolText(t, p, token, "run_command",
			`{"command":`+strconv.Quote(command)+`,"confirm":true}`)

		if !failed {
			t.Errorf("%q was allowed through: %s", command, text)
		}
	}

	// Reading the same file is not the same request.
	if _, failed := toolText(t, p, token, "run_command",
		`{"command":"ls /etc/nginx/sites-available"}`); failed {
		t.Error("listing the vhost directory was refused")
	}
}

// The name a client shows, which somebody with three panels connected needs to
// be able to change.
func TestTheServerIntroducesItselfByTheNameThatWasSet(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p, "Client staging box")

	_, answer := rpc(t, p, token,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)

	info, _ := answer["result"].(map[string]any)["serverInfo"].(map[string]any)

	if info["name"] != "Client staging box" {
		t.Errorf("it introduced itself as %v", info["name"])
	}
}

func TestRevokingAKeyStopsIt(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	response := p.do(http.MethodGet, "/ai", "", p.inertia())

	var page struct {
		Props struct {
			Tokens []struct {
				ID int64 `json:"id"`
			} `json:"tokens"`
		} `json:"props"`
	}

	json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	deleted := p.do(http.MethodDelete,
		"/ai/tokens/"+strconv.FormatInt(page.Props.Tokens[0].ID, 10), "", headers)
	deleted.Body.Close()

	status, _ := rpc(t, p, token, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)

	if status != http.StatusUnauthorized {
		t.Fatalf("a revoked key = %d, want 401", status)
	}
}
