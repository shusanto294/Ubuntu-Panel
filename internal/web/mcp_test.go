package web_test

import (
	"encoding/json"
	"fmt"
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
			Issued *string `json:"issued"`
			Tokens []struct {
				Name       string `json:"name"`
				ServerName string `json:"server_name"`
			} `json:"tokens"`
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

	// Every row says which server it was made for, by the name it was made
	// under — that is how a key is recognised when you come back to the page.
	wanted := "Ubuntu Panel"
	if len(serverName) > 0 {
		wanted = serverName[0]
	}

	if page.Props.Tokens[0].ServerName != wanted {
		t.Errorf("the key row names the server %q, want %q",
			page.Props.Tokens[0].ServerName, wanted)
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

// The address the page hands out has to be the one nginx answers on.
//
// It used to be the public IP with the panel's bind port on the end, which is
// 127.0.0.1:8088 — loopback by design, firewalled from the outside, and a
// client pointed at it sits there until it times out.
func TestTheEndpointIsTheAddressNginxServesNotTheBindPort(t *testing.T) {
	p := start(t)
	signIn(t, p)

	response := p.do(http.MethodGet, "/ai", "", p.inertia())
	defer response.Body.Close()

	var page struct {
		Props struct {
			Endpoint string `json:"endpoint"`
		} `json:"props"`
	}

	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decoding the AI page: %v", err)
	}

	if strings.Contains(page.Props.Endpoint, ":8088") {
		t.Errorf("the endpoint names the bind port: %s", page.Props.Endpoint)
	}

	if !strings.HasSuffix(page.Props.Endpoint, "/mcp") {
		t.Errorf("the endpoint is %s, which is not the MCP path", page.Props.Endpoint)
	}
}

// DNS is part of creating a site. With no credential connected the site is
// still created, and the answer says exactly what has to be pointed where —
// which is the sentence the person needs, rather than a site that silently
// resolves nowhere.
func TestCreatingASiteSaysWhatHappenedToItsDNS(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	text, failed := toolText(t, p, token, "create_site",
		`{"type":"static","domain":"example.test"}`)

	if failed {
		t.Fatalf("creating a static site failed: %s", text)
	}

	var answer struct {
		SiteID    int64 `json:"site_id"`
		ManageDNS bool  `json:"manage_dns"`
		DNS       struct {
			Managed bool   `json:"managed"`
			Reason  string `json:"reason"`
			Tell    string `json:"tell_the_person"`
		} `json:"dns"`
	}

	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("the answer was not JSON: %v\n%s", err, text)
	}

	if answer.SiteID <= 0 {
		t.Fatalf("no site id came back: %s", text)
	}

	if answer.ManageDNS {
		t.Error("it claimed to manage DNS with no credential connected")
	}

	if !strings.Contains(answer.DNS.Reason, "no DNS provider is connected") {
		t.Errorf("the reason was %q", answer.DNS.Reason)
	}

	if !strings.Contains(answer.DNS.Tell, "A record for example.test") {
		t.Errorf("it did not say what to point: %q", answer.DNS.Tell)
	}
}

func TestTheDNSToolsAreOffered(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	_, list := rpc(t, p, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools, _ := list["result"].(map[string]any)["tools"].([]any)

	found := map[string]bool{}

	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok {
			found[tool["name"].(string)] = true
		}
	}

	for _, name := range []string{"list_dns_accounts", "sync_dns"} {
		if !found[name] {
			t.Errorf("the tool list is missing %s", name)
		}
	}
}

