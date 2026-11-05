package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The protections against a server that has already been got into, or a site
// that is about to be: locked site files, the code change monitor, the
// backdoor watch, PHP hardening and compilers kept from everyone else — and
// the one-press recommended setup, port forwards and Cloudflare's own Under
// Attack mode.

const (
	siteLocksSetting     = "security_site_locks"
	siteWorkSetting      = "security_site_work"
	watchBaselineSetting = "security_watch_baseline"
	watchChangesSetting  = "security_watch_changes"
	codeChangesSetting   = "security_code_changes"
	forwardsSetting      = "security_forwards"
	cloudflareUAMSetting = "security_cloudflare_attack"
)

// unlockMinutes is how long Unlock for updates leaves a site open.
const unlockMinutes = 30

func (a *App) dataDir() string {
	if a.Config != nil && a.Config.DatabasePath != "" {
		return filepath.Dir(a.Config.DatabasePath)
	}
	return os.TempDir()
}

func (a *App) settingJSON(key string, into any) {
	json.Unmarshal([]byte(a.Store.Setting(key, "")), into)
}

func (a *App) saveJSON(key string, value any) {
	encoded, _ := json.Marshal(value)
	a.Store.SetSetting(key, string(encoded))
}

// ------------------------------------------------------------------ site locks

// siteLock is one site's lock: wanted on, and — while unlocked for updates —
// when it locks itself again.
type siteLock struct {
	Locked   bool  `json:"locked"`
	RelockAt int64 `json:"relock_at"`
}

var locksMutex sync.Mutex

func (a *App) siteLocks() map[string]siteLock {
	locks := map[string]siteLock{}
	a.settingJSON(siteLocksSetting, &locks)
	return locks
}

func (a *App) lockRecord(site *store.Site) string {
	return security.LockRecord(a.dataDir(), site.Domain)
}

// lockable is why a site cannot be locked, or "".
func lockable(site *store.Site) string {
	if !site.IsPHP() {
		return "Only PHP sites can be locked: an app that builds itself writes all over its own directory."
	}
	if site.RootPath == "" {
		return "This site has no directory."
	}
	return ""
}

// withSiteUnlocked runs the panel's own work on a site with its files open,
// and locks them again after unless relock is false (the site is going).
// Every job that writes a site's files goes through here — and through
// markSiteWork, so the code change monitor knows the change was the panel's.
func (a *App) withSiteUnlocked(ctx context.Context, siteID int64, relock bool, work func() error) error {
	a.markSiteWork(siteID)
	defer a.markSiteWork(siteID)

	site, err := a.Store.FindSite(ctx, siteID)
	if err != nil {
		return work()
	}

	record := a.lockRecord(site)
	if !security.SiteLocked(record) {
		return work()
	}

	locksMutex.Lock()
	unlockErr := security.UnlockSite(site.RootPath, record)
	locksMutex.Unlock()
	if unlockErr != nil {
		return fmt.Errorf("the site's files are locked and could not be unlocked for this: %w", unlockErr)
	}

	result := work()

	if relock {
		if lock := a.siteLocks()[site.Domain]; lock.Locked && lock.RelockAt == 0 {
			locksMutex.Lock()
			if err := security.LockSite(site.RootPath, record); err != nil {
				a.Log.Error("could not lock a site's files again after the panel's work", "site", site.Domain, "error", err)
			}
			locksMutex.Unlock()
		}
	}

	return result
}

// markSiteWork notes that the panel is changing a site's files now.
func (a *App) markSiteWork(siteID int64) {
	work := map[string]int64{}
	a.settingJSON(siteWorkSetting, &work)
	work[strconv.FormatInt(siteID, 10)] = time.Now().Unix()
	a.saveJSON(siteWorkSetting, work)
}

func (a *App) siteWorkSince(siteID int64, since time.Time) bool {
	work := map[string]int64{}
	a.settingJSON(siteWorkSetting, &work)
	return work[strconv.FormatInt(siteID, 10)] >= since.Unix()
}

