package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Settings the Security page keeps.
const (
	jailSetting            = "security_ssh_jail"
	loginMaxSetting        = "security_login_max_attempts"
	loginLockoutSetting    = "security_login_lockout_minutes"
	fail2banOfferedSetting = "security_fail2ban_offered"
)

// securityTaskActions are the tasks this page starts.
var securityTaskActions = []string{"security.auto-updates", "security.updates"}

// ------------------------------------------------------------ sign-in limits

// loginLimits are how many failed sign-ins an address gets, and for how long
// it is refused once it has had them.
func (a *App) loginLimits() (int, time.Duration) {
	limit, err := strconv.Atoi(a.Store.Setting(loginMaxSetting, "5"))
	if err != nil || limit < 1 {
		limit = 5
	}

	minutes, err := strconv.Atoi(a.Store.Setting(loginLockoutSetting, "15"))
	if err != nil || minutes < 1 {
		minutes = 15
	}

	return limit, time.Duration(minutes) * time.Minute
}

// loginLockedUntil is when an address may try again, or zero if it may now.
func (a *App) loginLockedUntil(ctx context.Context, ip string) int64 {
	limit, window := a.loginLimits()

	count, oldest := a.Store.FailedLoginsSince(ctx, ip, time.Now().Add(-window))
	if count < limit {
		return 0
	}

	return oldest + int64(window.Seconds())
}

// loginAttempt is the row a sign-in attempt from this request is recorded as.
func (a *App) loginAttempt(r *http.Request, email string) store.LoginAttempt {
	return store.LoginAttempt{
		Email:     strings.ToLower(strings.TrimSpace(email)),
		IP:        session.ClientIP(r),
		UserAgent: r.UserAgent(),
	}
}

// ------------------------------------------------------------------- SSH jail

// jailConfig is the SSH jail as the panel last saved it, or the defaults.
func (a *App) jailConfig() security.JailConfig {
	config := security.Defaults

	if stored := a.Store.Setting(jailSetting, ""); stored != "" {
		json.Unmarshal([]byte(stored), &config)
	}

	if config.IgnoreIPs == nil {
		config.IgnoreIPs = []string{}
	}

	return config
}

// applySSHProtection writes the jail fail2ban runs. It is the catalogue's last
// step for fail2ban as well as what the Security page calls on save.
func (a *App) applySSHProtection(runner *tasks.Runner) error {
	if err := security.Apply(a.jailConfig(), security.SSHPorts()); err != nil {
		return err
	}

	// The trap shares the never-ban list, so it is rewritten with it.
	if a.sshTrapEnabled() {
		if err := security.ApplyTrap(true, a.jailConfig().IgnoreIPs); err != nil {
			return err
		}
	}

	if runner != nil {
		config := a.jailConfig()
		runner.Writef("SSH is protected: %d failed attempts within %s and an address is %s.\n",
			config.MaxRetry, humanSeconds(config.FindTime), banDescription(config.BanTime))
	}

	securityCache.forget()

	return nil
}

// EnsureSSHProtection makes the default true on every machine, not only the
// ones installed after it existed: fail2ban is installed if it is missing, and
// the jail is rewritten if what is on disk is not what the panel would write.
//
// Installed once. An operator who removes fail2ban from Apps has decided
// something, and a panel restart is not a reason to overrule it.
func (a *App) EnsureSSHProtection(ctx context.Context) {
	if a.Config.InstallApps == "none" {
		return
	}

	if !proc.Which("fail2ban-client") {
		if a.Store.SettingBool(fail2banOfferedSetting, false) {
			return
		}

		a.Store.SetSetting(fail2banOfferedSetting, "1")

		// A first run's install already has it on the list.
		if a.Store.ServicesPending() {
			return
		}

		if a.Catalog.Queue([]string{"fail2ban"}, false) == 0 {
			return
		}

		if _, err := a.Queue.PushID(queue.ServicesInstall, "user", 0); err != nil {
			a.Log.Error("could not queue the fail2ban install", "error", err)
			return
		}

		a.Log.Info("installing fail2ban to protect SSH from password guessing")

		return
	}

	a.Store.SetSetting(fail2banOfferedSetting, "1")

	current, _ := proc.ReadFile(security.JailPath, true)
	journal := !proc.Ok("sudo test -s /var/log/auth.log")

	if current == security.JailFile(a.jailConfig(), security.SSHPorts(), journal) {
		return
	}

	if err := a.applySSHProtection(nil); err != nil {
		a.Log.Error("could not protect SSH", "error", err)
		return
	}

	a.Log.Info("SSH protection applied", "jail", security.JailPath)
}

