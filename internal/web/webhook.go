package web

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"net/http"
	"strings"
)

// The git webhook: a push at the repository, and the site deploys itself.
//
// This is the one route in the panel that an unauthenticated stranger is meant
// to reach, which is the whole of what makes it worth care. Everything it does
// is behind a constant-time check of a signature over the body, the body is
// read with a hard limit, and the most it can ever cause is a pull of a
// repository somebody already configured — no payload field decides anything
// except which branch was pushed, and that is compared rather than used.

// maxWebhookBody is where the read stops.
//
// GitHub's push payloads are a few kilobytes and can reach a megabyte on a
// commit touching thousands of files. A limit is not optional on a public
// endpoint: without one, the body is whatever a stranger decided to send and
// the panel holds all of it in memory while computing an HMAC over it.
const maxWebhookBody = 2 << 20

// gitWebhook is POST /webhooks/git/{site}.
//
// Public in the route table, and it has to be: a provider sends no cookie. It
// authenticates itself on every request and answers 401 to anything it cannot
// verify.
func (a *App) gitWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Not an error worth a body. A browser that followed the URL out of
		// curiosity gets nothing to read, and a provider's "test" ping gets a
		// method it can understand.
		w.Header().Set("Allow", "POST")
		http.Error(w, "This endpoint takes a POST from a git host.", http.StatusMethodNotAllowed)

		return
	}

	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))

	// The same answer for a site that does not exist and a site with no
	// webhook configured. A stranger walking the ids learns nothing about
	// which of them are real.
	if err != nil || site.Status == "deleting" || site.WebhookSecret == "" {
		http.Error(w, "unauthorised", http.StatusUnauthorized)

		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "could not read the request", http.StatusBadRequest)

		return
	}

	if !webhookAuthentic(r, body, site.WebhookSecret) {
		a.Log.Warn("a git webhook was refused", "site", site.Domain,
			"from", r.Header.Get("X-Forwarded-For"))

		http.Error(w, "unauthorised", http.StatusUnauthorized)

		return
	}

	// A ping rather than a push. GitHub sends one when the hook is created,
	// and answering it with "I pulled nothing" is what makes the green tick
	// appear next to the hook in its own UI.
	if event := pushEvent(r); event != "" && event != "push" {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"note": "Received. The panel acts on push events; this was a " + event + ".",
		})

		return
	}

	// Which branch moved. A repository with a dozen branches pushes constantly
	// and the site follows one of them — deploying on all of them would put a
	// feature branch onto production because somebody pushed it for review.
	branch := orDefault(site.Branch, "main")

	if pushed := pushedBranch(body); pushed != "" && pushed != branch {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"note": "Received a push to " + pushed + "; this site follows " + branch +
				", so nothing was deployed.",
		})

		return
	}

	if strings.TrimSpace(site.Repository) == "" {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"note": "This site is not deployed from a repository, so there was nothing to pull.",
		})

		return
	}

	// Already building. Queueing a second pull behind the first would deploy
	// the same commit twice, and a busy repository could queue a dozen.
	if site.Status == "deploying" {
		a.writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"note": "A deployment is already running for this site; this push will be " +
				"picked up by it or by the next one.",
		})

		return
	}

	if err := a.pullSiteNow(r.Context(), site); err != nil {
		a.Log.Error("could not queue a webhook pull", "site", site.Domain, "error", err)

		http.Error(w, "could not queue the pull", http.StatusInternalServerError)

		return
	}

	a.Log.Info("queued a pull from a webhook", "site", site.Domain, "branch", branch)

	a.writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":   true,
		"note": "Pulling " + branch + " into " + site.Domain + ".",
	})
}

// webhookAuthentic reports whether this request really came from somebody
// holding the secret.
//
// Three forms, because the three hosts people use do it three different ways
// and none of them is configurable at their end:
//
//   - GitHub signs the body: X-Hub-Signature-256, HMAC-SHA256, hex, prefixed
//     `sha256=`. Older installations send X-Hub-Signature with SHA-1.
//   - GitLab sends the secret itself: X-Gitlab-Token.
//   - Everything else — Gitea, Bitbucket's own hooks, a curl in a CI job —
//     gets a plain header or a query parameter.
//
// A signature is the better of the two and is checked first: the secret itself
// never crosses the wire, so it cannot be read out of a proxy log on the way.
func webhookAuthentic(r *http.Request, body []byte, secret string) bool {
	if signature := r.Header.Get("X-Hub-Signature-256"); signature != "" {
		return hmacMatches(sha256.New, body, secret, strings.TrimPrefix(signature, "sha256="))
	}

	if signature := r.Header.Get("X-Hub-Signature"); signature != "" {
		// SHA-1, and only because GitHub still sends it alongside the SHA-256
		// header for compatibility. Accepted when it is the only one offered.
		return hmacMatches(sha1.New, body, secret, strings.TrimPrefix(signature, "sha1="))
	}

	// Gitea's own header, same construction as GitHub's.
	if signature := r.Header.Get("X-Gitea-Signature"); signature != "" {
		return hmacMatches(sha256.New, body, secret, signature)
	}

	for _, given := range []string{
		r.Header.Get("X-Gitlab-Token"),
		r.Header.Get("X-Panel-Token"),
		r.URL.Query().Get("token"),
	} {
		if given != "" {
			// Constant time even here. The comparison is on a secret, and a
			// byte-at-a-time one over a public endpoint is a secret somebody
			// can read a character at a time.
			return hmac.Equal([]byte(given), []byte(secret))
		}
	}

	return false
}

// hmacMatches checks a hex signature over the body, in constant time.
func hmacMatches(newHash func() hash.Hash, body []byte, secret, signature string) bool {
	given, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}

	expected := hmac.New(newHash, []byte(secret))
	expected.Write(body)

	return hmac.Equal(expected.Sum(nil), given)
}

// pushEvent is what the host says this delivery is.
func pushEvent(r *http.Request) string {
	for _, header := range []string{"X-GitHub-Event", "X-Gitea-Event", "X-Gitlab-Event"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return strings.ToLower(strings.TrimSuffix(
				strings.TrimSuffix(value, " Hook"), " hook"))
		}
	}

	return ""
}

// pushedBranch is the branch out of a push payload, or empty when the body is
// not one the panel recognises.
//
// `ref` is `refs/heads/<branch>` on GitHub, Gitea and GitLab alike. A tag push
// is `refs/tags/...` and returns empty, which the caller treats as "do not
// know" rather than as a branch — a tag push to a site following `main` should
// deploy nothing, and it does, because the ref does not match.
func pushedBranch(body []byte) string {
	var payload struct {
		Ref string `json:"ref"`
	}

	// A body that is not JSON is not a reason to refuse: the signature already
	// proved who sent it, and a host with a payload shape nobody here knows
	// should still be able to trigger a deploy.
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	if !strings.HasPrefix(payload.Ref, "refs/heads/") {
		return ""
	}

	return strings.TrimPrefix(payload.Ref, "refs/heads/")
}