// A site the panel does not manage DNS for says so, and says why, rather than
// failing with something about a missing credential id.
func TestSyncingDNSForASiteWithoutItExplainsItself(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	if _, failed := toolText(t, p, token, "create_site",
		`{"type":"static","domain":"example.test"}`); failed {
		t.Fatal("the site could not be created")
	}

	text, failed := toolText(t, p, token, "sync_dns", `{"site":"example.test"}`)

	if !failed || !strings.Contains(text, "does not manage DNS") {
		t.Errorf("sync_dns answered %q", text)
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

// The bug this whole arrangement exists to prevent: a site created by an
// assistant coming out different from one created in the browser.
//
// Both are asked for the least a person can give — a type and a domain — and
// the rows they produce have to agree on every default the panel has.
func TestASiteFromTheToolMatchesOneFromTheForm(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	// The form posts every field, because a browser form does; the defaults
	// under test are the ones the create page itself fills in.
	response := p.do(http.MethodPost, "/sites", `{
		"type": "static",
		"domain": "form.example",
		"aliases": [],
		"php_version": "",
		"web_directory": "",
		"ssl": true,
		"repository": "",
		"branch": "main",
		"manage_dns": false,
		"dns_type": "A",
		"dns_content": "",
		"dns_proxied": true
	}`, headers)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /sites = %d, want 303", response.StatusCode)
	}

	// The tool is given the two arguments the person actually said.
	if text, failed := toolText(t, p, token, "create_site",
		`{"type":"static","domain":"tool.example"}`); failed {
		t.Fatalf("create_site failed: %s", text)
	}

	read := func(domain string) map[string]any {
		t.Helper()

		text, failed := toolText(t, p, token, "get_site", `{"site":"`+domain+`"}`)
		if failed {
			t.Fatalf("get_site %s failed: %s", domain, text)
		}

		site := map[string]any{}

		if err := json.Unmarshal([]byte(text), &site); err != nil {
			t.Fatalf("get_site %s was not JSON: %v", domain, err)
		}

		return site
	}

	form, tool := read("form.example"), read("tool.example")

	for _, field := range []string{
		"type", "ssl", "php_version", "manage_dns", "dns_type", "dns_content",
		"dns_proxied", "web_directory", "branch", "tls_mode",
	} {
		if fmt.Sprint(form[field]) != fmt.Sprint(tool[field]) {
			t.Errorf("%s differs: the form made %v, the tool made %v",
				field, form[field], tool[field])
		}
	}
}

// Every page of the panel is reachable through a tool.
//
// The list is the panel's own menu, one name per thing a person can do in the
// browser. It is written out rather than derived so that a page added with no
// tool behind it fails here: the whole point of this server is that an
// assistant is never the one who has to say "open the panel and do it yourself".
func TestEveryPanelCapabilityHasATool(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	_, list := rpc(t, p, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools, _ := list["result"].(map[string]any)["tools"].([]any)

	found := map[string]bool{}

	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok {
			found[tool["name"].(string)] = true
		}
	}

	for _, name := range []string{
		// This machine
		"server_status", "server_metrics", "panel_settings", "set_panel_domain",
		"panel_update", "panel_account", "ai_access",

		// Sites, and the tabs on one
		"list_site_types", "list_sites", "get_site", "create_site", "delete_site",
		"deploy_site", "pull_site", "restart_site", "site_logs", "set_web_directory",
		"site_php_settings", "request_certificate", "site_cache", "site_cron",
		"site_access", "wordpress_login_link",

		// A site's backups
		"list_site_backups", "create_site_backup", "restore_site_backup",
		"delete_site_backup", "site_backup_schedule",

		// Files
		"list_files", "read_file", "write_file", "manage_files", "upload_file",
		"download_file",

		// Logs
		"get_task", "list_tasks",

		// Databases
		"list_databases", "get_database", "create_database", "delete_database",
		"set_database_password", "repair_database", "manage_database_users",
		"mongo_connection", "service_credentials",

		// Email
		"list_email_domains", "get_email_domain", "create_email_domain",
		"delete_email_domain", "retry_email_domain", "sync_email_dns", "create_mailbox",
		"list_mailboxes", "get_mailbox", "update_mailbox", "set_mailbox_password",
		"repair_mailbox", "delete_mailbox", "configure_mail_server", "test_mail_delivery",

		// DNS
		"list_dns_accounts", "list_dns_providers", "connect_dns_account",
		"update_dns_account", "verify_dns_account", "delete_dns_account",
		"list_dns_zones", "list_dns_records", "set_dns_record", "delete_dns_record",
		"sync_dns",

		// Apps, and the machine itself
		"list_apps", "install_apps", "uninstall_app", "retry_app", "detect_apps",
		"run_command", "panel_guide",
	} {
		if !found[name] {
			t.Errorf("the tool list is missing %s", name)
		}
	}
}

// An address at a domain the server does not handle yet is answered with the
// tool that fixes it, not with a shrug.
func TestCreatingAMailboxNamesTheDomainToolWhenTheDomainIsMissing(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	text, failed := toolText(t, p, token, "create_mailbox",
		`{"address":"shusanto@example.com","password":"a-long-enough-password"}`)

	if !failed {
		t.Fatalf("a mailbox on an unknown domain was accepted: %s", text)
	}

	if !strings.Contains(text, "create_email_domain") {
		t.Errorf("the refusal does not name the tool that fixes it: %s", text)
	}
}

// A mail domain on a machine with no mail server says which app is missing.
func TestCreatingAMailDomainSaysWhatIsMissing(t *testing.T) {
	p := start(t)
	signIn(t, p)

	token := issueToken(t, p)

	text, failed := toolText(t, p, token, "create_email_domain", `{"domain":"example.com"}`)

	if !failed {
		t.Fatalf("a mail domain was accepted with no mail server installed: %s", text)
	}

	if !strings.Contains(text, "install_apps") && !strings.Contains(text, "configure_mail_server") {
		t.Errorf("the refusal does not say how to get a mail server: %s", text)
	}
}