// ---------------------------------------------------------------- snapshot

// securitySnapshot is everything the Security page's checks need. Reading
// it is half a dozen sudo commands and a journal query — seconds of CPU, on a
// machine that is being guessed at all day — so only the Security page reads
// it. That page gets the copy there is, and a copy older than a minute is
// replaced in the background for the page after. The dashboard used to carry
// a tile built from it, and opening the dashboard after a break ran apt, ufw
// and fail2ban-client all at once — which the CPU graph then showed.
type securitySnapshot struct {
	Jail     security.JailStatus
	SSH      security.SSHState
	Firewall security.Firewall
	Updates  security.Updates
	Events   []security.Event
	Users    []security.SSHUser
	Trap     *security.JailStatus
	Taken    time.Time
}

type snapshotCache struct {
	sync.Mutex
	value *securitySnapshot

	// generation moves on every forget, so a background read that started
	// before something changed cannot put its out-of-date answer back.
	generation int
	refreshing bool

	// reading serialises the reads themselves: two pages arriving at a cold
	// cache share one read rather than running two.
	reading sync.Mutex
}

var securityCache snapshotCache

// securityStaleAfter is how old a copy can be before the Security page waits
// for a new one rather than showing it. Nothing else reads it, so a copy can
// sit for hours between visits, and checks that old are not worth showing as
// current.
const securityStaleAfter = 15 * time.Minute

// forget drops the cached snapshot, after anything that changes what it says.
// The next page reads afresh and waits for it, because it is the page that
// should show the change.
func (c *snapshotCache) forget() {
	c.Lock()
	c.value = nil
	c.generation++
	c.Unlock()
}

func (a *App) snapshot(fresh bool) *securitySnapshot {
	securityCache.Lock()
	current, generation := securityCache.value, securityCache.generation

	if !fresh && current != nil && time.Since(current.Taken) < securityStaleAfter {
		if time.Since(current.Taken) >= time.Minute && !securityCache.refreshing {
			securityCache.refreshing = true

			go func() {
				snapshot := a.readSnapshot()

				securityCache.Lock()
				if securityCache.generation == generation {
					securityCache.value = snapshot
				}
				securityCache.refreshing = false
				securityCache.Unlock()
			}()
		}

		securityCache.Unlock()

		return current
	}

	securityCache.Unlock()

	securityCache.reading.Lock()
	defer securityCache.reading.Unlock()

	// Somebody else may have finished a read while this one waited for it.
	started := time.Now()

	securityCache.Lock()
	if !fresh && securityCache.value != nil && time.Since(securityCache.value.Taken) < securityStaleAfter {
		current := securityCache.value
		securityCache.Unlock()

		return current
	}
	securityCache.Unlock()

	snapshot := a.readSnapshot()

	securityCache.Lock()
	if securityCache.value == nil || securityCache.value.Taken.Before(started) {
		securityCache.value = snapshot
	}
	securityCache.Unlock()

	return snapshot
}

// readSnapshot asks the machine. The parts are independent and each a few
// hundred milliseconds of sudo, so they are asked at once.
func (a *App) readSnapshot() *securitySnapshot {
	var (
		wait     sync.WaitGroup
		snapshot = &securitySnapshot{Taken: time.Now()}
	)

	wait.Add(6)
	go func() { defer wait.Done(); snapshot.Jail = security.Status() }()
	go func() { defer wait.Done(); snapshot.SSH = security.ReadSSH() }()
	go func() { defer wait.Done(); snapshot.Firewall = security.ReadFirewall() }()
	go func() { defer wait.Done(); snapshot.Updates = security.ReadUpdates() }()
	go func() { defer wait.Done(); snapshot.Events = security.ReadEvents(7*24*time.Hour, 1000) }()
	go func() { defer wait.Done(); snapshot.Users = security.ReadUsers(a.Config.SystemUser) }()

	// Only when the trap is on: asking fail2ban is a Python start-up.
	if a.sshTrapEnabled() {
		wait.Add(1)
		go func() { defer wait.Done(); status := security.TrapStatus(); snapshot.Trap = &status }()
	}

	wait.Wait()

	return snapshot
}

