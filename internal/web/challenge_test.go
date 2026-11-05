package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

func TestTheSitesIncludeTheGuardFile(t *testing.T) {
	if sites.GuardInclude != security.GuardInclude {
		t.Fatalf("the vhosts include %q, the protections write to %q", sites.GuardInclude, security.GuardInclude)
	}
}

func TestSettingsSavedBeforeTheNewProtectionsGetTheirDefaults(t *testing.T) {
	app := bootstrapApp(t, "none")
	app.Store.SetSetting(protectionSetting, `{"web_login":{"enabled":true,"max_retry":3,"find_time":600,"ban_time":-1}}`)

	p := app.protection()
	if !p.WebLogin.Enabled || p.WebLogin.MaxRetry != 3 {
		t.Errorf("the saved jail was lost: %+v", p.WebLogin)
	}
	if p.WebFlood != security.ProtectionDefaults.WebFlood || p.ConnLimit.Max != 50 || p.WAF.MaxRetry != 5 ||
		p.Countries.Mode != security.CountriesBlock || p.Countries.Codes == nil || p.Challenge.Sites == nil {
		t.Errorf("new protections have no defaults: %+v", p)
	}
}

func TestTheBrowserCheckHandsOutPasses(t *testing.T) {
	app := bootstrapApp(t, "none")
	handler := app.withChallenge(http.NotFoundHandler())

	request := func(method, body, remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+security.PanelChallengePath, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("X-Real-IP", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// Off: nothing there.
	if w := request(http.MethodGet, "", "127.0.0.1:5555"); w.Code != http.StatusNotFound {
		t.Fatalf("the check answers while off: %d", w.Code)
	}

	p := app.protection()
	p.Challenge = security.ChallengeRule{Enabled: true, Sites: []string{"example.com"}}
	app.saveProtection(p)

	// Only nginx on this machine may ask.
	if w := request(http.MethodGet, "", "198.51.100.1:5555"); w.Code != http.StatusNotFound {
		t.Fatalf("the check answers the internet directly: %d", w.Code)
	}

	w := request(http.MethodGet, "", "127.0.0.1:5555")
	var answer struct {
		Seed string `json:"seed"`
		Wait int64  `json:"wait"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || answer.Seed == "" || answer.Wait < 1000 {
		t.Fatalf("no seed: %d %s", w.Code, w.Body.String())
	}

	// Handed straight back, before the wait: refused.
	if w := request(http.MethodPost, "seed="+url.QueryEscape(answer.Seed), "127.0.0.1:5555"); w.Code != http.StatusForbidden {
		t.Fatalf("a seed handed back at once got %d", w.Code)
	}

	// One issued long enough ago passes, with the two cookies nginx checks.
	secret := app.challengeSecret()
	seed := security.ChallengeSeed(secret, "203.0.113.9", "example.com", time.Now().Add(-2*time.Second))
	w = request(http.MethodPost, "seed="+url.QueryEscape(seed), "127.0.0.1:5555")
	if w.Code != http.StatusNoContent {
		t.Fatalf("a valid seed got %d: %s", w.Code, w.Body.String())
	}

	cookies := map[string]*http.Cookie{}
	for _, cookie := range w.Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	hash, expires := cookies[security.ChallengeCookie], cookies[security.ChallengeExpiresCookie]
	if hash == nil || expires == nil || !hash.HttpOnly || !hash.Secure {
		t.Fatalf("missing or loose cookies: %v", w.Result().Cookies())
	}
	at, _ := strconv.ParseInt(expires.Value, 10, 64)
	if hash.Value != security.ChallengeToken(secret, "203.0.113.9", "example.com", at) {
		t.Error("the pass is not the one nginx will compute")
	}

	// A seed for another address is somebody else's.
	other := security.ChallengeSeed(secret, "198.51.100.7", "example.com", time.Now().Add(-2*time.Second))
	if w := request(http.MethodPost, "seed="+url.QueryEscape(other), "127.0.0.1:5555"); w.Code != http.StatusForbidden {
		t.Errorf("another address's seed got %d", w.Code)
	}
}
