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
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// Hardening that costs nothing while the server runs: locked site files, port
// forwards, and Cloudflare's own Under Attack mode for a site behind it. The
// kernel, ping, PHP and compiler switches are in protection.go.

const (
	siteLocksSetting     = "security_site_locks"
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
// Every job that writes a site's files goes through here.
func (a *App) withSiteUnlocked(ctx context.Context, siteID int64, relock bool, work func() error) error {
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

// removeIntegritySnapshots deletes what the code change monitor, retired in
// 2.21.0, kept of every site.
func removeIntegritySnapshots(dataDir string) {
	os.RemoveAll(filepath.Join(dataDir, "integrity"))
}

func must[T any](value T, _ error) T { return value }