// check is one line of the security checklist.
type check struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Level  string `json:"level"` // critical | warning | info
	Detail string `json:"detail"`
	Tab    string `json:"tab"`
}

// checks are what the score is made of, worst first when they fail.
func (a *App) checks(ctx context.Context, user *store.User, snap *securitySnapshot) []check {
	jail := a.jailConfig()
	sshOff := snap.SSH.Readable && !snap.SSH.Service.Running

	list := []check{
		{
			Key: "ssh_bruteforce", Label: "SSH brute-force protection", Level: "critical", Tab: "ssh",
			OK: snap.Jail.Running && snap.Jail.Managed,
			Detail: map[bool]string{
				true: fmt.Sprintf("fail2ban bans an address after %d failed attempts, %s.",
					jail.MaxRetry, banDescription(jail.BanTime)),
				false: "Nothing stops an address from guessing SSH passwords forever.",
			}[snap.Jail.Running && snap.Jail.Managed],
		},
		{
			Key: "firewall", Label: "Firewall", Level: "critical", Tab: "firewall",
			OK: snap.Firewall.Active,
			Detail: map[bool]string{
				true:  fmt.Sprintf("ufw is on with %d rules; anything not allowed is refused.", len(snap.Firewall.Rules)),
				false: "ufw is off, so every port something listens on is open to the internet.",
			}[snap.Firewall.Active],
		},
		{
			Key: "root_login", Label: "Root cannot log in with a password", Level: "warning", Tab: "hardening",
			OK: snap.SSH.Readable && snap.SSH.PermitRootLogin != "yes",
			Detail: map[bool]string{
				true:  "root can only log in with a key, or not at all.",
				false: "root can log in over SSH with a password, which is the account every scanner tries first.",
			}[snap.SSH.Readable && snap.SSH.PermitRootLogin != "yes"],
		},
		{
			Key: "password_auth", Label: "SSH keys instead of passwords", Level: "info", Tab: "hardening",
			OK: snap.SSH.Readable && !snap.SSH.PasswordAuthentication,
			Detail: map[bool]string{
				true:  "Passwords are refused; only keys log in.",
				false: "SSH accepts passwords. A key cannot be guessed — turn passwords off once yours is installed.",
			}[snap.SSH.Readable && !snap.SSH.PasswordAuthentication],
		},
		{
			Key: "two_factor", Label: "Two-factor sign-in to the panel", Level: "warning", Tab: "profile",
			OK: user != nil && a.Store.TwoFactorEnabled(ctx, user.ID),
			Detail: map[bool]string{
				true:  "Signing in to the panel needs a code from your phone as well as the password.",
				false: "Your password alone signs in to a panel that controls the whole server.",
			}[user != nil && a.Store.TwoFactorEnabled(ctx, user.ID)],
		},
		{
			Key: "panel_https", Label: "Panel served over HTTPS", Level: "warning", Tab: "settings",
			OK: a.Store.Setting("panel_domain", "") != "" && a.Store.SettingBool("panel_domain_tls", false),
			Detail: map[bool]string{
				true:  "The panel has a hostname and a trusted certificate.",
				false: "Give the panel a hostname in Settings, so passwords and codes are not sent in the clear.",
			}[a.Store.Setting("panel_domain", "") != "" && a.Store.SettingBool("panel_domain_tls", false)],
		},
		{
			Key: "auto_updates", Label: "Automatic security updates", Level: "warning", Tab: "updates",
			OK: snap.Updates.Automatic,
			Detail: map[bool]string{
				true:  "unattended-upgrades installs security patches every day.",
				false: "Security patches wait until somebody installs them by hand.",
			}[snap.Updates.Automatic],
		},
		{
			Key: "pending_updates", Label: "No security updates waiting", Level: "warning", Tab: "updates",
			OK: snap.Updates.Security == 0,
			Detail: map[bool]string{
				true:  "Every security update apt knows about is installed.",
				false: fmt.Sprintf("%d security updates are waiting to be installed.", snap.Updates.Security),
			}[snap.Updates.Security == 0],
		},
		{
			Key: "alerts", Label: "Security alerts by email", Level: "info", Tab: "alerts",
			OK: a.smtpConfig().Ready() && a.anyAlertEnabled(),
			Detail: map[bool]string{
				true:  "Sign-ins and lockouts are emailed to " + a.alertRecipient(ctx) + ".",
				false: "Connect an SMTP server in Settings to be emailed about sign-ins and lockouts.",
			}[a.smtpConfig().Ready() && a.anyAlertEnabled()],
		},
	}

	// With SSH off there is no login to guess or harden.
	if sshOff {
		for i := range list {
			switch list[i].Key {
			case "ssh_bruteforce", "root_login", "password_auth":
				list[i].OK = true
				list[i].Detail = "SSH is turned off, so nobody can log in over it."
			}
		}
	}

	if snap.Updates.Reboot {
		list = append(list, check{
			Key: "reboot", Label: "Reboot to finish updates", Level: "info", Tab: "updates",
			Detail: "An installed update — usually the kernel — only takes effect after a reboot.",
		})
	}

	return list
}

