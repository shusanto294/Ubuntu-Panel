package sites

import (
	"strings"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A repository URL is the one field on the create form nobody can check by
// reading it back, and an unvalidated one failed four steps into a deployment —
// after the vhost, the database and the certificate existed.
func TestARepositoryIsCheckedBeforeItReachesACommandLine(t *testing.T) {
	accepted := map[string]string{
		"https://github.com/owner/repo.git":     "https://github.com/owner/repo.git",
		"https://github.com/owner/repo":         "https://github.com/owner/repo",
		"  https://github.com/owner/repo.git  ": "https://github.com/owner/repo.git",

		// The scp-like form, which is what every host's "clone with SSH"
		// button hands you.
		"git@github.com:owner/repo.git":       "git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git": "ssh://git@github.com/owner/repo.git",

		// A scheme-less paste out of an address bar.
		"github.com/owner/repo": "https://github.com/owner/repo",
	}

	for given, wanted := range accepted {
		got, err := NormaliseRepository(given)

		if err != nil {
			t.Errorf("NormaliseRepository(%q) refused it: %v", given, err)

			continue
		}

		if got != wanted {
			t.Errorf("NormaliseRepository(%q) = %q, wanted %q", given, got, wanted)
		}
	}

	refused := []string{
		"",                             // handled separately below
		"not a url at all",             // spaces
		"http://github.com/owner/repo", // would send a token in the clear
		"git://github.com/owner/repo",  // unauthenticated, and GitHub dropped it
		"ftp://example.com/repo.git",   // not a protocol git clones here
		"https://github.com",           // a host and no repository
		"https://github.com/owner/repo; rm -rf /", // shell metacharacters
	}

	for _, given := range refused {
		if given == "" {
			// Empty is a site with no repository, which is allowed.
			if value, err := NormaliseRepository(""); err != nil || value != "" {
				t.Errorf("an empty repository was not accepted as 'no repository'")
			}

			continue
		}

		if _, err := NormaliseRepository(given); err == nil {
			t.Errorf("NormaliseRepository(%q) was accepted", given)
		}
	}
}

// A URL with a token already in it is a credential in the wrong field, not a
// mistake to refuse. The URL is cleaned and stored, and the operator is told
// where the token belongs — because left in, it would be written into
// .git/config in the clear, into the deployment console, and into every backup.
func TestCredentialsInAURLAreStrippedRatherThanRefused(t *testing.T) {
	cleaned, err := NormaliseRepository("https://user:ghp_secret@github.com/owner/repo.git")

	if !ErrCredentialsInURL(err) {
		t.Fatalf("a URL with credentials gave %v, wanted the credentials-in-URL answer", err)
	}

	if cleaned != "https://github.com/owner/repo.git" {
		t.Errorf("the cleaned URL is %q, which still is not clean", cleaned)
	}

	if strings.Contains(cleaned, "ghp_secret") || strings.Contains(cleaned, "user") {
		t.Errorf("the credential survived into %q", cleaned)
	}
}

// A token is sent as the password in the URL, because that is the only way git
// sends one over HTTPS without an interactive prompt.
func TestATokenIsPutIntoTheURLGitIsGiven(t *testing.T) {
	site := &store.Site{
		Repository: "https://github.com/owner/repo.git",
		RepoAuth:   AuthToken,
		RepoToken:  "ghp_abc123",
	}

	url := CloneURL(site)

	if !strings.Contains(url, "ghp_abc123@github.com") {
		t.Errorf("the token is not in the URL git is given: %q", url)
	}

	if !strings.Contains(url, "x-access-token") {
		t.Errorf("GitHub wants a username beside the token: %q", url)
	}

	// GitLab is the one host that insists on a particular one.
	site.Repository = "https://gitlab.com/owner/repo.git"

	if url := CloneURL(site); !strings.Contains(url, "oauth2:ghp_abc123@") {
		t.Errorf("GitLab needs oauth2 as the username: %q", url)
	}
}

// A public repository and a deploy key both clone from the plain URL — a token
// in the URL of an SSH remote is no use to anybody, and a URL carrying a
// secret that nothing reads is a secret in a log for no reason.
func TestOnlyTokenAuthPutsAnythingInTheURL(t *testing.T) {
	for _, method := range []string{AuthNone, AuthKey} {
		site := &store.Site{
			Repository: "https://github.com/owner/repo.git",
			RepoAuth:   method,
			RepoToken:  "ghp_abc123",
		}

		if url := CloneURL(site); url != site.Repository {
			t.Errorf("%s auth rewrote the URL to %q", method, url)
		}
	}

	// And a token against an SSH URL is left alone: there is nowhere to put it.
	ssh := &store.Site{
		Repository: "git@github.com:owner/repo.git",
		RepoAuth:   AuthToken,
		RepoToken:  "ghp_abc123",
	}

	if url := CloneURL(ssh); url != ssh.Repository {
		t.Errorf("a token was forced into an SSH URL: %q", url)
	}
}

// Without GIT_TERMINAL_PROMPT=0 a private repository with no credentials does
// not fail — git asks for a username, finds no terminal, and waits until the
// thirty-minute command timeout. A timeout is the one error that says nothing
// about what went wrong.
func TestGitNeverWaitsForSomebodyToTypeAPassword(t *testing.T) {
	site := &store.Site{ID: 1, Domain: "example.com", Repository: "https://github.com/o/r.git"}

	for _, command := range []string{
		CloneCommand(site, "/var/www/example.com", "main"),
		FetchCommand(site, "/var/www/example.com", "main"),
	} {
		if !strings.Contains(command, "GIT_TERMINAL_PROMPT=0") {
			t.Errorf("a git command can still stop and wait for a password:\n%s", command)
		}

		// The environment has to be inside the sudo, or sudo resets it away
		// before git ever sees it.
		sudo := strings.Index(command, "sudo -u www-data")
		prompt := strings.Index(command, "GIT_TERMINAL_PROMPT=0")

		if sudo < 0 || prompt < sudo {
			t.Errorf("the environment is set before sudo, which strips it:\n%s", command)
		}
	}
}

// A deploy key is offered to ssh explicitly and nothing else is, so a key in an
// agent or in root's .ssh cannot be what a site quietly authenticates with.
func TestADeployKeySiteOffersThatKeyAndNoOther(t *testing.T) {
	site := &store.Site{ID: 7, Domain: "example.com", RepoAuth: AuthKey,
		Repository: "git@github.com:o/r.git"}

	command := CloneCommand(site, "/var/www/example.com", "main")

	for _, wanted := range []string{
		"GIT_SSH_COMMAND=", "-i /var/lib/ubuntu-panel/deploy-keys/site-7",
		"IdentitiesOnly=yes", "BatchMode=yes",
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the clone is missing %q:\n%s", wanted, command)
		}
	}

	// accept-new rather than `no`: a first connection to github.com must not
	// be a question nobody can answer, while a host key that changes later is
	// still refused.
	if !strings.Contains(command, "StrictHostKeyChecking=accept-new") {
		t.Errorf("host key checking is not set to accept-new:\n%s", command)
	}
}

