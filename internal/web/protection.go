package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/serverlog"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// The Protection tab: the optional protections in security/protect.go, each
// switched on and off on its own. See there for what each one is; this is where they are stored, applied and kept applied.

const (
	protectionSetting     = "security_protection"
	kernelPreviousSetting = "security_kernel_previous"
	accessLogsSetting     = "security_access_logs"
	protectFailSetting    = "security_protection_failures"
	pingPreviousSetting   = "security_ping_previous"
	challengeSecretKey    = "security_challenge_secret"
	countriesFetchedKey   = "security_countries_refreshed"
)

// protectionFailure is why a protection could not be switched, kept on its
// card until it is switched successfully.
//
// It used to be a flash, at the top of the page, gone after five seconds —
// and the person who pressed Turn on is scrolled down to the card, so what
// they saw was the button saying Turn on again and no reason why.
type protectionFailure struct {
	Message string `json:"message"`
	On      bool   `json:"on"`
	At      int64  `json:"at"`
}

func (a *App) protectionFailures() map[string]protectionFailure {
	failures := map[string]protectionFailure{}
	json.Unmarshal([]byte(a.Store.Setting(protectFailSetting, "{}")), &failures)

	return failures
}

// recordProtection notes how switching feature went: the reason when it
// failed, nothing when it worked.
func (a *App) recordProtection(feature string, on bool, err error) {
	failures := a.protectionFailures()

	if err == nil {
		if _, ok := failures[feature]; !ok {
			return
		}
		delete(failures, feature)
	} else {
		failures[feature] = protectionFailure{Message: err.Error(), On: on, At: time.Now().Unix()}
	}

	encoded, _ := json.Marshal(failures)
	a.Store.SetSetting(protectFailSetting, string(encoded))
}

// protection is what was saved, over the defaults, so a field added later has
// a value on machines that saved before it existed.
func (a *App) protection() security.Protection {
	p := security.ProtectionDefaults

	if stored := a.Store.Setting(protectionSetting, ""); stored != "" {
		json.Unmarshal([]byte(stored), &p)
	}

	// A jail that is off is only the values it would be turned on with, and
	// a saved copy of old defaults is not a choice anybody made: it shows
	// the current ones. One that is on keeps what it is running with.
	for _, jail := range []struct {
		rule     *security.JailRule
		fallback security.JailRule
	}{
		{&p.WebLogin, security.ProtectionDefaults.WebLogin},
		{&p.WebScan, security.ProtectionDefaults.WebScan},
		{&p.Mail, security.ProtectionDefaults.Mail},
		{&p.Recidive, security.ProtectionDefaults.Recidive},
		{&p.WebFlood, security.ProtectionDefaults.WebFlood},
		{&p.Tools, security.ProtectionDefaults.Tools},
		{&p.PortScan, security.ProtectionDefaults.PortScan},
	} {
		if !jail.rule.Enabled {
			*jail.rule = jail.fallback
		}
	}

	if !p.ConnLimit.Enabled {
		p.ConnLimit = security.ProtectionDefaults.ConnLimit
	}
	if !p.WAF.Enabled {
		p.WAF = security.ProtectionDefaults.WAF
	}
	if p.Countries.Mode == "" {
		p.Countries.Mode = security.CountriesBlock
	}
	if p.Countries.Codes == nil {
		p.Countries.Codes = []string{}
	}
	if p.Challenge.Sites == nil {
		p.Challenge.Sites = []string{}
	}

	syncPathRules(p)

	return p
}

var pathRulesKey struct {
	sync.Mutex
	key string
}

// syncPathRules gives the traffic reader the scanner protection's lists, so
// the Logs page and the automatic bans flag what the jails ban.
func syncPathRules(p security.Protection) {
	secrets, scans := p.Secrets(), p.Scans()
	key := strings.Join(secrets, "\n") + "\x00" + strings.Join(scans, "\n")

	pathRulesKey.Lock()
	defer pathRulesKey.Unlock()
	if key == pathRulesKey.key {
		return
	}
	pathRulesKey.key = key

	expressions := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, pattern := range list {
			if security.ValidatePathPattern(pattern) == "" {
				out = append(out, security.PathPatternRegexp(pattern))
			}
		}
		return out
	}
	weblog.SetPaths(expressions(secrets), expressions(scans))
}