// score is the share of the checklist that passes, weighted so a missing
// firewall costs more than a missing alert.
func score(list []check) int {
	weights := map[string]int{"critical": 3, "warning": 2, "info": 1}

	total, earned := 0, 0

	for _, item := range list {
		total += weights[item.Level]
		if item.OK {
			earned += weights[item.Level]
		}
	}

	if total == 0 {
		return 100
	}

	return earned * 100 / total
}

func eventsSince(events []security.Event, window time.Duration) []security.Event {
	cutoff := time.Now().Add(-window)
	recent := []security.Event{}

	for _, event := range events {
		if event.Time.After(cutoff) {
			recent = append(recent, event)
		}
	}

	return recent
}

// sshEventsSetting is when sshd's log was last copied into the panel.
const sshEventsSetting = "ssh_events_recorded_at"

// recordSSHEvents copies the SSH logins and attempts since the last copy out of
// sshd's log and into the panel's own table, which the Logs page reads and
// which outlives the journal's rotation. A week back the first time — as far
// as the Security page ever reads — and then only the minutes since, with two
// minutes of overlap so a line written as the last copy ran is not missed. The
// table ignores what it already has.
func (a *App) recordSSHEvents(ctx context.Context) {
	defer func() {
		if problem := recover(); problem != nil {
			a.Log.Error("copying the SSH log panicked", "panic", problem)
		}
	}()

	now := time.Now()
	window := 7 * 24 * time.Hour

	if last, err := strconv.ParseInt(a.Store.Setting(sshEventsSetting, ""), 10, 64); err == nil && last > 0 {
		if since := now.Sub(time.Unix(last, 0)) + 2*time.Minute; since < window {
			window = since
		}
	}

	events := security.ReadEvents(window, 0)
	kept := make([]store.SSHEvent, 0, len(events))

	for _, event := range events {
		kept = append(kept, store.SSHEvent{
			Kind: event.Kind, User: event.User, IP: event.IP, Method: event.Method,
			CreatedAt: event.Time.Unix(),
		})
	}

	a.Store.RecordSSHEvents(ctx, kept)
	a.Store.SetSetting(sshEventsSetting, strconv.FormatInt(now.Unix(), 10))
}

// ------------------------------------------------------------------- page

