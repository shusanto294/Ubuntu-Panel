package web_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// mailInstalled tells the panel the mail server is on this machine and set up,
// which is what the Email page and the mail tools both refuse to work without.
//
// Nothing is actually installed — these tests are about which work is queued,
// not about Postfix — so the two facts the guard reads are written directly.
func mailInstalled(t *testing.T, p *panel) {
	t.Helper()

	p.db.SetServiceStatus("mail", store.ServiceInstalled, "", "")
	p.db.SetSetting("mail_configured", "1")
}

// queuedKinds is the work waiting on the queue, in order.
func queuedKinds(t *testing.T, p *panel) []string {
	t.Helper()

	jobs, err := p.db.QueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("reading the queue: %v", err)
	}

	kinds := make([]string, 0, len(jobs))
	for _, job := range jobs {
		kinds = append(kinds, job.Kind)
	}

	return kinds
}

// call runs one MCP tool and returns the tool's own answer, failing on a
// protocol error or on a tool that refused.
func call(t *testing.T, p *panel, token, name, arguments string) map[string]any {
	t.Helper()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` +
		strconv.Quote(name) + `,"arguments":` + arguments + `}}`

	_, answer := rpc(t, p, token, body)

	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s returned no result: %v", name, answer)
	}

	if failed, _ := result["isError"].(bool); failed {
		t.Fatalf("%s refused: %v", name, result)
	}

	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s returned nothing: %v", result, answer)
	}

	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("%s did not answer with JSON: %v\n%s", name, err, text)
	}

	return decoded
}

// A mail domain created by an assistant is set up by the same worker as one
// created from the Email page.
//
// This is the property that matters, and it is worth a test rather than a
// comment: the setup a domain gets — its DKIM key, its Postfix row, its DNS
// records, its autoconfig host — lives in mail.CreateDomain, and the two ways
// of asking for it are two pushes of one job. A tool that grew its own copy of
// any of that would drift from the page the first time either changed, and the
// symptom would be a domain that works when made one way and not the other.
func TestAnAssistantSetsAMailDomainUpTheSameWayThePageDoes(t *testing.T) {
	p := start(t)
	signIn(t, p)
	mailInstalled(t, p)

	token := issueToken(t, p)

	answer := call(t, p, token, "create_email_domain", `{"domain":"example.com","manage_dns":false}`)

	if answer["queued"] != true {
		t.Fatalf("create_email_domain did not queue the setup: %v", answer)
	}

	if kinds := queuedKinds(t, p); len(kinds) != 1 || kinds[0] != "mail.domain.create" {
		t.Fatalf("an assistant queued %v, wanted one mail.domain.create", kinds)
	}
}

// And a mailbox created by an assistant is set up by the same worker too.
//
// The folders a new mailbox gets — Sent, Drafts, Trash — come from the Dovecot
// drop-in rather than from anything per-account, so they follow from this: one
// job, one service, one configuration. There is nothing for a tool to do
// differently, and this is what keeps it that way.
func TestAnAssistantCreatesAMailboxTheSameWayThePageDoes(t *testing.T) {
	p := start(t)
	signIn(t, p)
	mailInstalled(t, p)

	token := issueToken(t, p)

	// The domain the mailbox goes on, already set up.
	id, err := p.db.CreateEmailDomain(context.Background(), &store.EmailDomain{
		Domain:       "example.com",
		DKIMSelector: "mail",
	})
	if err != nil || id <= 0 {
		t.Fatalf("creating the mail domain: %v", err)
	}

	p.db.SetEmailDomainStatus(id, "active", "")

	answer := call(t, p, token, "create_mailbox", `{"address":"sam@example.com"}`)

	if answer["queued"] != true {
		t.Fatalf("create_mailbox did not queue the work: %v", answer)
	}

	if kinds := queuedKinds(t, p); len(kinds) != 1 || kinds[0] != "mail.account.create" {
		t.Fatalf("an assistant queued %v, wanted one mail.account.create", kinds)
	}

	// And the answer points the person at the autoconfig host rather than at
	// four settings to type, which is the whole reason the record is published.
	settings, _ := answer["client_settings"].(map[string]any)

	note, _ := settings["autoconfig"].(string)
	if !strings.Contains(note, "autoconfig.example.com") {
		t.Errorf("a new mailbox's settings do not mention where a client finds them: %v", settings)
	}
}