func (a *App) saveProtection(p security.Protection) {
	encoded, _ := json.Marshal(p)
	a.Store.SetSetting(protectionSetting, string(encoded))
	challengeState.forget()
}

// nginxEnv is what the nginx file is rendered with: the never-ban list, and
// the hostnames whose clients are busy by design — the panel itself, and the
// applications that sync or poll.
//
// And for the per-site protections: the hostnames under attack mode, the
// secret their passes are signed with, and the WAF's configuration.
func (a *App) nginxEnv(ctx context.Context, p security.Protection) security.NginxEnv {
	env := security.NginxEnv{IgnoreIPs: a.trustedList(ctx), BindPort: a.Config.BindPort}

	if domain := a.Store.Setting("panel_domain", ""); domain != "" {
		env.ExemptHosts = append(env.ExemptHosts, domain)
	}

	challenged := map[string]bool{}
	for _, domain := range p.Challenge.Sites {
		challenged[domain] = true
	}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.Type == "nextcloud" || site.Type == "n8n" {
				env.ExemptHosts = append(env.ExemptHosts, site.Hostnames()...)
			}
			if challenged[site.Domain] {
				env.ChallengeHosts = append(env.ChallengeHosts, site.Hostnames()...)
			}
		}
	}

	if p.Challenge.Enabled {
		env.ChallengeSecret = a.challengeSecret()
	}

	if p.WAF.Enabled {
		if waf, err := security.WAFEnvFor(env.IgnoreIPs, env.ExemptHosts); err == nil {
			env.WAFRules = security.WAFFile(p.WAF, waf)
		}
	}

	return env
}

// applyProtection writes the jails and the nginx file for p. The kernel
// settings are applied by their own switch, because turning them off needs
// the values they replaced.
func (a *App) applyProtection(ctx context.Context, p security.Protection) error {
	// The country lists first: the nginx file includes them.
	if p.Countries.Enabled && !sameStrings(security.CountriesOnDisk(), p.Countries.Codes) {
		if err := security.WriteCountries(p.Countries.Codes); err != nil {
			return err
		}
		a.Store.SetSetting(countriesFetchedKey, strconv.FormatInt(time.Now().Unix(), 10))
	}

	if err := security.ApplyJails(p, a.trustedList(ctx)); err != nil {
		return err
	}

	if err := security.ApplyNginx(security.NginxFiles(p, a.nginxEnv(ctx, p))); err != nil {
		return err
	}

	a.Store.SetSetting(accessLogsSetting, security.AccessLogs())
	securityCache.forget()

	return nil
}

// EnsureProtection puts back at start what is switched on and missing — an
// update that changed a filter, a never-ban list edited while fail2ban was
// away, a sysctl file somebody deleted.
func (a *App) EnsureProtection(ctx context.Context) {
	p := a.protection()

	// The malware scan is gone (2.11.0); its last result and alert counter
	// would otherwise sit in the settings table for ever.
	a.Store.ForgetSetting("security_scan_result")
	a.Store.ForgetSetting("security_scan_unreported")

	// The panel's own lines left the Server logs tab in 2.15.1; the ones
	// collected before that go with them, once.
	if a.Store.Setting("server_events_panel_cleared", "") == "" {
		removed := a.Store.ForgetServerEvents(ctx,
			[]string{serverlog.SourceSystem, serverlog.SourceAuth, serverlog.SourceNginx},
			[]string{"ubuntu-panel", "Ubuntu Panel", "Reloading ", "Reloaded ", "signal process started"},
			serverlog.PanelCaused)
		a.Store.SetSetting("server_events_panel_cleared", "1")
		a.Log.Info("removed the panel's own lines from the server logs", "events", removed)
	}

	// Every site's server blocks include the per-site protections, whether
	// or not any is on, so turning one on never has to touch a vhost.
	if a.Store.ServiceInstalled("nginx") {
		if err := a.Sites.EnsureGuardIncludes(ctx); err != nil {
			a.Log.Error("could not add the protections' include to the vhosts", "error", err)
		}
	}

	if p.AnyJail() || p.AnyNginx() || security.NginxPresent() {
		if p.Countries.Enabled && !sameStrings(security.CountriesOnDisk(), p.Countries.Codes) {
			if err := security.WriteCountries(p.Countries.Codes); err != nil {
				a.Log.Error("could not download the country lists", "error", err)
			}
		}

		want := security.NginxFiles(p, a.nginxEnv(ctx, p))

		if !security.NginxCurrent(want) {
			if err := security.ApplyNginx(want); err != nil {
				a.Log.Error("could not apply the nginx protections", "error", err)
			}
		}

		if p.AnyJail() && proc.Which("fail2ban-client") && !security.JailsCurrent(p, a.trustedList(ctx)) {
			if err := security.ApplyJails(p, a.trustedList(ctx)); err != nil {
				a.Log.Error("could not apply the fail2ban protections", "error", err)
			}
			a.Store.SetSetting(accessLogsSetting, security.AccessLogs())
		}
	}

	if p.EdgeOnly && !security.EdgeOnlyApplied() {
		if err := security.ApplyEdgeOnly(true); err != nil {
			a.Log.Error("could not keep the web ports to Cloudflare only", "error", err)
		}
	}

	if p.Kernel && !security.KernelApplied() {
		if err := security.ApplyKernel(true, nil); err != nil {
			a.Log.Error("could not apply the kernel network settings", "error", err)
		}
	}

	// A PHP version installed since is hardened like the rest.
	if p.PHPHarden && !security.PHPHardeningApplied() {
		if err := security.ApplyPHPHardening(true); err != nil {
			a.Log.Error("could not harden PHP", "error", err)
		}
	}

	// Port forwards survive a firewall reset.
	if forwards := a.forwards(); len(forwards) > 0 {
		if err := security.ApplyForwards(forwards); err != nil {
			a.Log.Error("could not put the port forwards back", "error", err)
		}
	}

	if p.NoPing && !security.PingApplied() {
		if err := security.ApplyNoPing(true, nil); err != nil {
			a.Log.Error("could not stop the server answering ping", "error", err)
		}
	}
}