func (a *App) securityIndex(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	sess := session.FromContext(r.Context())

	snap := a.snapshot(r.URL.Query().Get("fresh") == "1")
	list := a.checks(r.Context(), user, snap)

	limit, window := a.loginLimits()
	succeeded, failed := a.Store.LoginCounts(r.Context(), time.Now().Add(-24*time.Hour))
	attempts, _ := a.Store.LoginAttempts(r.Context(), 200)

	locked := []map[string]any{}
	for ip, until := range a.Store.LockedAddresses(r.Context(), limit, window) {
		locked = append(locked, map[string]any{"ip": ip, "until": until})
	}

	sessions, _ := a.Store.SessionsFor(r.Context(), user.ID, time.Now().Add(-session.Lifetime))
	current := store.SessionKey(sess.ID)

	sessionList := make([]map[string]any, 0, len(sessions))
	for _, entry := range sessions {
		sessionList = append(sessionList, map[string]any{
			"key":           entry.Key,
			"ip":            entry.IP,
			"user_agent":    entry.UserAgent,
			"last_activity": entry.LastActivity,
			"current":       entry.Key == current,
		})
	}

	events := snap.Events
	if len(events) > 300 {
		events = events[:300]
	}

	a.Render.Render(w, r, "System/Security", inertia.Props{
		"score":  score(list),
		"checks": list,
		"myIp":   session.ClientIP(r),
		"ssh": map[string]any{
			"jail":        a.jailConfig(),
			"status":      snap.Jail,
			"events":      events,
			"summary_24h": security.Summarise(eventsSince(snap.Events, 24*time.Hour), 10),
			"summary_7d":  security.Summarise(snap.Events, 10),
			"installing":  a.Store.ServicesPending(),
		},
		"hardening":  snap.SSH,
		"sshService": a.sshServiceProps(snap.SSH.Service),
		"sshTrap": map[string]any{
			"enabled": a.sshTrapEnabled(),
			"status":  snap.Trap,
		},
		"bans":      a.bans(r.Context(), snap),
		"users":     a.sshUsers(snap),
		"firewall":  snap.Firewall,
		"panelPort": a.panelPort(),
		"updates":   snap.Updates,
		"logins": map[string]any{
			"attempts":        attempts,
			"succeeded_24h":   succeeded,
			"failed_24h":      failed,
			"locked":          locked,
			"max_attempts":    limit,
			"lockout_minutes": int(window.Minutes()),
		},
		"sessions":  sessionList,
		"alerts":    a.alertSettings(r.Context()),
		"smtpReady": a.smtpConfig().Ready(),
		"checkedAt": snap.Taken.Unix(),
		"activeTask": a.Store.TaskConsole(r.Context(),
			a.Store.LatestTaskByActions(r.Context(), "security", securityTaskActions, true), 0),
		"latestTask": a.Store.TaskConsole(r.Context(),
			a.Store.RecentTaskID(r.Context(), "security", securityTaskActions, store.RecentTaskWindow), 0),
	})
}

// countryLookup keeps two pages from asking the lookup service about the same
// addresses at once, which would spend its rate limit twice for one answer.
var countryLookup sync.Mutex

// securityCountries is where the addresses on the Security page are, for the
// flags beside them. Asked for by the browser after the page is up rather than
// worked out while rendering it, so a slow lookup costs a flag, not the page.
func (a *App) securityCountries(w http.ResponseWriter, r *http.Request) {
	asked := []string{}
	seen := map[string]bool{}

	for _, address := range strings.Split(r.URL.Query().Get("ips"), ",") {
		address = strings.TrimSpace(address)
		if security.ValidIP(address) && !seen[address] && len(asked) < 200 {
			seen[address] = true
			asked = append(asked, address)
		}
	}

	countryLookup.Lock()
	defer countryLookup.Unlock()

	countries := a.Store.IPCountries(r.Context(), asked)

	missing := []string{}
	for _, address := range asked {
		if _, known := countries[address]; known {
			continue
		}

		if security.Placeable(address) {
			missing = append(missing, address)
		} else {
			countries[address] = ""
		}
	}

	if len(missing) > 0 {
		found, err := security.LookupCountries(missing)
		if err != nil {
			a.Log.Warn("could not look up where addresses are", "error", err)
		} else {
			a.Store.SaveIPCountries(r.Context(), found)

			for address, country := range found {
				countries[address] = country
			}
		}
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"countries": countries})
}

// securityBack returns to the Security page with the cache dropped, so what
// was just changed is what the page shows.
func (a *App) securityBack(w http.ResponseWriter, r *http.Request, message string, failed bool) {
	securityCache.forget()

	if sess := session.FromContext(r.Context()); sess != nil {
		if failed {
			sess.Flash("error", message)
		} else if message != "" {
			sess.Flash("success", message)
		}
	}

	inertia.Back(w, r, "/security")
}

// --------------------------------------------------------- SSH protection

