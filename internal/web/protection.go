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
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// The Protection tab: the optional protections in security/protect.go, each
// switched on and off on its own, and the malware scan. See there for what
// each one is; this is where they are stored, applied and kept applied.

const (
	protectionSetting     = "security_protection"
	kernelPreviousSetting = "security_kernel_previous"
	scanResultSetting     = "security_scan_result"
	scanUnreportedSetting = "security_scan_unreported"
	accessLogsSetting     = "security_access_logs"
)

// protection is what was saved, over the defaults, so a field added later has
// a value on machines that saved before it existed.
func (a *App) protection() security.Protection {
	p := security.ProtectionDefaults

	if stored := a.Store.Setting(protectionSetting, ""); stored != "" {
		json.Unmarshal([]byte(stored), &p)
	}

	return p
}

func (a *App) saveProtection(p security.Protection) {
	encoded, _ := json.Marshal(p)
	a.Store.SetSetting(protectionSetting, string(encoded))
}

// nginxEnv is what the nginx file is rendered with: the never-ban list, and
// the hostnames whose clients are busy by design — the panel itself, and the
// applications that sync or poll.
func (a *App) nginxEnv(ctx context.Context) security.NginxEnv {
	env := security.NginxEnv{IgnoreIPs: a.trustedList(ctx)}

	if domain := a.Store.Setting("panel_domain", ""); domain != "" {
		env.ExemptHosts = append(env.ExemptHosts, domain)
	}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.Type == "nextcloud" || site.Type == "n8n" {
				env.ExemptHosts = append(env.ExemptHosts, site.Hostnames()...)
			}
		}
	}

	return env
}

// applyProtection writes the jails and the nginx file for p. The kernel
// settings are applied by their own switch, because turning them off needs
// the values they replaced.
func (a *App) applyProtection(ctx context.Context, p security.Protection) error {
	if err := security.ApplyJails(p, a.trustedList(ctx)); err != nil {
		return err
	}

	if err := security.ApplyNginx(security.NginxFile(p, a.nginxEnv(ctx))); err != nil {
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

	if p.AnyJail() || p.RealIP || p.RateLogin.Enabled || p.RateFlood.Enabled ||
		security.NginxCurrent() != "" {
		want := security.NginxFile(p, a.nginxEnv(ctx))

		if security.NginxCurrent() != want {
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

	if p.Kernel && !security.KernelApplied() {
		if err := security.ApplyKernel(true, nil); err != nil {
			a.Log.Error("could not apply the kernel network settings", "error", err)
		}
	}
}

var lastProtectionCheck struct {
	sync.Mutex
	at time.Time
}

// checkProtection is the every-minute clock's: every ten minutes, a site added
// since fail2ban started is a log the web jails are not reading yet, and the
// jails are reloaded to pick it up. The daily scan is started from here too.
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

	p := a.protection()

	if p.WebLogin.Enabled || p.WebScan.Enabled {
		if logs := security.AccessLogs(); logs != a.Store.Setting(accessLogsSetting, "") {
			security.ReloadJails()
			a.Store.SetSetting(accessLogsSetting, logs)
		}
	}

	if p.Scan && a.scanDue() {
		go a.runScan(context.WithoutCancel(ctx))
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
	case "scan":
		p.Scan = enabled
		label = "The daily malware scan"
	default:
		a.securityBack(w, r, "That is not one of the protections.", true)
		return
	}

	if !ok {
		inertia.Back(w, r, "/security?tab=protection")
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
			a.securityBack(w, r, err.Error(), true)
			return
		}

		p.Kernel = enabled
		a.saveProtection(p)
		a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
		a.securityBack(w, r, label+map[bool]string{true: " is on.", false: " is off; the previous values are back."}[enabled], false)
		return
	}

	if feature != "scan" {
		if err := a.applyProtection(r.Context(), p); err != nil {
			// What is on disk is what was there before; the setting says so too.
			a.applyProtection(r.Context(), before)
			a.securityBack(w, r, err.Error(), true)
			return
		}
	}

	a.saveProtection(p)
	a.Log.Info("protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))

	message := label + " is off."
	if enabled {
		message = label + " is on."
	}

	if feature == "scan" && enabled && a.scanDue() {
		go a.runScan(context.WithoutCancel(r.Context()))
		message += " The first scan has started."
	}

	a.securityBack(w, r, message, false)
}

// ------------------------------------------------------------------- scan

// scanResult is the last scan.
type scanResult struct {
	StartedAt  int64              `json:"started_at"`
	FinishedAt int64              `json:"finished_at"`
	Sites      int                `json:"sites"`
	Findings   []security.Finding `json:"findings"`
	Errors     []string           `json:"errors"`
	WPCLI      bool               `json:"wp_cli"`
}

var scanning struct {
	sync.Mutex
	running bool
}

func (a *App) scanRunning() bool {
	scanning.Lock()
	defer scanning.Unlock()
	return scanning.running
}

func (a *App) lastScan() *scanResult {
	stored := a.Store.Setting(scanResultSetting, "")
	if stored == "" {
		return nil
	}

	var result scanResult
	if json.Unmarshal([]byte(stored), &result) != nil {
		return nil
	}

	return &result
}

// scanDue is whether the daily scan should run now: never run, or a day since
// the last, in the small hours where the machine is quietest — unless it has
// not run for two days, in which case now is as good as any time.
func (a *App) scanDue() bool {
	if a.scanRunning() {
		return false
	}

	last := a.lastScan()
	if last == nil || last.FinishedAt == 0 {
		return true
	}

	since := time.Since(time.Unix(last.FinishedAt, 0))
	hour := time.Now().Hour()

	return since > 48*time.Hour || (since > 20*time.Hour && hour >= 2 && hour < 6)
}

// runScan scans every site, one after another at the lowest priority, and
// keeps the result. What it found that the previous scan had not is counted
// for the alert clock to send.
func (a *App) runScan(ctx context.Context) {
	scanning.Lock()
	if scanning.running {
		scanning.Unlock()
		return
	}
	scanning.running = true
	scanning.Unlock()

	defer func() {
		scanning.Lock()
		scanning.running = false
		scanning.Unlock()

		if problem := recover(); problem != nil {
			a.Log.Error("the malware scan panicked", "panic", problem)
		}
	}()

	previous := a.lastScan()

	result := scanResult{StartedAt: time.Now().Unix(), Findings: []security.Finding{}, Errors: []string{},
		WPCLI: proc.Which("wp")}

	// Kept while it runs, so the page shows it started.
	running := result
	encoded, _ := json.Marshal(running)
	if previous != nil {
		running.Findings = previous.Findings
		encoded, _ = json.Marshal(running)
	}
	a.Store.SetSetting(scanResultSetting, string(encoded))

	sites, err := a.Store.Sites(ctx)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
	}

	for _, site := range sites {
		if !site.IsPHP() {
			continue
		}

		result.Sites++

		findings, err := security.ScanSite(security.ScanTarget{
			Domain: site.Domain, Root: site.DocumentRoot(), WordPress: site.Type == "wordpress",
		}, result.WPCLI)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}

		result.Findings = append(result.Findings, findings...)
	}

	if len(result.Findings) > 1000 {
		result.Findings = result.Findings[:1000]
	}

	result.FinishedAt = time.Now().Unix()

	known := map[string]bool{}
	if previous != nil {
		for _, finding := range previous.Findings {
			known[finding.Site+" "+finding.Kind+" "+finding.Path] = true
		}
	}

	fresh := 0
	for _, finding := range result.Findings {
		if !known[finding.Site+" "+finding.Kind+" "+finding.Path] {
			fresh++
		}
	}

	encoded, _ = json.Marshal(result)
	a.Store.SetSetting(scanResultSetting, string(encoded))

	if fresh > 0 {
		unreported, _ := strconv.Atoi(a.Store.Setting(scanUnreportedSetting, "0"))
		a.Store.SetSetting(scanUnreportedSetting, strconv.Itoa(unreported+fresh))
	}

	a.Log.Info("malware scan finished", "sites", result.Sites, "findings", len(result.Findings), "new", fresh)
}