// securityLock locks a site, unlocks it, or unlocks it for thirty minutes.
func (a *App) securityLock(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	var site *store.Site
	if sites, err := a.Store.Sites(r.Context()); err == nil {
		for _, candidate := range sites {
			if candidate.Domain == input["site"] {
				site = candidate
			}
		}
	}
	if site == nil {
		a.securityBack(w, r, "That site no longer exists.", true)
		return
	}

	locksMutex.Lock()
	defer locksMutex.Unlock()

	locks := a.siteLocks()
	record := a.lockRecord(site)
	message := ""

	switch input["action"] {
	case "lock":
		if why := lockable(site); why != "" {
			a.securityBack(w, r, why, true)
			return
		}
		if !security.SiteLocked(record) {
			if err := security.LockSite(site.RootPath, record); err != nil {
				a.securityBack(w, r, err.Error(), true)
				return
			}
		}
		locks[site.Domain] = siteLock{Locked: true}
		message = site.Domain + "’s files are locked. WordPress cannot update itself until you unlock it."

	case "unlock", "unlock_temp":
		if security.SiteLocked(record) {
			if err := security.UnlockSite(site.RootPath, record); err != nil {
				a.securityBack(w, r, err.Error(), true)
				return
			}
		}
		if input["action"] == "unlock_temp" {
			locks[site.Domain] = siteLock{Locked: true, RelockAt: time.Now().Add(unlockMinutes * time.Minute).Unix()}
			message = fmt.Sprintf("%s is unlocked for %d minutes — run your updates now. It locks itself again after.",
				site.Domain, unlockMinutes)
		} else {
			delete(locks, site.Domain)
			message = site.Domain + "’s files are unlocked."
		}

	default:
		a.securityBack(w, r, "That is not something a lock does.", true)
		return
	}

	a.saveJSON(siteLocksSetting, locks)
	a.Log.Info("site lock changed", "site", site.Domain, "action", input["action"], "ip", session.ClientIP(r))
	a.securityBack(w, r, message, false)
}

// checkLocks locks again the sites whose thirty minutes are up. Every minute.
func (a *App) checkLocks(ctx context.Context) {
	locks := a.siteLocks()
	if len(locks) == 0 {
		return
	}

	sites, err := a.Store.Sites(ctx)
	if err != nil {
		return
	}

	changed := false
	for _, site := range sites {
		lock, ok := locks[site.Domain]
		if !ok || !lock.Locked || lock.RelockAt == 0 || time.Now().Unix() < lock.RelockAt {
			continue
		}

		locksMutex.Lock()
		record := a.lockRecord(site)
		var err error
		if !security.SiteLocked(record) {
			err = security.LockSite(site.RootPath, record)
		}
		locksMutex.Unlock()

		if err != nil {
			a.Log.Error("could not lock a site's files again", "site", site.Domain, "error", err)
			continue
		}

		lock.RelockAt = 0
		locks[site.Domain] = lock
		changed = true
	}

	// A site that was deleted takes its lock with it.
	known := map[string]bool{}
	for _, site := range sites {
		known[site.Domain] = true
	}
	for domain := range locks {
		if !known[domain] {
			delete(locks, domain)
			changed = true
		}
	}

	if changed {
		a.saveJSON(siteLocksSetting, locks)
	}
}

// lockProps are the sites as the lock card shows them.
func (a *App) lockProps(ctx context.Context) []map[string]any {
	locks := a.siteLocks()
	list := []map[string]any{}

	sites, _ := a.Store.Sites(ctx)
	for _, site := range sites {
		lock := locks[site.Domain]
		list = append(list, map[string]any{
			"domain":    site.Domain,
			"locked":    security.SiteLocked(a.lockRecord(site)),
			"wanted":    lock.Locked,
			"relock_at": lock.RelockAt,
			"lockable":  lockable(site) == "",
			"why_not":   lockable(site),
		})
	}

	return list
}

// ------------------------------------------------------------------ code changes

var integrity struct {
	sync.Mutex
	running bool
	at      time.Time
}

func (a *App) snapshotPath(site *store.Site) string {
	return filepath.Join(a.dataDir(), "integrity", strings.ReplaceAll(site.Domain, "/", "_")+".tsv")
}