func (a *App) securitySSHProtection(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	config := security.JailConfig{
		MaxRetry:  int(intInput(input, "max_retry", 0)),
		FindTime:  int(intInput(input, "find_time", 0)),
		BanTime:   int(intInput(input, "ban_time", 0)),
		IgnoreIPs: splitAddresses(input["ignore_ips"]),
	}

	if boolInput(input, "permanent", false) {
		config.BanTime = security.Permanent
	}

	for field, problem := range config.Validate() {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/security")
		return
	}

	encoded, _ := json.Marshal(config)
	a.Store.SetSetting(jailSetting, string(encoded))

	if !proc.Which("fail2ban-client") {
		a.securityInstallFail2ban(w, r)
		return
	}

	if err := a.applySSHProtection(nil); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.securityBack(w, r, fmt.Sprintf("Saved. %d failed SSH attempts and an address is %s.",
		config.MaxRetry, banDescription(config.BanTime)), false)
}

// securityInstallFail2ban puts fail2ban through the Apps install, whose last
// step writes the jail.
func (a *App) securityInstallFail2ban(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	if proc.Which("fail2ban-client") {
		if err := a.applySSHProtection(nil); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.securityBack(w, r, "SSH protection is on.", false)
		return
	}

	if a.Catalog.Queue([]string{"fail2ban"}, true) == 0 && !a.Store.ServicesPending() {
		a.securityBack(w, r, "fail2ban could not be queued for install.", true)
		return
	}

	a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

	a.securityBack(w, r, "Installing fail2ban. SSH is protected as soon as it finishes — "+
		"follow it on the Apps page or in Logs.", false)
}

func (a *App) securityBan(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := strings.TrimSpace(input["ip"])

	if address == session.ClientIP(r) {
		a.securityBack(w, r, "That is the address you are using the panel from.", true)
		return
	}

	// The whole server is a ufw block: it outlives fail2ban and the trap, and
	// is refused on every port whatever else is on.
	if input["scope"] == "server" {
		if err := security.BlockAddress(address, strings.TrimSpace(input["comment"])); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.Log.Warn("banned from the server by hand", "ip", address)
		a.securityBack(w, r, address+" is banned from the whole server.", false)

		return
	}

	if err := security.Ban(address); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Warn("banned from SSH by hand", "ip", address)
	a.securityBack(w, r, address+" is banned from SSH.", false)
}

func (a *App) securityUnban(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := strings.TrimSpace(input["ip"])

	if err := security.Unban(address); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("unbanned from SSH", "ip", address)
	a.securityBack(w, r, address+" can connect to SSH again.", false)
}

// securityTrust adds an address to the jail's never-ban list: by default the
// one the request came from, which is the operator protecting themselves from
// their own typing.
func (a *App) securityTrust(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := stringInput(input, "ip", session.ClientIP(r))

	if !security.ValidAddress(address) {
		a.securityBack(w, r, "That is not an IP address or a range.", true)
		return
	}

	config := a.jailConfig()

	for _, existing := range config.IgnoreIPs {
		if existing == address {
			a.securityBack(w, r, address+" is already trusted.", false)
			return
		}
	}

	config.IgnoreIPs = append(config.IgnoreIPs, address)

	encoded, _ := json.Marshal(config)
	a.Store.SetSetting(jailSetting, string(encoded))

	if proc.Which("fail2ban-client") {
		// Unbanned too, in case trusting it is the reason somebody came here.
		security.Unban(address)

		if err := a.applySSHProtection(nil); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}
	}

	a.securityBack(w, r, address+" will never be banned.", false)
}

// ----------------------------------------------------------- panel sign-in

func (a *App) securityLoginProtection(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	limit := intInput(input, "max_attempts", 0)
	minutes := intInput(input, "lockout_minutes", 0)

	if limit < 1 || limit > 50 {
		sess.Error("max_attempts", "Allow between 1 and 50 attempts.")
	}
	if minutes < 1 || minutes > 7*24*60 {
		sess.Error("lockout_minutes", "Lock out for between a minute and a week.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/security")
		return
	}

	a.Store.SetSetting(loginMaxSetting, strconv.FormatInt(limit, 10))
	a.Store.SetSetting(loginLockoutSetting, strconv.FormatInt(minutes, 10))

	a.securityBack(w, r, fmt.Sprintf("Saved. %d failed sign-ins and an address waits %d minutes.",
		limit, minutes), false)
}