// sameStrings is whether two lists hold the same strings, in any order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		if counts[value]--; counts[value] < 0 {
			return false
		}
	}
	return true
}

var lastProtectionCheck struct {
	sync.Mutex
	at time.Time
}

// checkProtection is the every-minute clock's: every ten minutes, a site added
// since fail2ban started is a log the web jails are not reading yet, and the
// jails are reloaded to pick it up.
func (a *App) checkProtection(ctx context.Context) {
	lastProtectionCheck.Lock()
	if time.Since(lastProtectionCheck.at) < 10*time.Minute {
		lastProtectionCheck.Unlock()
		return
	}
	lastProtectionCheck.at = time.Now()
	lastProtectionCheck.Unlock()

	// An address that signed in, or one aged out of the thirty days.
	a.syncTrusted(ctx)

	// Turning the firewall off and on again, or a reset, opens the web ports to
	// everyone; Cloudflare only puts that right.
	if p := a.protection(); p.EdgeOnly && !security.EdgeOnlyApplied() {
		if err := security.ApplyEdgeOnly(true); err != nil {
			a.Log.Warn("could not keep the web ports to Cloudflare only", "error", err)
		}
	}

	// A domain allowed to reach a database may have moved.
	if a.Store.ServiceInstalled("mysql") && a.Databases.HasDomains(ctx) {
		if err := a.Databases.SyncMySQLAccess(ctx, true); err != nil {
			a.Log.Warn("could not refresh the MariaDB access settings", "error", err)
		}
	}

	p := a.protection()

	if p.WebLogin.Enabled || p.WebScan.Enabled || p.WebFlood.Enabled || p.Tools.Enabled || p.WAF.Bans() {
		if logs := security.AccessLogs(); logs != a.Store.Setting(accessLogsSetting, "") {
			security.ReloadJails()
			a.Store.SetSetting(accessLogsSetting, logs)
		}
	}

	// Address ranges move between countries; the lists are fetched again
	// once a week.
	if p.Countries.Enabled {
		fetched, _ := strconv.ParseInt(a.Store.Setting(countriesFetchedKey, "0"), 10, 64)
		if time.Since(time.Unix(fetched, 0)) > 7*24*time.Hour {
			if err := security.RefreshCountries(p.Countries.Codes); err != nil {
				a.Log.Warn("could not refresh the country lists", "error", err)
			} else {
				a.Store.SetSetting(countriesFetchedKey, strconv.FormatInt(time.Now().Unix(), 10))
			}
		}
	}
}

// ------------------------------------------------------------------ handler

