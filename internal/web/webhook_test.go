package web

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "a-long-generated-webhook-secret"

func githubSignature(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func request(body string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/git/1", strings.NewReader(body))

	for key, value := range headers {
		r.Header.Set(key, value)
	}

	return r
}

// This is the one route in the panel a stranger is meant to reach, so the
// check that decides whether to act on a request is the thing most worth being
// sure about.
func TestAWebhookIsOnlyActedOnWhenItProvesWhoSentIt(t *testing.T) {
	body := `{"ref":"refs/heads/main"}`

	cases := []struct {
		what      string
		headers   map[string]string
		query     string
		authentic bool
	}{
		{"a correctly signed GitHub push",
			map[string]string{"X-Hub-Signature-256": githubSignature(body, testSecret)},
			"", true},

		{"a GitHub push signed with the wrong secret",
			map[string]string{"X-Hub-Signature-256": githubSignature(body, "not-the-secret")},
			"", false},

		{"a GitHub signature over a different body",
			map[string]string{"X-Hub-Signature-256": githubSignature(`{"ref":"x"}`, testSecret)},
			"", false},

		{"a signature that is not hex at all",
			map[string]string{"X-Hub-Signature-256": "sha256=not-hex-at-all"}, "", false},

		{"an empty signature header",
			map[string]string{"X-Hub-Signature-256": ""}, "", false},

		{"GitLab's plain token",
			map[string]string{"X-Gitlab-Token": testSecret}, "", true},

		{"GitLab's token, wrong",
			map[string]string{"X-Gitlab-Token": "wrong"}, "", false},

		{"a token in a header, for everything else",
			map[string]string{"X-Panel-Token": testSecret}, "", true},

		{"nothing at all", map[string]string{}, "", false},
	}

	for _, test := range cases {
		r := request(body, test.headers)

		if test.query != "" {
			r.URL.RawQuery = test.query
		}

		if got := webhookAuthentic(r, []byte(body), testSecret); got != test.authentic {
			t.Errorf("%s: authentic = %v, wanted %v", test.what, got, test.authentic)
		}
	}
}

// The older SHA-1 header, which GitHub still sends beside the SHA-256 one.
func TestTheOlderSHA1SignatureIsAcceptedWhenItIsAllThatIsOffered(t *testing.T) {
	body := `{"ref":"refs/heads/main"}`

	mac := hmac.New(sha1.New, []byte(testSecret))
	mac.Write([]byte(body))

	r := request(body, map[string]string{
		"X-Hub-Signature": "sha1=" + hex.EncodeToString(mac.Sum(nil)),
	})

	if !webhookAuthentic(r, []byte(body), testSecret) {
		t.Error("a valid SHA-1 signature was refused")
	}

	wrong := request(body, map[string]string{"X-Hub-Signature": "sha1=00"})

	if webhookAuthentic(wrong, []byte(body), testSecret) {
		t.Error("an invalid SHA-1 signature was accepted")
	}
}

// A query parameter is the last resort, for a host that will not send a header.
func TestATokenInTheQueryStringWorksForAHostWithNoHeaders(t *testing.T) {
	body := `{}`

	r := request(body, map[string]string{})
	r.URL.RawQuery = "token=" + testSecret

	if !webhookAuthentic(r, []byte(body), testSecret) {
		t.Error("a correct token in the query string was refused")
	}

	r.URL.RawQuery = "token=wrong"

	if webhookAuthentic(r, []byte(body), testSecret) {
		t.Error("a wrong token in the query string was accepted")
	}
}

// A repository with a dozen branches pushes constantly, and the site follows
// one of them. Deploying on every push would put a feature branch onto
// production because somebody pushed it for review.
func TestOnlyThePushedBranchTheSiteFollowsCounts(t *testing.T) {
	cases := map[string]string{
		`{"ref":"refs/heads/main"}`:            "main",
		`{"ref":"refs/heads/release/2026-01"}`: "release/2026-01",

		// A tag push is not a branch push. Empty, which the caller reads as
		// "do not know" — and a site following `main` then deploys nothing,
		// because the empty string does not match it.
		`{"ref":"refs/tags/v1.2.3"}`: "",

		// A body the panel does not recognise. Not a refusal: the signature
		// already proved who sent it, and a host with its own payload shape
		// should still be able to trigger a deploy.
		`{"something":"else"}`: "",
		`not json at all`:      "",
		``:                     "",
	}

	for body, wanted := range cases {
		if got := pushedBranch([]byte(body)); got != wanted {
			t.Errorf("pushedBranch(%q) = %q, wanted %q", body, got, wanted)
		}
	}
}

// A hook that has just been created sends a ping, and answering it is what
// makes the green tick appear in the host's own UI.
func TestTheEventTypeIsReadFromWhicheverHostSentIt(t *testing.T) {
	cases := []struct {
		header, value, wanted string
	}{
		{"X-GitHub-Event", "push", "push"},
		{"X-GitHub-Event", "ping", "ping"},
		{"X-Gitlab-Event", "Push Hook", "push"},
		{"X-Gitea-Event", "push", "push"},
	}

	for _, test := range cases {
		r := request("{}", map[string]string{test.header: test.value})

		if got := pushEvent(r); got != test.wanted {
			t.Errorf("%s: %q gave %q, wanted %q", test.header, test.value, got, test.wanted)
		}
	}

	// No header at all, which is a curl from a CI job. Empty, and the handler
	// treats that as a push rather than refusing work somebody asked for.
	if got := pushEvent(request("{}", map[string]string{})); got != "" {
		t.Errorf("a request with no event header gave %q", got)
	}
}