// A token must not end up in .git/config, where it would sit in the working
// tree, in every backup of the site, and readable by every other site on the
// machine.
func TestATokenIsNeverLeftInTheCheckout(t *testing.T) {
	site := &store.Site{
		ID: 1, Domain: "example.com",
		Repository: "https://github.com/owner/repo.git",
		RepoAuth:   AuthToken, RepoToken: "ghp_secret",
	}

	// The clone has to use it, and then scrub the remote.
	clone := CloneCommand(site, "/var/www/example.com", "main")

	if !strings.Contains(clone, "ghp_secret") {
		t.Fatalf("the clone does not authenticate at all:\n%s", clone)
	}

	if !strings.Contains(clone, "remote set-url origin 'https://github.com/owner/repo.git'") {
		t.Errorf("the clone leaves the token in .git/config:\n%s", clone)
	}

	// The fetch passes the URL per command rather than reading the remote, so
	// nothing is stored in the first place.
	fetch := FetchCommand(site, "/var/www/example.com", "main")

	if strings.Contains(fetch, "remote set-url") {
		t.Errorf("the fetch writes the token into .git/config:\n%s", fetch)
	}

	if !strings.Contains(fetch, "ghp_secret") {
		t.Errorf("the fetch sends no credential:\n%s", fetch)
	}
}