// checkIntegrity looks at every PHP site's code once an hour, in the
// background, and reports what changed that the panel did not change.
func (a *App) checkIntegrity(ctx context.Context) []security.CodeChange {
	integrity.Lock()
	if integrity.running || time.Since(integrity.at) < time.Hour {
		integrity.Unlock()
		return nil
	}
	integrity.running, integrity.at = true, time.Now()
	since := integrity.at.Add(-time.Hour - 5*time.Minute)
	integrity.Unlock()

	defer func() {
		integrity.Lock()
		integrity.running = false
		integrity.Unlock()
	}()

	sites, err := a.Store.Sites(ctx)
	if err != nil {
		return nil
	}

	found := []security.CodeChange{}
	os.MkdirAll(filepath.Join(a.dataDir(), "integrity"), 0o700)

	for _, site := range sites {
		if !site.IsPHP() || site.RootPath == "" {
			continue
		}

		current, err := security.CodeSnapshot(site.RootPath)
		if err != nil {
			continue
		}

		path := a.snapshotPath(site)
		previous, readErr := os.ReadFile(path)
		os.WriteFile(path, []byte(security.EncodeSnapshot(current)), 0o600)

		// The first look is the baseline; a look after the panel's own work
		// is the new one.
		if readErr != nil || a.siteWorkSince(site.ID, since) {
			continue
		}

		if change := security.DiffSnapshots(site.Domain, security.DecodeSnapshot(string(previous)), current, time.Now()); !change.Empty() {
			found = append(found, change)
		}
	}

	if len(found) > 0 {
		changes := []security.CodeChange{}
		a.settingJSON(codeChangesSetting, &changes)
		changes = append(found, changes...)
		if len(changes) > 50 {
			changes = changes[:50]
		}
		a.saveJSON(codeChangesSetting, changes)
	}

	return found
}

// codeAlert is the email about changed code.
func codeAlert(changes []security.CodeChange) string {
	var text strings.Builder

	files := 0
	for _, change := range changes {
		files += len(change.Added) + len(change.Changed) + len(change.Removed)
	}
	fmt.Fprintf(&text, "Code changed on %d site%s (%d file%s)\n\n", len(changes), sOf(len(changes)),
		files, sOf(files))

	list := func(label string, paths []string) {
		for i, path := range paths {
			if i == 10 {
				fmt.Fprintf(&text, "    … and %d more\n", len(paths)-10)
				break
			}
			fmt.Fprintf(&text, "    %s %s\n", label, path)
		}
	}

	for _, change := range changes {
		fmt.Fprintf(&text, "%s\n", change.Site)
		if len(change.InUploads) > 0 {
			text.WriteString("  ! PHP in an uploads folder — no real site keeps code there; this is very likely a web shell:\n")
			list("!", change.InUploads)
		}
		list("+", change.Added)
		list("~", change.Changed)
		list("-", change.Removed)
		text.WriteString("\n")
	}

	text.WriteString("Nobody changed these through the panel. If you or WordPress did (an update, a plugin), there is nothing to do. " +
		"If not, the site has probably been hacked: look at the files in the panel's file manager, restore a backup from before " +
		"the change, and lock the site's files on Security → Protection.")

	return text.String()
}

// ------------------------------------------------------------------ backdoor watch

var watchClock struct {
	sync.Mutex
	at time.Time
}

func (a *App) watchEnv(ctx context.Context) security.WatchEnv {
	env := security.WatchEnv{}
	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			env.SiteHomes = append(env.SiteHomes, site.RootPath)
			if site.IsProxied() {
				env.PanelUnits = append(env.PanelUnits, site.ServiceName())
			}
		}
	}
	return env
}

// rebaselineWatch takes what the machine is now as normal: after the owner
// accepts the changes, and after the panel's own work that changes accounts,
// keys or services.
func (a *App) rebaselineWatch(ctx context.Context) {
	if !a.protection().Watch {
		return
	}
	a.saveJSON(watchBaselineSetting, security.TakeWatch(a.watchEnv(ctx)))
}