// securityScanNow starts a scan whether or not the daily one is on.
func (a *App) securityScanNow(w http.ResponseWriter, r *http.Request) {
	if a.scanRunning() {
		a.securityBack(w, r, "A scan is already running.", false)
		return
	}

	go a.runScan(context.WithoutCancel(r.Context()))

	a.securityBack(w, r, "The scan has started. It runs at low priority, so a large site takes a while; "+
		"reload this tab to see the result.", false)
}

// ------------------------------------------------------------------ props

// protectJailKinds are what the Banned IPs tab calls each optional jail's
// bans.
var protectJailKinds = map[string]string{
	security.JailWebLogin: "web_login",
	security.JailWebScan:  "web_scan",
	security.JailMail:     "mail",
	security.JailMailIMAP: "mail",
	security.JailRecidive: "recidive",
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

	return map[string]any{
		"settings": p,
		"defaults": security.ProtectionDefaults,
		"jails":    jails,
		"ready": map[string]bool{
			"fail2ban": proc.Which("fail2ban-client"),
			"nginx":    a.Store.ServiceInstalled("nginx"),
			"mail":     a.Store.ServiceInstalled("mail"),
			"wp_cli":   proc.Which("wp"),
		},
		"kernel_applied": p.Kernel && security.KernelApplied(),
		"edge_sites":     edgeSites,
		"scan":           a.lastScan(),
		"scan_running":   a.scanRunning(),
		"finding_labels": security.FindingLabels,
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
			Key: "kernel", Label: "Kernel network hardening", Level: "info", Tab: "protection",
			OK: p.Kernel,
			Detail: map[bool]string{
				true:  "SYN-flood protection on; ICMP redirects and source routing refused.",
				false: "The kernel accepts ICMP redirects and has the default SYN backlog.",
			}[p.Kernel],
		},
		{
			Key: "scan", Label: "Daily malware scan", Level: "info", Tab: "protection",
			OK: p.Scan,
			Detail: map[bool]string{
				true:  "Every PHP site is checked each night for changed WordPress files, PHP in uploads and web shells.",
				false: "Nothing checks the sites' files for a backdoor left by an attack.",
			}[p.Scan],
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
				break
			}
		}
	}

	if p.Scan {
		if last := a.lastScan(); last != nil && last.FinishedAt > 0 && len(last.Findings) > 0 {
			list = append(list, check{
				Key: "scan_findings", Label: "Malware scan findings to review", Level: "warning", Tab: "protection",
				Detail: fmt.Sprintf("The last scan found %d file(s) worth a look.", len(last.Findings)),
			})
		}
	}

	return list
}