// A `--depth 1 --branch main` clone configures itself to fetch only main, so a
// site whose branch is changed afterwards gave a fetch that succeeded and a
// reset that failed on a ref nothing had ever fetched.
func TestAFetchNamesTheBranchItWantsExplicitly(t *testing.T) {
	site := &store.Site{ID: 1, Domain: "example.com", Repository: "https://github.com/o/r.git"}

	fetch := FetchCommand(site, "/var/www/example.com", "release")

	if !strings.Contains(fetch, "+refs/heads/release:refs/remotes/origin/release") {
		t.Errorf("the fetch does not name the branch's refspec:\n%s", fetch)
	}

	if !strings.Contains(fetch, "reset --hard 'refs/remotes/origin/release'") {
		t.Errorf("the reset does not point at the ref the fetch just wrote:\n%s", fetch)
	}
}

// The intervals are a menu, and nothing shorter than five minutes: a check is
// cheap, but a repository that moved means a fetch, an install, a rebuild and a
// restart, and a one-minute interval is a site restarting more than it serves.
func TestThePullIntervalsAreAMenuWithAFloor(t *testing.T) {
	if !ValidPullSchedule("off") || !ValidPullSchedule("") {
		t.Error("off has to be a choice somebody can make")
	}

	if ValidPullSchedule("1m") || ValidPullSchedule("every tuesday") {
		t.Error("an interval nobody offered was accepted")
	}

	if NextPullAt("off", testNow()) != 0 {
		t.Error("a site that is off was given a due time, so it would be polled")
	}

	for _, entry := range PullSchedules {
		if entry.Key == "off" {
			continue
		}

		if NextPullAt(entry.Key, testNow()) <= testNow().Unix() {
			t.Errorf("%s produced a due time that is already past", entry.Key)
		}
	}
}

// Every failure git can hand back has to come out as something somebody can go
// and fix. git is answering a different question: "could not read from remote
// repository" is what a private repo says to a client with no key, and
// "repository not found" is what GitHub says to a valid token without access.
func TestGitFailuresAreTranslatedIntoSomethingActionable(t *testing.T) {
	cases := []struct {
		method string
		output string
		wanted string
	}{
		{AuthNone, "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			"private and the panel sent no credentials"},
		{AuthToken, "remote: Repository not found.\nfatal: repository not found",
			"no access to this"},
		{AuthKey, "git@github.com: Permission denied (publickey).",
			"refused the deploy key"},
		{AuthNone, "fatal: unable to access 'https://githb.com/o/r': Could not resolve host: githb.com",
			"does not resolve"},
		{AuthNone, "ssh: connect to host github.com port 22: Connection refused",
			"Port 22 outbound"},
	}

	for _, test := range cases {
		site := &store.Site{RepoAuth: test.method}

		got := explainGitFailure(site, test.output)

		if !strings.Contains(got, test.wanted) {
			t.Errorf("%s + %q\n  gave %q\n  wanted something containing %q",
				test.method, test.output, got, test.wanted)
		}

		// And git's own words are kept on the end, because they are
		// occasionally the real answer.
		if !strings.Contains(got, "git said:") {
			t.Errorf("git's own output was dropped from %q", got)
		}
	}
}

// testNow is a fixed instant, so a schedule test does not depend on the clock.
func testNow() time.Time { return time.Unix(1_770_000_000, 0).UTC() }