// checkWatch compares every five minutes; the changes are kept for the
// Protection tab and returned for the alert.
func (a *App) checkWatch(ctx context.Context) []security.WatchChange {
	if !a.protection().Watch {
		return nil
	}

	watchClock.Lock()
	if time.Since(watchClock.at) < 5*time.Minute {
		watchClock.Unlock()
		return nil
	}
	watchClock.at = time.Now()
	watchClock.Unlock()

	var baseline []security.WatchItem
	a.settingJSON(watchBaselineSetting, &baseline)

	current := security.TakeWatch(a.watchEnv(ctx))
	if len(current) == 0 {
		return nil // the picture could not be taken; not the same as everything gone
	}
	if baseline == nil {
		a.saveJSON(watchBaselineSetting, current)
		return nil
	}

	changes := security.DiffWatch(baseline, current, time.Now())
	if len(changes) == 0 {
		return nil
	}

	a.saveJSON(watchBaselineSetting, current)

	kept := []security.WatchChange{}
	a.settingJSON(watchChangesSetting, &kept)
	kept = append(changes, kept...)
	if len(kept) > 100 {
		kept = kept[:100]
	}
	a.saveJSON(watchChangesSetting, kept)

	return changes
}

func watchAlert(changes []security.WatchChange) string {
	var text strings.Builder

	fmt.Fprintf(&text, "%d change%s to accounts, keys or scheduled jobs\n\n", len(changes), sOf(len(changes)))
	for _, change := range changes {
		fmt.Fprintf(&text, "  • %s\n", change.Summary)
	}
	text.WriteString("\nThese are the ways an intruder keeps a way back into a server. None of them was made through the panel. " +
		"If you made them, mark them as expected on Security → Protection. If not, remove them now — and change the " +
		"passwords and keys of every account that can log in.")

	return text.String()
}

// securityClearChanges empties the changes list of the watch or the code
// monitor: the owner has seen them.
func (a *App) securityClearChanges(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	switch input["kind"] {
	case "watch":
		a.Store.ForgetSetting(watchChangesSetting)
		a.rebaselineWatch(r.Context())
		a.securityBack(w, r, "Marked as expected. Only changes from now on will be reported.", false)
	case "code":
		a.Store.ForgetSetting(codeChangesSetting)
		a.securityBack(w, r, "The list of code changes is cleared.", false)
	default:
		a.securityBack(w, r, "Nothing to clear.", true)
	}
}

// ------------------------------------------------------------------ the simple switches

// applySwitch turns one of the machine-level hardening switches on or off.
func (a *App) applySwitch(ctx context.Context, feature string, on bool) error {
	switch feature {
	case "watch":
		if on {
			a.saveJSON(watchBaselineSetting, security.TakeWatch(a.watchEnv(ctx)))
		} else {
			a.Store.ForgetSetting(watchBaselineSetting)
		}
	case "integrity":
		if !on {
			os.RemoveAll(filepath.Join(a.dataDir(), "integrity"))
		} else {
			// The first look starts at once, in the background: it is the baseline.
			integrity.Lock()
			integrity.at = time.Time{}
			integrity.Unlock()
			go a.checkIntegrity(context.WithoutCancel(ctx))
		}
	case "php_harden":
		return security.ApplyPHPHardening(on)
	case "compilers":
		return security.RestrictCompilers(on, a.Config.SystemUser)
	}
	return nil
}

// ------------------------------------------------------------------ recommended