// securityProtection switches one protection on or off, with its settings.
func (a *App) securityProtection(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	p := a.protection()
	before := p
	enabled := boolInput(input, "enabled", false)
	feature := input["feature"]

	number := func(key string, fallback int) int {
		value, err := strconv.Atoi(strings.TrimSpace(input[key]))
		if err != nil {
			return fallback
		}
		return value
	}

	jail := func(current security.JailRule) (security.JailRule, bool) {
		next := security.JailRule{
			Enabled:  enabled,
			MaxRetry: number("max_retry", current.MaxRetry),
			FindTime: number("find_time", current.FindTime),
			BanTime:  number("ban_time", current.BanTime),
		}

		// 0 is how some people say "the first one": fail2ban's 1.
		if next.MaxRetry == 0 {
			next.MaxRetry = 1
		}

		if problems := next.Validate(); len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			return current, false
		}

		return next, true
	}

	rate := func(current security.Rate, perSecond bool) (security.Rate, bool) {
		next := security.Rate{Enabled: enabled, Rate: number("rate", current.Rate), Burst: number("burst", current.Burst)}

		if problems := next.Validate(perSecond); len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			return current, false
		}

		return next, true
	}

	// The scanner protection's lists: what is banned on the first request,
	// and what counts as scanning. Saved whether or not the protection is on;
	// applied at once when it is.
	if feature == "scan_paths" {
		a.saveScanPaths(w, r, p, input)
		return
	}

	ok := true
	label := ""

	switch feature {
	case "web_login":
		p.WebLogin, ok = jail(p.WebLogin)
		label = "Website login protection"
	case "web_scan":
		p.WebScan, ok = jail(p.WebScan)
		label = "Scanner protection"
	case "mail":
		p.Mail, ok = jail(p.Mail)
		label = "Mail login protection"
	case "recidive":
		p.Recidive, ok = jail(p.Recidive)
		label = "Repeat-offender bans"
	case "web_flood":
		p.WebFlood, ok = jail(p.WebFlood)
		label = "Flood bans"
	case "tools":
		p.Tools, ok = jail(p.Tools)
		label = "Attack tool bans"
	case "port_scan":
		p.PortScan, ok = jail(p.PortScan)
		label = "Port scan bans"
	case "conn_limit":
		next := security.ConnLimit{Enabled: enabled, Max: number("max", p.ConnLimit.Max)}
		if problems := next.Validate(); len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			ok = false
		} else {
			p.ConnLimit = next
		}
		label = "The connection limit"
	case "countries":
		next := security.CountryRule{Enabled: enabled, Mode: input["mode"], Codes: p.Countries.Codes}
		if _, sent := input["codes"]; sent {
			codes, problem := security.CleanCountries(input["codes"])
			if problem != "" {
				sess.Error("codes", problem)
				inertia.Back(w, r, "/security?tab=protection")
				return
			}
			next.Codes = codes
		}
		if next.Mode == "" {
			next.Mode = p.Countries.Mode
		}
		if problems := next.Validate(); enabled && len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			ok = false
		} else {
			p.Countries = next
		}
		label = "Country rules"
	case "challenge":
		next := security.ChallengeRule{Enabled: enabled, Sites: p.Challenge.Sites}
		if _, sent := input["sites"]; sent {
			next.Sites = a.knownSites(r.Context(), input["sites"])
		}
		if problems := next.Validate(); enabled && len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			ok = false
		} else {
			p.Challenge = next
		}
		label = "Under attack mode"
	case "waf":
		next := security.WAFRule{
			Enabled:  enabled,
			Block:    boolInput(input, "block", p.WAF.Block),
			MaxRetry: number("max_retry", p.WAF.MaxRetry),
			FindTime: number("find_time", p.WAF.FindTime),
			BanTime:  number("ban_time", p.WAF.BanTime),
		}
		if problems := next.Validate(); len(problems) > 0 {
			for field, message := range problems {
				sess.Error(field, message)
			}
			ok = false
		} else {
			p.WAF = next
		}
		label = "The web application firewall"
	case "no_ping":
		label = "Ignoring ping"
	case "waf_install":
		a.installWAF(w, r)
		return
	case "watch":
		label = "The backdoor watch"
	case "integrity":
		label = "The code change monitor"
	case "php_harden":
		label = "PHP hardening"
	case "compilers":
		label = "Compilers for administrators only"
	case "recommended":
		a.securityRecommended(w, r)
		return
	case "rate_login":
		p.RateLogin, ok = rate(p.RateLogin, false)
		label = "The login rate limit"
	case "rate_flood":
		p.RateFlood, ok = rate(p.RateFlood, true)
		label = "The request flood limit"
	case "real_ip":
		p.RealIP = enabled
		label = "Real visitor addresses behind Cloudflare"
	case "kernel":
		label = "Network hardening"
	case "edge_only":
		label = "Only Cloudflare reaches the websites"
	default:
		a.securityBack(w, r, "That is not one of the protections.", true)
		return
	}

	if !ok {
		inertia.Back(w, r, "/security?tab=protection")
		return
	}

	// The firewall, not nginx or fail2ban. Refused while a site or the panel
	// is reached without Cloudflare: it would stop answering.
	if feature == "edge_only" {
		if enabled {
			if exposed := security.NotBehindCloudflare(a.webHostnames(r.Context())); len(exposed) > 0 {
				err := fmt.Errorf("these are not proxied through Cloudflare, so they would stop working: %s — "+
					"turn on the orange cloud for them in Cloudflare's DNS first", strings.Join(exposed, ", "))
				a.recordProtection(feature, enabled, err)
				a.securityBack(w, r, err.Error(), true)
				return
			}
		}

		if err := security.ApplyEdgeOnly(enabled); err != nil {
			a.recordProtection(feature, enabled, err)
			a.securityBack(w, r, err.Error(), true)
			return
		}

		p.EdgeOnly = enabled
		a.saveProtection(p)
		a.recordProtection(feature, enabled, nil)
		a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
		a.securityBack(w, r, label+map[bool]string{true: " — ports 80 and 443 now refuse everyone else.",
			false: " is off; ports 80 and 443 are open to everyone again."}[enabled], false)
		return
	}

	if enabled {
		if refusal := a.refuseProtection(r.Context(), feature, p); refusal != nil {
			a.recordProtection(feature, enabled, refusal)
			a.securityBack(w, r, refusal.Error(), true)
			return
		}
	}

	// The machine-level switches: each applies itself.
	switch feature {
	case "watch", "integrity", "php_harden", "compilers":
		if err := a.applySwitch(r.Context(), feature, enabled); err != nil {
			a.recordProtection(feature, enabled, err)
			a.securityBack(w, r, err.Error(), true)
			return
		}

		switch feature {
		case "watch":
			p.Watch = enabled
		case "integrity":
			p.Integrity = enabled
		case "php_harden":
			p.PHPHarden = enabled
		case "compilers":
			p.Compilers = enabled
		}

		a.saveProtection(p)
		a.recordProtection(feature, enabled, nil)
		a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
		a.securityBack(w, r, label+map[bool]string{true: " is on.", false: " is off."}[enabled], false)
		return
	}

	// Ping is the kernel's too, and keeps what it replaces in the same way.
	if feature == "no_ping" {
		if enabled && !before.NoPing {
			encoded, _ := json.Marshal(security.PingValues())
			a.Store.SetSetting(pingPreviousSetting, string(encoded))
		}

		var previous map[string]string
		json.Unmarshal([]byte(a.Store.Setting(pingPreviousSetting, "{}")), &previous)

		if err := security.ApplyNoPing(enabled, previous); err != nil {
			a.recordProtection(feature, enabled, err)
			a.securityBack(w, r, err.Error(), true)
			return
		}

		p.NoPing = enabled
		a.saveProtection(p)
		a.recordProtection(feature, enabled, nil)
		a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
		a.securityBack(w, r, map[bool]string{true: "The server no longer answers ping.",
			false: "The server answers ping again."}[enabled], false)
		return
	}

	// The kernel settings keep the values they replace, to put back.
	if feature == "kernel" {
		if enabled {
			if !before.Kernel {
				encoded, _ := json.Marshal(security.KernelValues())
				a.Store.SetSetting(kernelPreviousSetting, string(encoded))
			}
		}

		var previous map[string]string
		json.Unmarshal([]byte(a.Store.Setting(kernelPreviousSetting, "{}")), &previous)

		if err := security.ApplyKernel(enabled, previous); err != nil {
			a.recordProtection(feature, enabled, err)
			a.securityBack(w, r, err.Error(), true)
			return
		}

		p.Kernel = enabled
		a.saveProtection(p)
		a.recordProtection(feature, enabled, nil)
		a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
		a.securityBack(w, r, label+map[bool]string{true: " is on.", false: " is off; the previous values are back."}[enabled], false)
		return
	}

	if err := a.applyProtection(r.Context(), p); err != nil {
		// What is on disk is what was there before; the setting says so too.
		a.applyProtection(r.Context(), before)
		a.recordProtection(feature, enabled, err)
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.saveProtection(p)
	a.recordProtection(feature, enabled, nil)
	a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))

	message := label + " is off."
	if enabled {
		message = label + " is on."
	}

	a.securityBack(w, r, message, false)
}

