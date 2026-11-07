package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Settings the Security page keeps.
const (
	loginMaxSetting     = "security_login_max_attempts"
	loginLockoutSetting = "security_login_lockout_minutes"
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

// ---------------------------------------------------------------- snapshot

// securitySnapshot is what the Security page reads off the machine: ufw, the
// listening ports, sshd's configuration, the accounts and apt. Only the page
// reads it, and only while somebody has it open — nothing in the background
// asks — so it is kept for a minute between page loads and read again
// afterwards, or at once with "Check again".
type securitySnapshot struct {
	SSH      security.SSHState
	Firewall security.Firewall
	Ports    []security.Port
	Updates  security.Updates
	Users    []security.SSHUser
	Taken    time.Time
}

// securityFreshFor is how long a snapshot is shown before it is read again.
const securityFreshFor = time.Minute

type snapshotCache struct {
	sync.Mutex
	value *securitySnapshot

	// reading serialises the reads themselves: two pages arriving at a cold
	// cache share one read rather than running two.
	reading sync.Mutex
}

var securityCache snapshotCache

// forget drops the cached snapshot, after anything that changes what it says.
func (c *snapshotCache) forget() {
	c.Lock()
	c.value = nil
	c.Unlock()
}

func (c *snapshotCache) current(fresh bool) *securitySnapshot {
	c.Lock()
	defer c.Unlock()

	if !fresh && c.value != nil && time.Since(c.value.Taken) < securityFreshFor {
		return c.value
	}

	return nil
}

func (a *App) snapshot(fresh bool) *securitySnapshot {
	if snap := securityCache.current(fresh); snap != nil {
		return snap
	}

	securityCache.reading.Lock()
	defer securityCache.reading.Unlock()

	// Somebody else may have finished a read while this one waited for it.
	if snap := securityCache.current(false); snap != nil && !fresh {
		return snap
	}

	snap := a.readSnapshot()

	securityCache.Lock()
	securityCache.value = snap
	securityCache.Unlock()

	return snap
}

// readSnapshot asks the machine, every part at once: somebody is waiting.
func (a *App) readSnapshot() *securitySnapshot {
	snap := &securitySnapshot{Taken: time.Now()}

	var wait sync.WaitGroup
	for _, part := range []func(){
		func() { snap.Firewall = security.ReadFirewall() },
		func() { snap.Updates = security.ReadUpdates() },
		func() { snap.SSH = security.ReadSSH() },
		func() { snap.Users = security.ReadUsers(a.Config.SystemUser) },
	} {
		wait.Add(1)
		go func() { defer wait.Done(); part() }()
	}
	wait.Wait()

	// The ports need the firewall's rules.
	snap.Ports = security.ReadPorts(snap.Firewall, security.SSHPorts(), a.panelPort(), a.neededPorts(context.Background()))

	return snap
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
//
// Nothing here bans anybody, so what stands between a password guesser and
// the server is the configuration: SSH that takes keys only, a firewall that
// closes what is not meant to be public, a panel that wants a second factor.
// Those are the checks that weigh most.
func (a *App) checks(ctx context.Context, user *store.User, snap *securitySnapshot) []check {
	sshOff := snap.SSH.Readable && !snap.SSH.Service.Running

	risky := []string{}
	if snap.Firewall.Active {
		for _, port := range snap.Ports {
			if port.Risk == "risky" && port.Open && port.Listening && port.Public {
				risky = append(risky, fmt.Sprintf("%s (%d)", port.Name, port.Port))
			}
		}
	} else {
		risky = security.ExposedServices(snap.Firewall)
	}

	list := []check{
		{
			Key: "firewall", Label: "Firewall", Level: "critical", Tab: "firewall",
			OK: snap.Firewall.Active,
			Detail: map[bool]string{
				true:  fmt.Sprintf("ufw is on with %d rules; any port not opened is refused.", len(snap.Firewall.Rules)),
				false: "ufw is off, so every port something listens on is open to the internet.",
			}[snap.Firewall.Active],
		},
		{
			Key: "exposed_services", Label: "No database or cache is open to the internet", Level: "critical", Tab: "firewall",
			OK: len(risky) == 0,
			Detail: map[bool]string{
				true: "Databases, caches and admin services answer this machine only, or the firewall keeps them in.",
				false: "Reachable from anywhere: " + strings.Join(risky, ", ") + ". Close the port on the Ports & firewall " +
					"tab — an open Redis or MongoDB is the most common way a server is taken over.",
			}[len(risky) == 0],
		},
		{
			Key: "password_auth", Label: "SSH accepts keys only", Level: "critical", Tab: "ssh",
			OK: snap.SSH.Readable && !snap.SSH.PasswordAuthentication,
			Detail: map[bool]string{
				true:  "Passwords are refused; only keys log in, and a key cannot be guessed.",
				false: "SSH accepts passwords, and bots try them all day. Add your key on the SSH tab, then turn passwords off.",
			}[snap.SSH.Readable && !snap.SSH.PasswordAuthentication],
		},
		{
			Key: "root_login", Label: "Root cannot log in with a password", Level: "warning", Tab: "ssh",
			OK: snap.SSH.Readable && snap.SSH.PermitRootLogin != "yes",
			Detail: map[bool]string{
				true:  "root can only log in with a key, or not at all.",
				false: "root can log in over SSH with a password, which is the account every scanner tries first.",
			}[snap.SSH.Readable && snap.SSH.PermitRootLogin != "yes"],
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
				true:  "Sign-ins, lockouts and server problems are emailed to " + a.alertRecipient(ctx) + ".",
				false: "Connect an SMTP server in Settings to be emailed about sign-ins and server problems.",
			}[a.smtpConfig().Ready() && a.anyAlertEnabled()],
		},
	}

	// With SSH off there is no login to guess or harden.
	if sshOff {
		for i := range list {
			switch list[i].Key {
			case "root_login", "password_auth":
				list[i].OK = true
				list[i].Detail = "SSH is turned off, so nobody can log in over it."
			}
		}
	}

	list = append(list, a.hardeningChecks()...)

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

	a.Render.Render(w, r, "System/Security", inertia.Props{
		"score":      score(list),
		"checks":     list,
		"myIp":       session.ClientIP(r),
		"hardening":  snap.SSH,
		"sshService": a.sshServiceProps(snap.SSH.Service),
		"server":     a.hardeningProps(r.Context()),
		"users":      a.sshUsers(r.Context(), snap),
		"firewall":   snap.Firewall,
		"ports":      snap.Ports,
		"forwards":   a.forwards(),
		"panelPort":  a.panelPort(),
		"updates":    snap.Updates,
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

// securityFirewallEnable switches the firewall on with only what the server
// needs open, and securityCloseUnneeded closes the rest on one that is on:
// both are CloseUnneeded, which opens what is needed before closing anything.
func (a *App) securityFirewallEnable(w http.ResponseWriter, r *http.Request) {
	a.closeUnneeded(w, r, "The firewall is on")
}

func (a *App) securityCloseUnneeded(w http.ResponseWriter, r *http.Request) {
	a.closeUnneeded(w, r, "Done")
}

func (a *App) closeUnneeded(w http.ResponseWriter, r *http.Request, done string) {
	closed, err := security.CloseUnneeded(liveSSHPorts(), a.panelPort(), a.neededPorts(r.Context()))
	if err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("unneeded ports closed", "closed", strings.Join(closed, " "), "ip", session.ClientIP(r))

	if len(closed) == 0 {
		a.securityBack(w, r, done+". Only what this server needs is open: SSH, the panel, the websites, "+
			"and mail, DNS and databases where they are set up.", false)
		return
	}

	a.securityBack(w, r, done+". Closed "+strings.Join(closed, ", ")+" — only what this server needs is open now.", false)
}

// liveSSHPorts are SSH's ports while SSH is running, and none while it is
// turned off: an SSH that is off needs no port open.
func liveSSHPorts() []string {
	if !security.SSHRunning() {
		return nil
	}
	return security.SSHPorts()
}

// neededPorts are the ports the server's own work needs open to everyone:
// the websites always, mail and DNS when the panel installed them; and the
// ones the owner opened elsewhere in the panel — MariaDB opened to anywhere
// on the Databases page, MongoDB for desktop clients, port forwards — which
// are left open if they are.
func (a *App) neededPorts(ctx context.Context) security.Needed {
	needed := security.Needed{
		Open: map[string]string{"80/tcp": "Websites", "443/tcp": "Websites"},
		Keep: map[string]string{},
	}

	for _, port := range liveSSHPorts() {
		if port == "ssh" {
			port = "22"
		}
		needed.Keep[port+"/tcp"] = "SSH"
	}

	if a.Store.ServiceInstalled("mail") {
		for _, port := range []string{"25", "465", "587", "993", "995"} {
			needed.Open[port+"/tcp"] = "Mail"
		}
	}

	if a.Store.ServiceInstalled("dns") {
		needed.Open["53/tcp"] = "DNS"
		needed.Open["53/udp"] = "DNS"
	}

	var mysql []string
	a.settingJSON("mysql_firewall_applied", &mysql)
	for _, source := range mysql {
		if source == "any" {
			needed.Keep["3306/tcp"] = "Databases (opened to anywhere on the Databases page)"
		}
	}

	if a.Store.ServiceInstalled("mongodb") {
		needed.Keep["27017/tcp"] = "Databases (MongoDB, for desktop clients)"
	}

	for _, forward := range a.forwards() {
		needed.Keep[fmt.Sprintf("%d/%s", forward.Port, forward.Protocol)] = "Port forwarding"
	}

	return needed
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
		Action:   "allow",
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

	// The rule that lets the panel in is the one whose removal cannot be
	// undone from here, so it is looked up and refused rather than trusted to
	// a confirmation dialog — while it is the last one for its address family.
	panel := fmt.Sprintf("%d/tcp", a.panelPort())
	rules := security.ReadFirewall().Rules

	for _, rule := range rules {
		if rule.Number != number || !security.RuleOpens(rule, panel) {
			continue
		}

		others := 0
		for _, other := range rules {
			if other.Number != number && other.IPv6 == rule.IPv6 && security.RuleOpens(other, panel) {
				others++
			}
		}

		if others == 0 {
			a.securityBack(w, r, "That is the rule that lets you reach this panel. Removing it would lock you out.", true)
			return
		}
	}

	if err := security.DeleteRule(number); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.securityBack(w, r, "Rule removed.", false)
}

// securityPort opens a port to everyone or closes it, from the switch beside
// it on the Ports & firewall tab. Closing removes only the rules that open it
// to everyone; one for a particular address stays.
func (a *App) securityPort(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	port := int(intInput(input, "port", 0))
	protocol := input["protocol"]
	open := boolInput(input, "open", false)
	name := fmt.Sprintf("%d/%s", port, protocol)

	if !security.ReadFirewall().Active {
		a.securityBack(w, r, "The firewall is off, so every port is already reachable. Switch it on first.", true)
		return
	}

	if open {
		err = security.OpenPort(port, protocol, strings.TrimSpace(input["note"]))
	} else {
		err = security.ClosePort(port, protocol, a.panelPort())
	}

	if err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.Log.Info("port changed", "port", name, "open", open, "ip", session.ClientIP(r))

	if open {
		a.securityBack(w, r, "Port "+name+" is open to the internet.", false)
		return
	}

	a.securityBack(w, r, "Port "+name+" is closed. The internet can no longer reach it.", false)
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