func (a *App) securityUnlock(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := strings.TrimSpace(input["ip"])

	if !security.ValidIP(address) {
		a.securityBack(w, r, "That is not an IP address.", true)
		return
	}

	a.Store.ClearLoginFailures(r.Context(), address)

	a.securityBack(w, r, address+" can sign in to the panel again.", false)
}

// ---------------------------------------------------------------- firewall

func (a *App) securityFirewallEnable(w http.ResponseWriter, r *http.Request) {
	if err := security.EnableFirewall(security.SSHPorts(), a.panelPort()); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("firewall enabled")
	a.securityBack(w, r, "The firewall is on. SSH, the web ports and the panel are let through.", false)
}

func (a *App) securityFirewallDisable(w http.ResponseWriter, r *http.Request) {
	if err := security.DisableFirewall(); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Warn("firewall disabled")
	a.securityBack(w, r, "The firewall is off. Its rules are kept for when it is switched back on.", false)
}

func (a *App) securityFirewallRule(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	rule := security.NewRule{
		Action:   stringInput(input, "action", "allow"),
		Port:     strings.TrimSpace(input["port"]),
		Protocol: stringInput(input, "protocol", "tcp"),
		From:     strings.TrimSpace(input["from"]),
		Comment:  strings.TrimSpace(input["comment"]),
	}

	for field, problem := range rule.Validate() {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/security")
		return
	}

	if err := security.AddRule(rule); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.securityBack(w, r, "Rule added.", false)
}

func (a *App) securityFirewallDelete(w http.ResponseWriter, r *http.Request) {
	number := int(routeID(r, "rule"))

	// The rule that lets SSH in is the one whose removal nobody can undo from
	// outside, so it is looked up and refused rather than trusted to a
	// confirmation dialog.
	for _, rule := range security.ReadFirewall().Rules {
		if rule.Number != number {
			continue
		}

		if isSSHRule(rule, security.SSHPorts()) && rule.Action != "DENY" && rule.Action != "REJECT" &&
			countSSHRules(security.ReadFirewall().Rules, security.SSHPorts(), rule.IPv6) <= 1 {
			a.securityBack(w, r, "That is the rule that lets SSH in. Add another first, or switch "+
				"the firewall off — removing it would lock everybody out.", true)
			return
		}
	}

	if err := security.DeleteRule(number); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.securityBack(w, r, "Rule removed.", false)
}

func isSSHRule(rule security.Rule, ports []string) bool {
	to := strings.TrimSpace(strings.TrimSuffix(rule.To, "(v6)"))

	if strings.EqualFold(to, "OpenSSH") || to == "22" || to == "22/tcp" {
		return true
	}

	for _, port := range ports {
		if to == port || to == port+"/tcp" {
			return true
		}
	}

	return false
}

func countSSHRules(rules []security.Rule, ports []string, ipv6 bool) int {
	count := 0

	for _, rule := range rules {
		if rule.IPv6 == ipv6 && isSSHRule(rule, ports) && (rule.Action == "ALLOW" || rule.Action == "LIMIT") &&
			strings.HasPrefix(rule.From, "Anywhere") {
			count++
		}
	}

	return count
}

func (a *App) securityFirewallBlock(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	address := strings.TrimSpace(input["ip"])

	if address == session.ClientIP(r) {
		a.securityBack(w, r, "That is the address you are using the panel from.", true)
		return
	}

	if err := security.BlockAddress(address, strings.TrimSpace(input["comment"])); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Warn("blocked at the firewall", "ip", address)
	a.securityBack(w, r, address+" is blocked on every port.", false)
}

// --------------------------------------------------------------- hardening

func (a *App) securityHardening(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	state := security.ReadSSH()

	config := security.SSHConfig{
		PermitRootLogin:        input["permit_root_login"],
		PasswordAuthentication: boolInput(input, "password_authentication", true),
		MaxAuthTries:           int(intInput(input, "max_auth_tries", 0)),
	}

	for field, problem := range config.Validate(state) {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/security")
		return
	}

	if err := security.ApplySSH(config); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("SSH configuration changed", "root", config.PermitRootLogin,
		"passwords", config.PasswordAuthentication, "max_auth_tries", config.MaxAuthTries)

	if !state.Service.Running {
		a.securityBack(w, r, "Saved. SSH is off; these apply when it is turned back on.", false)
		return
	}

	a.securityBack(w, r, "Saved. sshd has reloaded; sessions that are already open are unaffected — "+
		"check you can open a new one before closing this tab.", false)
}