// saveScanPaths saves the two lists the form sends, one pattern a line. A
// list the same as the default is stored as the default, so the patterns a
// later version adds reach it.
func (a *App) saveScanPaths(w http.ResponseWriter, r *http.Request, p security.Protection, input map[string]string) {
	sess := session.FromContext(r.Context())
	before := p

	secrets, problem := security.CleanPathPatterns(input["secret_paths"])
	if problem != "" {
		sess.Error("secret_paths", problem)
		inertia.Back(w, r, "/security?tab=protection")
		return
	}

	scans, problem := security.CleanPathPatterns(input["scan_paths"])
	if problem != "" {
		sess.Error("scan_paths", problem)
		inertia.Back(w, r, "/security?tab=protection")
		return
	}

	p.SecretPaths, p.ScanPaths = secrets, scans
	if security.SamePatterns(secrets, security.DefaultSecretPaths) {
		p.SecretPaths = nil
	}
	if security.SamePatterns(scans, security.DefaultScanPaths) {
		p.ScanPaths = nil
	}

	if p.WebScan.Enabled {
		if err := a.applyProtection(r.Context(), p); err != nil {
			a.applyProtection(r.Context(), before)
			a.securityBack(w, r, err.Error(), true)
			return
		}
	}

	a.saveProtection(p)
	syncPathRules(p)
	a.Log.Info("scanner paths changed", "secret", len(secrets), "scan", len(scans), "ip", session.ClientIP(r))

	message := "The scanner paths are saved."
	if p.WebScan.Enabled {
		message = "The scanner paths are saved and in force."
	}
	a.securityBack(w, r, message, false)
}