// applyRecommended turns on everything that is safe for any server: what
// never changes how a real visitor's site behaves. Left for the owner to
// choose: under attack mode, country rules, the WAF, Cloudflare only, ping,
// PHP hardening, compilers and file locks — each changes what a site or a
// person may do.
func (a *App) applyRecommended(ctx context.Context) (security.Protection, []string, error) {
	p := a.protection()
	skipped := []string{}

	fail2ban := proc.Which("fail2ban-client")
	nginx := a.Store.ServiceInstalled("nginx")
	haveSites := security.AccessLogs() != ""

	if nginx {
		p.RateLogin.Enabled, p.RateFlood.Enabled, p.ConnLimit.Enabled = true, true, true
		for _, site := range must(a.Store.Sites(ctx)) {
			if site.EdgeTLS() {
				p.RealIP = true
			}
		}
	} else {
		skipped = append(skipped, "the website protections (nginx is not installed)")
	}

	if fail2ban {
		p.Recidive.Enabled = true
		if nginx && haveSites {
			p.WebLogin.Enabled, p.WebScan.Enabled, p.Tools.Enabled, p.WebFlood.Enabled = true, true, true, true
		} else if nginx {
			skipped = append(skipped, "the website bans (there are no sites yet — press again after adding one)")
		}
		if a.Store.ServiceInstalled("mail") {
			p.Mail.Enabled = true
		}
		if security.ReadFirewall().Active && security.EnableUFWLogging() == nil {
			p.PortScan.Enabled = true
		} else {
			skipped = append(skipped, "port scan bans (the firewall is off)")
		}
	} else {
		skipped = append(skipped, "every ban (fail2ban is not installed — the SSH protection tab installs it)")
	}

	if err := a.applyProtection(ctx, p); err != nil {
		return a.protection(), skipped, err
	}

	if !p.Kernel {
		encoded, _ := json.Marshal(security.KernelValues())
		a.Store.SetSetting(kernelPreviousSetting, string(encoded))
		if err := security.ApplyKernel(true, nil); err == nil {
			p.Kernel = true
		}
	}

	if !p.Watch {
		p.Watch = true
		a.saveJSON(watchBaselineSetting, security.TakeWatch(a.watchEnv(ctx)))
	}
	if !p.Integrity {
		p.Integrity = true
		a.applySwitch(ctx, "integrity", true)
	}

	a.saveProtection(p)

	return p, skipped, nil
}

func must[T any](value T, _ error) T { return value }

// ------------------------------------------------------------------ port forwards

func (a *App) forwards() []security.Forward {
	list := []security.Forward{}
	a.settingJSON(forwardsSetting, &list)
	return list
}

// securityForward adds a forward, or removes one.
func (a *App) securityForward(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	list := a.forwards()
	message := ""

	if key := input["remove"]; key != "" {
		protocol, port, ok := security.ParseForwardKey(key)
		kept := []security.Forward{}
		for _, forward := range list {
			if !(ok && forward.Protocol == protocol && forward.Port == port) {
				kept = append(kept, forward)
			}
		}
		list = kept
		message = "The forward is removed."
	} else {
		port, _ := strconv.Atoi(strings.TrimSpace(input["port"]))
		toPort, _ := strconv.Atoi(strings.TrimSpace(input["to_port"]))
		forward := security.Forward{
			Protocol: strings.ToLower(strings.TrimSpace(input["protocol"])),
			Port:     port, ToIP: strings.TrimSpace(input["to_ip"]), ToPort: toPort,
			Comment: strings.TrimSpace(input["comment"]),
		}
		if forward.Protocol == "" {
			forward.Protocol = "tcp"
		}

		problems := forward.Validate()
		for _, existing := range list {
			if existing.Protocol == forward.Protocol && existing.Port == forward.Port {
				problems["port"] = "That port is already forwarded."
			}
		}
		if forward.Port == a.Config.PanelPort || forward.Port == 22 || forward.Port == 80 || forward.Port == 443 {
			problems["port"] = "That port is in use by SSH, the sites or the panel."
		}
		if len(problems) > 0 {
			for field, problem := range problems {
				sess.Error(field, problem)
			}
			inertia.Back(w, r, "/security?tab=firewall")
			return
		}

		list = append(list, forward)
		target := "port " + strconv.Itoa(forward.ToPort) + " on this server"
		if !forward.Local() {
			target = fmt.Sprintf("%s:%d", forward.ToIP, forward.ToPort)
		}
		message = fmt.Sprintf("Port %d now leads to %s.", forward.Port, target)
	}

	sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })

	if err := security.ApplyForwards(list); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	// The forwarded port has to be open for anyone to reach it.
	for _, forward := range list {
		security.AddRule(security.NewRule{Action: "allow", Port: strconv.Itoa(forward.Port), Protocol: forward.Protocol,
			Comment: "Port forward"})
	}

	a.saveJSON(forwardsSetting, list)
	securityCache.forget()
	a.Log.Info("port forwards changed", "count", len(list), "ip", session.ClientIP(r))
	a.securityBack(w, r, message, false)
}

// ------------------------------------------------------------------ Cloudflare

