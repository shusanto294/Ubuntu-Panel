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
func issueToken(t *testing.T, p *panel) string {
	t.Helper()

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response := p.do(http.MethodPost, "/ai/tokens", `{"name":"Claude"}`, headers)
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
