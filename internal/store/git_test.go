package store

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
)

func gitStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	// A repository token is a stored credential, so this store needs the
	// application key the panel is given at startup.
	cipher, err := crypto.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}

	st.UseCipher(cipher)

	t.Cleanup(func() { st.Close() })

	return st
}

func timeAt(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

// Every statement this feature added has to actually run against SQLite — a
// typo in one is a setter that silently does nothing, which on this page reads
// as a form that will not save.
func TestTheGitSettersAllRun(t *testing.T) {
	st := gitStore(t)
	ctx := context.Background()

	id, err := st.CreateSite(ctx, &Site{
		Domain: "example.com", Type: "php", RootPath: "/var/www/example.com",
		PHPVersion: "8.3", Branch: "main", Repository: "https://github.com/o/r.git",
		RepoAuth: "token", RepoToken: "ghp_secret",
	})
	if err != nil || id <= 0 {
		t.Fatalf("could not create the site: %v", err)
	}

	st.SetSiteRepository(id, "https://github.com/o/other.git", "release")

	if err := st.SetSiteRepoAuth(id, "key", ""); err != nil {
		t.Fatalf("SetSiteRepoAuth: %v", err)
	}

	st.SetSiteDeployKey(id, "ssh-ed25519 AAAA test")

	if err := st.SetSiteWebhookSecret(id, "a-secret"); err != nil {
		t.Fatalf("SetSiteWebhookSecret: %v", err)
	}

	st.SetSiteAutoPull(id, "15m", 1770000000)
	st.SetSiteAutoPullNext(id, 1770000600)
	st.SetSiteLastCommit(id, "abc1234")

	site, err := st.FindSite(ctx, id)
	if err != nil {
		t.Fatalf("FindSite: %v", err)
	}

	checks := map[string]struct{ got, want string }{
		"repository":  {site.Repository, "https://github.com/o/other.git"},
		"branch":      {site.Branch, "release"},
		"auth":        {site.RepoAuth, "key"},
		"deploy key":  {site.RepoDeployKey, "ssh-ed25519 AAAA test"},
		"secret":      {site.WebhookSecret, "a-secret"},
		"auto pull":   {site.AutoPull, "15m"},
		"last commit": {site.LastCommit, "abc1234"},

		// The token was stored by CreateSite and must survive a method change
		// that sent no new one — "" means "keep what is there".
		"token": {site.RepoToken, "ghp_secret"},
	}

	for what, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %q, wanted %q", what, check.got, check.want)
		}
	}

	if site.AutoPullNextAt != 1770000600 {
		t.Errorf("auto_pull_next_at = %d, wanted 1770000600", site.AutoPullNextAt)
	}

	// And the poller finds it.
	due, err := st.DuePulls(ctx, timeAt(1770001000))
	if err != nil {
		t.Fatalf("DuePulls: %v", err)
	}

	if len(due) != 1 || due[0].ID != id {
		t.Errorf("DuePulls returned %d sites, wanted the one that is due", len(due))
	}

	// A site switched off is not polled.
	st.SetSiteAutoPull(id, "off", 0)

	if due, _ := st.DuePulls(ctx, timeAt(1770001000)); len(due) != 0 {
		t.Errorf("a site with auto_pull off was still returned as due")
	}
}