// ------------------------------------------------------------------ props

// protectJailKinds are what the Banned IPs tab calls each optional jail's
// bans.
var protectJailKinds = map[string]string{
	security.JailWebLogin:  "web_login",
	security.JailWebScan:   "web_scan",
	security.JailWebSecret: "web_scan",
	security.JailMail:      "mail",
	security.JailMailIMAP:  "mail",
	security.JailRecidive:  "recidive",
	security.JailWebFlood:  "web_flood",
	security.JailWebTools:  "tools",
	security.JailWebWAF:    "waf",
	security.JailPortScan:  "port_scan",
}

func (a *App) protectionProps(ctx context.Context, snap *securitySnapshot) map[string]any {
	p := a.protection()

	edgeSites := []string{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.EdgeTLS() {
				edgeSites = append(edgeSites, site.Domain)
			}
		}
	}

	jails := map[string]any{}
	for jail, status := range snap.Protect {
		jails[jail] = status
	}

	type siteChoice struct {
		Domain string `json:"domain"`
		Edge   bool   `json:"edge"`
	}
	siteChoices := []siteChoice{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			siteChoices = append(siteChoices, siteChoice{Domain: site.Domain, Edge: site.EdgeTLS()})
		}
	}

	return map[string]any{
		"settings": p,
		"defaults": security.ProtectionDefaults,
		"failures": a.protectionFailures(),
		"jails":    jails,
		"ready": map[string]bool{
			"fail2ban": proc.Which("fail2ban-client"),
			"nginx":    a.Store.ServiceInstalled("nginx"),
			"mail":     a.Store.ServiceInstalled("mail"),
			"firewall": snap.Firewall.Active,
			"waf":      security.WAFInstalled(),
		},
		"waf_installing":    a.wafInstalling(ctx),
		"sites":             siteChoices,
		"countries_at":      a.Store.Setting(countriesFetchedKey, ""),
		"kernel_applied":    p.Kernel && security.KernelApplied(),
		"php_applied":       p.PHPHarden && security.PHPHardeningApplied(),
		"locks":             a.lockProps(ctx),
		"watch_changes":     a.watchChanges(),
		"code_changes":      a.codeChanges(),
		"cloudflare_attack": a.cloudflareAttackOn(),
		"ping_applied":      p.NoPing && security.PingApplied(),
		"edge_applied":      p.EdgeOnly && security.EdgeOnlyApplied(),
		"edge_sites":        edgeSites,
		"paths": map[string]any{
			"secret":         p.Secrets(),
			"scan":           p.Scans(),
			"default_secret": security.DefaultSecretPaths,
			"default_scan":   security.DefaultScanPaths,
		},
	}
}