// securityCloudflareAttack switches Cloudflare's own Under Attack mode for a
// site behind it, through the site's DNS account. The level it replaces is
// kept, and put back when it is switched off.
func (a *App) securityCloudflareAttack(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	on := boolInput(input, "enabled", false)

	var site *store.Site
	for _, candidate := range must(a.Store.Sites(r.Context())) {
		if candidate.Domain == input["site"] {
			site = candidate
		}
	}
	if site == nil || site.DNSAccountID == 0 {
		a.securityBack(w, r, "That site is not on a DNS account the panel manages, so Cloudflare cannot be asked. "+
			"Turn Under Attack mode on in Cloudflare's dashboard instead.", true)
		return
	}

	account, err := a.Store.FindDNSAccount(r.Context(), site.DNSAccountID, site.UserID)
	if err != nil || account.Provider != "cloudflare" {
		a.securityBack(w, r, "That site's DNS is not at Cloudflare.", true)
		return
	}

	client := dns.New(a.Store, account)
	zoneID, _, err := client.FindZone(site.Domain)
	if err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	previous := map[string]string{}
	a.settingJSON(cloudflareUAMSetting, &previous)

	level := "under_attack"
	if on {
		if current, err := client.SecurityLevel(zoneID); err == nil && current != "under_attack" {
			previous[site.Domain] = current
		}
	} else {
		level = previous[site.Domain]
		if level == "" {
			level = "medium"
		}
	}

	if err := client.SetSecurityLevel(zoneID, level); err != nil {
		a.securityBack(w, r, "Cloudflare refused: "+err.Error()+" — the API token needs the “Zone Settings: Edit” permission "+
			"(edit the token in Cloudflare's dashboard, under My Profile → API Tokens).", true)
		return
	}

	if on {
		previous["_on_"+site.Domain] = "1"
	} else {
		delete(previous, site.Domain)
		delete(previous, "_on_"+site.Domain)
	}
	a.saveJSON(cloudflareUAMSetting, previous)

	a.Log.Info("cloudflare under attack mode changed", "site", site.Domain, "on", on, "ip", session.ClientIP(r))
	a.securityBack(w, r, map[bool]string{
		true:  "Cloudflare's Under Attack mode is on for " + site.Domain + ". Every visitor sees Cloudflare's check first.",
		false: "Cloudflare's Under Attack mode is off for " + site.Domain + ".",
	}[on], false)
}

func (a *App) cloudflareAttackOn() map[string]bool {
	stored := map[string]string{}
	a.settingJSON(cloudflareUAMSetting, &stored)
	on := map[string]bool{}
	for key := range stored {
		if domain, found := strings.CutPrefix(key, "_on_"); found {
			on[domain] = true
		}
	}
	return on
}

// sOf is the plural ending for count.
func sOf(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func (a *App) watchChanges() []security.WatchChange {
	list := []security.WatchChange{}
	a.settingJSON(watchChangesSetting, &list)
	return list
}

func (a *App) codeChanges() []security.CodeChange {
	list := []security.CodeChange{}
	a.settingJSON(codeChangesSetting, &list)
	return list
}

// securityRecommended is the one press that turns on the safe set.
func (a *App) securityRecommended(w http.ResponseWriter, r *http.Request) {
	_, skipped, err := a.applyRecommended(r.Context())
	if err != nil {
		a.securityBack(w, r, "Some protections could not be turned on: "+err.Error(), true)
		return
	}

	a.Log.Info("recommended protections turned on", "ip", session.ClientIP(r))

	message := "The recommended protections are on."
	if len(skipped) > 0 {
		message += " Left off for now: " + strings.Join(skipped, "; ") + "."
	}
	a.securityBack(w, r, message, false)
}

// rebaselining runs a handler that changes accounts or keys on purpose, then
// takes the result as the backdoor watch's normal.
func (a *App) rebaselining(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
		a.rebaselineWatch(r.Context())
	}
}

// markingWork runs a handler that edits a site's files through the panel, so
// the code change monitor does not report the owner's own edit.
func (a *App) markingWork(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id, err := strconv.ParseInt(r.PathValue("site"), 10, 64); err == nil {
			a.markSiteWork(id)
		}
		handler(w, r)
	}
}
