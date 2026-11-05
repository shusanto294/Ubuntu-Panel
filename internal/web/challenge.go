package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
)

// The protections that need more than a switch: what has to be true before
// one turns on, the WAF's install, and the endpoint behind under attack
// mode's browser check.

// refuseProtection is why a protection cannot be turned on as asked, or nil.
func (a *App) refuseProtection(ctx context.Context, feature string, p security.Protection) error {
	behindCloudflare := func(domains []string) []string {
		wanted := map[string]bool{}
		for _, domain := range domains {
			wanted[domain] = true
		}

		edge := []string{}
		if sites, err := a.Store.Sites(ctx); err == nil {
			for _, site := range sites {
				if site.EdgeTLS() && (domains == nil || wanted[site.Domain]) {
					edge = append(edge, site.Domain)
				}
			}
		}
		return edge
	}

	switch feature {
	case "port_scan":
		if !security.ReadFirewall().Active {
			return errors.New("port scan bans read what the firewall refuses, and the firewall is off — turn it on on the Firewall tab first")
		}
		if err := security.EnableUFWLogging(); err != nil {
			return err
		}

	case "countries":
		// Without real addresses every visitor through Cloudflare is
		// Cloudflare, which no country rule may refuse.
		if edge := behindCloudflare(nil); len(edge) > 0 && !p.RealIP {
			return fmt.Errorf("%s %s behind Cloudflare, where every visitor looks like Cloudflare — "+
				"turn on real visitor addresses first", strings.Join(edge, ", "), isAre(len(edge)))
		}

	case "challenge":
		if edge := behindCloudflare(p.Challenge.Sites); len(edge) > 0 && !p.RealIP {
			return fmt.Errorf("%s %s behind Cloudflare, so every visitor would share Cloudflare's addresses and "+
				"no pass would hold — turn on real visitor addresses first, or use Cloudflare's own Under Attack mode",
				strings.Join(edge, ", "), isAre(len(edge)))
		}
		if !security.SecureLinkAvailable() {
			return errors.New("this nginx was built without the secure_link module, which checks the browser passes")
		}

	case "waf":
		if !security.WAFInstalled() {
			return errors.New("the WAF is not installed yet — press Install the WAF first")
		}
	}

	return nil
}

func isAre(count int) string {
	if count == 1 {
		return "is"
	}
	return "are"
}

// knownSites is the sites a form named, by domain, that exist.
func (a *App) knownSites(ctx context.Context, text string) []string {
	asked := map[string]bool{}
	for _, domain := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	}) {
		asked[domain] = true
	}

	found := []string{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if asked[strings.ToLower(site.Domain)] {
				found = append(found, site.Domain)
			}
		}
	}

	return found
}

// installWAF queues ModSecurity and the Core Rule Set, like the Apps page.
func (a *App) installWAF(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	if security.WAFInstalled() {
		a.securityBack(w, r, "The WAF is installed. Turn it on below.", false)
		return
	}

	if !a.Store.ServiceInstalled("nginx") {
		a.securityBack(w, r, "The WAF runs inside nginx, which is not installed.", true)
		return
	}

	if a.Catalog.Queue([]string{"modsecurity"}, true) == 0 && !a.Store.ServicesPending() {
		a.securityBack(w, r, "The WAF could not be queued for install.", true)
		return
	}

	a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

	a.securityBack(w, r, "Installing the WAF (ModSecurity and the OWASP Core Rule Set) — follow it on the Apps page "+
		"or in Logs. Turn it on here when it finishes.", false)
}

// wafInstalling is whether the WAF's packages are queued or going in.
func (a *App) wafInstalling(ctx context.Context) bool {
	service, err := a.Store.FindService(ctx, "modsecurity")
	return err == nil && (service.Status == "queued" || service.Status == "installing")
}

// ------------------------------------------------------------------ the check

// challengeSecret signs the browser passes; made once, kept in the settings.
func (a *App) challengeSecret() string {
	if secret := a.Store.Setting(challengeSecretKey, ""); len(secret) >= 32 {
		return secret
	}

	buffer := make([]byte, 24)
	rand.Read(buffer)
	secret := hex.EncodeToString(buffer)
	a.Store.SetSetting(challengeSecretKey, secret)

	return secret
}

// challengeState is what the endpoint needs, kept in memory: under attack it
// is asked hundreds of times a second, and the settings are a database read.
type challengeCache struct {
	sync.Mutex
	at      time.Time
	enabled bool
	secret  string
}

var challengeState challengeCache

func (c *challengeCache) forget() {
	c.Lock()
	c.at = time.Time{}
	c.Unlock()
}

func (a *App) challengeSettings() (bool, string) {
	challengeState.Lock()
	defer challengeState.Unlock()

	if time.Since(challengeState.at) > 30*time.Second {
		p := a.protection()
		challengeState.enabled = p.Challenge.Enabled
		challengeState.secret = ""
		if p.Challenge.Enabled {
			challengeState.secret = a.challengeSecret()
		}
		challengeState.at = time.Now()
	}

	return challengeState.enabled, challengeState.secret
}

// withChallenge answers the browser check's endpoint before the session
// layer: a visitor to a site is not a panel user, and a flood of them must not
// become a flood of session rows.
func (a *App) withChallenge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != security.PanelChallengePath {
			next.ServeHTTP(w, r)
			return
		}
		a.challenge(w, r)
	})
}

// challenge hands a browser a seed (GET) and, given it back after the wait,
// its pass (POST): the two cookies nginx checks. Only from nginx on this
// machine, which says who the visitor is in X-Real-IP.
func (a *App) challenge(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		http.NotFound(w, r)
		return
	}

	enabled, secret := a.challengeSettings()
	address := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if !enabled || net.ParseIP(address) == nil {
		http.NotFound(w, r)
		return
	}

	hostname := strings.ToLower(r.Host)
	if name, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = name
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"seed": security.ChallengeSeed(secret, address, hostname, time.Now()),
			"wait": security.ChallengeWait.Milliseconds(),
		})

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !security.ValidChallengeSeed(secret, address, hostname, r.PostForm.Get("seed"), time.Now()) {
			http.Error(w, "the check did not pass", http.StatusForbidden)
			return
		}

		expires := time.Now().Add(security.ChallengeLifetime).Unix()
		secure := r.Header.Get("X-Forwarded-Proto") == "https"

		for name, value := range map[string]string{
			security.ChallengeCookie:        security.ChallengeToken(secret, address, hostname, expires),
			security.ChallengeExpiresCookie: strconv.FormatInt(expires, 10),
		} {
			http.SetCookie(w, &http.Cookie{
				Name: name, Value: value, Path: "/", MaxAge: int(security.ChallengeLifetime.Seconds()),
				HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
			})
		}

		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