// ----------------------------------------------------------------- updates

func (a *App) securityAutoUpdates(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	a.Queue.Push(queue.SecurityAutoUpdates, map[string]any{
		"user":   user.ID,
		"enable": boolInput(input, "enable", true),
	})

	a.securityBack(w, r, "", false)
}

func (a *App) securityInstallUpdates(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	a.Queue.PushID(queue.SecurityUpdates, "user", user.ID)

	a.securityBack(w, r, "", false)
}

// registerSecurityJobs is the Security page's share of registerJobs.
func (a *App) registerSecurityJobs() {
	a.Queue.Handle(queue.SecurityAutoUpdates, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			User   int64 `json:"user"`
			Enable bool  `json:"enable"`
		}

		if err := job.Decode(&payload); err != nil {
			return err
		}

		message := "Turning automatic security updates off"
		if payload.Enable {
			message = "Turning automatic security updates on"
		}

		taskID, err := a.Store.CreateTask(ctx, "security", "security.auto-updates", payload.User, 0, message)
		if err != nil {
			return err
		}

		list := &tasks.List{}
		security.AutomaticUpdateSteps(list, payload.Enable)

		defer securityCache.forget()

		if !tasks.New(a.Store, taskID).Run(list) {
			return errors.New("automatic updates could not be changed")
		}

		return nil
	})

	a.Queue.Handle(queue.SecurityUpdates, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			User int64 `json:"user"`
		}

		if err := job.Decode(&payload); err != nil {
			return err
		}

		taskID, err := a.Store.CreateTask(ctx, "security", "security.updates", payload.User, 0,
			"Installing security updates")
		if err != nil {
			return err
		}

		list := &tasks.List{}
		security.InstallSecurityUpdateSteps(list)

		defer securityCache.forget()

		if !tasks.New(a.Store, taskID).Run(list) {
			return errors.New("security updates did not install")
		}

		return nil
	})
}

// ---------------------------------------------------------------- sessions

func (a *App) securityRevokeSession(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	sess := session.FromContext(r.Context())

	key := r.PathValue("session")

	if key == store.SessionKey(sess.ID) {
		a.securityBack(w, r, "That is this browser. Use Log Out instead.", true)
		return
	}

	if err := a.Store.RevokeSession(r.Context(), user.ID, key); err != nil {
		a.securityBack(w, r, "That session has already ended.", true)
		return
	}

	a.securityBack(w, r, "That browser is signed out.", false)
}

func (a *App) securityRevokeOthers(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	sess := session.FromContext(r.Context())

	count, err := a.Store.RevokeOtherSessions(r.Context(), user.ID, sess.ID)
	if err != nil {
		a.securityBack(w, r, "Could not sign the other browsers out.", true)
		return
	}

	a.securityBack(w, r, fmt.Sprintf("Signed out %d other %s.", count,
		map[bool]string{true: "browser", false: "browsers"}[count == 1]), false)
}

// ----------------------------------------------------------------- helpers

// splitAddresses reads a textarea of addresses, one per line or separated by
// commas and spaces, with duplicates dropped.
func splitAddresses(text string) []string {
	seen := map[string]bool{}
	list := []string{}

	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	}) {
		if !seen[field] {
			seen[field] = true
			list = append(list, field)
		}
	}

	return list
}

func banDescription(seconds int) string {
	if seconds == security.Permanent {
		return "banned permanently"
	}

	return "banned for " + humanSeconds(seconds)
}

func humanSeconds(seconds int) string {
	switch {
	case seconds%86400 == 0:
		return plural(seconds/86400, "day")
	case seconds%3600 == 0:
		return plural(seconds/3600, "hour")
	case seconds%60 == 0:
		return plural(seconds/60, "minute")
	}

	return plural(seconds, "second")
}

func plural(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}

	return fmt.Sprintf("%d %ss", count, unit)
}