// protectionChecks are the checklist's lines for the protections.
func (a *App) protectionChecks(ctx context.Context) []check {
	p := a.protection()

	list := []check{
		{
			Key: "web_login", Label: "Website login brute-force bans", Level: "warning", Tab: "protection",
			OK: p.WebLogin.Enabled,
			Detail: map[bool]string{
				true:  fmt.Sprintf("An address posting to a login form %d times in %s is banned from the sites.", p.WebLogin.MaxRetry, humanSeconds(p.WebLogin.FindTime)),
				false: "A bot can guess WordPress and other login passwords as fast as PHP answers.",
			}[p.WebLogin.Enabled],
		},
		{
			Key: "rate_login", Label: "Login pages are rate limited", Level: "info", Tab: "protection",
			OK: p.RateLogin.Enabled,
			Detail: map[bool]string{
				true:  fmt.Sprintf("Each address gets %d login posts a minute; the rest are refused before PHP.", p.RateLogin.Rate),
				false: "Every login attempt reaches PHP and costs CPU, however many there are.",
			}[p.RateLogin.Enabled],
		},
		{
			Key: "web_scan", Label: "Vulnerability scanners are banned", Level: "info", Tab: "protection",
			OK: p.WebScan.Enabled,
			Detail: map[bool]string{
				true:  "Addresses looking for .env files, .git folders and known exploits are banned.",
				false: "Scanners can probe every site for leaked secrets and exploitable files.",
			}[p.WebScan.Enabled],
		},
		{
			Key: "recidive", Label: "Repeat offenders banned for longer", Level: "info", Tab: "protection",
			OK: p.Recidive.Enabled,
			Detail: map[bool]string{
				true:  "An address banned again and again is banned from every port for " + humanSeconds(p.Recidive.BanTime) + ".",
				false: "An address that is banned, waits and comes back is treated as new each time.",
			}[p.Recidive.Enabled],
		},
		{
			Key: "conn_limit", Label: "Slow and held-open connections are cut off", Level: "info", Tab: "protection",
			OK: p.ConnLimit.Enabled,
			Detail: map[bool]string{
				true:  fmt.Sprintf("Each address may have %d requests open at once; clients that send slowly are closed in seconds.", p.ConnLimit.Max),
				false: "A Slowloris attack can hold nginx's connections open with a trickle of bytes from one machine.",
			}[p.ConnLimit.Enabled],
		},
		{
			Key: "tools", Label: "Attack tools are refused", Level: "info", Tab: "protection",
			OK: p.Tools.Enabled,
			Detail: map[bool]string{
				true:  "sqlmap, nikto, WPScan and similar tools are cut off and banned on their first request.",
				false: "Vulnerability scanners that announce themselves can still probe every site.",
			}[p.Tools.Enabled],
		},
		{
			Key: "waf", Label: "Web application firewall", Level: "info", Tab: "protection",
			OK: p.WAF.Enabled && p.WAF.Block,
			Detail: map[bool]string{
				true:  "ModSecurity with the OWASP Core Rule Set refuses SQL injection, XSS and other attacks before PHP runs.",
				false: "SQL injection and cross-site scripting attempts reach the sites' code; only the code stands in the way.",
			}[p.WAF.Enabled && p.WAF.Block],
		},
		{
			Key: "port_scan", Label: "Port scanners are banned", Level: "info", Tab: "protection",
			OK: p.PortScan.Enabled,
			Detail: map[bool]string{
				true:  "An address trying closed port after closed port is banned from the whole server.",
				false: "Anyone can map which ports this server answers on, as often as they like.",
			}[p.PortScan.Enabled],
		},
		{
			Key: "kernel", Label: "Kernel network hardening", Level: "info", Tab: "protection",
			OK: p.Kernel,
			Detail: map[bool]string{
				true:  "SYN-flood protection on; ICMP redirects and source routing refused.",
				false: "The kernel accepts ICMP redirects and has the default SYN backlog.",
			}[p.Kernel],
		},
	}

	if a.Store.ServiceInstalled("mail") {
		list = append(list, check{
			Key: "mail", Label: "Mail login brute-force bans", Level: "warning", Tab: "protection",
			OK: p.Mail.Enabled,
			Detail: map[bool]string{
				true:  "Addresses guessing mailbox passwords over SMTP, IMAP or POP3 are banned.",
				false: "Mailbox passwords can be guessed forever; a stolen one sends spam from this server.",
			}[p.Mail.Enabled],
		})
	}

	// Only a question when a site is behind Cloudflare.
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.EdgeTLS() {
				list = append(list, check{
					Key: "real_ip", Label: "Real visitor addresses behind Cloudflare", Level: "warning", Tab: "protection",
					OK: p.RealIP,
					Detail: map[bool]string{
						true:  "Logs, bans and limits see the visitor's address rather than Cloudflare's.",
						false: "Sites behind Cloudflare log Cloudflare's addresses, so attackers cannot be told apart or banned.",
					}[p.RealIP],
				})
				list = append(list, check{
					Key: "edge_only", Label: "Only Cloudflare reaches the websites", Level: "info", Tab: "protection",
					OK: p.EdgeOnly,
					Detail: map[bool]string{
						true:  "Ports 80 and 443 refuse everyone but Cloudflare, so the sites cannot be reached around it.",
						false: "Anyone who finds this server's address can reach the sites without going through Cloudflare.",
					}[p.EdgeOnly],
				})
				break
			}
		}
	}

	return list
}

// webHostnames are every name the web ports answer to: each site's, and the
// panel's own.
func (a *App) webHostnames(ctx context.Context) []string {
	names := []string{}

	if domain := a.Store.Setting("panel_domain", ""); domain != "" {
		names = append(names, domain)
	}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			names = append(names, site.Hostnames()...)
		}
	}

	return names
}

// syncNginxBans has nginx refuse every banned address itself — the only thing
// that stops one whose requests arrive through Cloudflare. Every minute, so a
// ban fail2ban makes and one it lets expire both reach nginx within one; and
// with fresh at once, from what the machine says this moment, after the owner
// bans or lets an address go on the Security page.
//
// The website jails reach nginx sooner still, through their own action (see
// security/instant.go); what that leaves behind once it is banned no longer is
// pruned here.
func (a *App) syncNginxBans(ctx context.Context, fresh bool) {
	if !a.Store.ServiceInstalled("nginx") {
		return
	}

	ips, ok := a.webBannedIPs(ctx, fresh)
	if !ok {
		return
	}

	contents, instantErr := proc.ReadFile(security.InstantBansPath, true)
	if instantErr != nil {
		contents = ""
	}

	if err := security.ApplyNginxBans(ips, contents); err != nil {
		a.Log.Warn("could not give nginx the banned addresses", "error", err)
	}

	// Older than the oldest copy of the state the minute's sync can be
	// reading, so a ban that is newer than the copy is never taken away.
	if instantErr == nil {
		for _, address := range security.StaleInstantBans(contents, ips, time.Now(), securityStaleAfter+5*time.Minute) {
			if err := security.ReleaseInstant(address); err != nil {
				a.Log.Warn("could not take an address off nginx's instant bans", "ip", address, "error", err)
			}
		}
	}
}

// webBannedIPs are the banned addresses the sites should refuse: a firewall
// block, or a website, mail or repeat-offender ban. Not an SSH-only ban — the
// Ban form promises that one can still load the sites — nor a panel lockout,
// which is about signing in to the panel and expires. ok is false when the
// list could not be read, which is not the same as nobody being banned.
func (a *App) webBannedIPs(ctx context.Context, fresh bool) ([]string, bool) {
	ips := []string{}

	list, ok := a.bans(ctx, a.snapshot(fresh))["list"].([]bannedAddress)
	if !ok {
		return ips, false
	}

	for _, row := range list {
		for _, source := range row.Sources {
			if source.Kind != banSSH && source.Kind != banPanel {
				ips = append(ips, row.IP)
				break
			}
		}
	}

	return ips, true
}
