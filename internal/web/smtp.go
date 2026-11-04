package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/notify"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// The SMTP relay the panel sends its own mail through. The password is the
// only secret and the only one stored encrypted.
const (
	smtpHostSetting       = "smtp_host"
	smtpPortSetting       = "smtp_port"
	smtpUsernameSetting   = "smtp_username"
	smtpPasswordSetting   = "smtp_password"
	smtpEncryptionSetting = "smtp_encryption"
	smtpFromEmailSetting  = "smtp_from_email"
	smtpFromNameSetting   = "smtp_from_name"
)

// The alerts, each a checkbox on the Security page.
const (
	alertEmailSetting      = "alert_email"
	alertPanelLoginSetting = "alert_panel_login"
	alertPanelLockSetting  = "alert_panel_lockout"
	alertSSHLoginSetting   = "alert_ssh_login"
	alertSSHBanSetting     = "alert_ssh_ban"
	alertCursorSetting     = "alert_login_cursor"
	alertSSHCheckedSetting = "alert_ssh_checked_at"
	alertBannedSetting     = "alert_ssh_banned"
)

func (a *App) smtpConfig() notify.Config {
	port, _ := strconv.Atoi(a.Store.Setting(smtpPortSetting, "587"))

	return notify.Config{
		Host:       a.Store.Setting(smtpHostSetting, ""),
		Port:       port,
		Username:   a.Store.Setting(smtpUsernameSetting, ""),
		Password:   a.Store.SecretSetting(smtpPasswordSetting),
		Encryption: a.Store.Setting(smtpEncryptionSetting, notify.EncryptionSTARTTLS),
		FromEmail:  a.Store.Setting(smtpFromEmailSetting, ""),
		FromName:   a.Store.Setting(smtpFromNameSetting, config.Name),
	}
}

// smtpProps is the Settings page's form, without the password: the field
// says whether one is stored, and leaving it empty keeps it.
func (a *App) smtpProps() map[string]any {
	current := a.smtpConfig()

	return map[string]any{
		"host":         current.Host,
		"port":         current.Port,
		"username":     current.Username,
		"has_password": current.Password != "",
		"encryption":   current.Encryption,
		"from_email":   current.FromEmail,
		"from_name":    current.FromName,
		"configured":   current.Ready(),
	}
}

func (a *App) smtpUpdate(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	port, _ := strconv.Atoi(strings.TrimSpace(input["smtp_port"]))

	next := notify.Config{
		Host:       strings.TrimSpace(input["smtp_host"]),
		Port:       port,
		Username:   strings.TrimSpace(input["smtp_username"]),
		Encryption: stringInput(input, "smtp_encryption", notify.EncryptionSTARTTLS),
		FromEmail:  strings.TrimSpace(input["smtp_from_email"]),
		FromName:   strings.TrimSpace(input["smtp_from_name"]),
	}

	for field, problem := range next.Validate() {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/settings")
		return
	}

	a.Store.SetSetting(smtpHostSetting, next.Host)
	a.Store.SetSetting(smtpPortSetting, strconv.Itoa(next.Port))
	a.Store.SetSetting(smtpUsernameSetting, next.Username)
	a.Store.SetSetting(smtpEncryptionSetting, next.Encryption)
	a.Store.SetSetting(smtpFromEmailSetting, next.FromEmail)
	a.Store.SetSetting(smtpFromNameSetting, next.FromName)

	if password := input["smtp_password"]; password != "" {
		if err := a.Store.SetSecretSetting(smtpPasswordSetting, password); err != nil {
			a.failBack(w, r, "Could not store the password. The panel's application key may be missing.")
			return
		}
	}

	if next.Username == "" {
		a.Store.ForgetSetting(smtpPasswordSetting)
	}

	a.okBack(w, r, "SMTP settings saved. Send a test message to check them.")
}

func (a *App) smtpTest(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, _ := a.input(r)
	to := stringInput(input, "to", user.Email)

	if !a.smtpConfig().Ready() {
		a.failBack(w, r, "Save the SMTP settings first.")
		return
	}

	// On the queue like every email: the outcome arrives as a notification.
	a.queueEmail(to, config.Name+": test message", fmt.Sprintf("This is a test message from %s on %s.\n\n"+
		"If you are reading it, the panel can email you its alerts.\n", config.Name, system.Hostname()), true)

	a.okBack(w, r, "Sending a test message to "+to+" in the background. The bell in the top bar says whether it went.")
}

func (a *App) smtpDestroy(w http.ResponseWriter, r *http.Request) {
	for _, key := range []string{smtpHostSetting, smtpPortSetting, smtpUsernameSetting,
		smtpPasswordSetting, smtpEncryptionSetting, smtpFromEmailSetting, smtpFromNameSetting} {
		a.Store.ForgetSetting(key)
	}

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "SMTP settings removed. The panel will not send email.")
	}

	inertia.Back(w, r, "/settings")
}

// ------------------------------------------------------------------ alerts

func (a *App) alertRecipient(ctx context.Context) string {
	if address := a.Store.Setting(alertEmailSetting, ""); address != "" {
		return address
	}

	if user, err := a.Store.FindUser(ctx, 1); err == nil {
		return user.Email
	}

	return ""
}

func (a *App) anyAlertEnabled() bool {
	return a.Store.SettingBool(alertPanelLoginSetting, true) ||
		a.Store.SettingBool(alertPanelLockSetting, true) ||
		a.Store.SettingBool(alertSSHLoginSetting, true) ||
		a.Store.SettingBool(alertSSHBanSetting, false) ||
		a.healthAlert(alertCPUSetting) || a.healthAlert(alertDiskSetting) ||
		a.healthAlert(alertAttacksSetting) || a.healthAlert(alertCriticalSetting)
}

func (a *App) alertSettings(ctx context.Context) map[string]any {
	return map[string]any{
		"email":         a.alertRecipient(ctx),
		"panel_login":   a.Store.SettingBool(alertPanelLoginSetting, true),
		"panel_lockout": a.Store.SettingBool(alertPanelLockSetting, true),
		"ssh_login":     a.Store.SettingBool(alertSSHLoginSetting, true),
		"ssh_ban":       a.Store.SettingBool(alertSSHBanSetting, false),
		"cpu_high":      a.healthAlert(alertCPUSetting),
		"disk_full":     a.healthAlert(alertDiskSetting),
		"mail_queue":    a.healthAlert(alertMailQueueSetting),
		"cert_expiry":   a.healthAlert(alertCertsSetting),
		"web_ban":       a.healthAlert(alertWebBanSetting),
		"attacks":       a.healthAlert(alertAttacksSetting),
		"task_failed":   a.Store.SettingBool(alertTaskFailedSetting, true),
		"critical":      a.healthAlert(alertCriticalSetting),
	}
}

func (a *App) securityAlerts(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	email := strings.TrimSpace(input["email"])
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \r\n,")) {
		sess.Error("email", "Enter one email address.")
		inertia.Back(w, r, "/security")
		return
	}

	a.Store.SetSetting(alertEmailSetting, email)

	for key, setting := range map[string]string{
		"panel_login":   alertPanelLoginSetting,
		"panel_lockout": alertPanelLockSetting,
		"ssh_login":     alertSSHLoginSetting,
		"ssh_ban":       alertSSHBanSetting,
	} {
		a.Store.SetSetting(setting, strconv.FormatBool(boolInput(input, key, false)))
	}

	a.Store.SetSetting(alertTaskFailedSetting, strconv.FormatBool(boolInput(input, "task_failed", false)))

	for key, setting := range healthAlertKeys {
		a.Store.SetSetting(setting, strconv.FormatBool(boolInput(input, key, false)))
	}

	a.securityBack(w, r, "Alert settings saved.", false)
}

// runSecurityAlerts is the clock behind the alerts: every minute it looks at
// what happened since it last looked and emails what the operator asked to
// hear about.
//
// It always moves its cursors forward, alerting or not. Switching an alert on
// is then about what happens from that moment, rather than a mailbox full of
// last month.
func (a *App) runSecurityAlerts(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	a.recordSSHEvents(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.recordSSHEvents(ctx)
			a.checkProtection(ctx)
			a.syncNginxBans(ctx)
			go a.collectServerLogs(ctx)
			a.checkAlerts(ctx)
		}
	}
}

func (a *App) checkAlerts(ctx context.Context) {
	defer func() {
		if problem := recover(); problem != nil {
			a.Log.Error("the security alert check panicked", "panic", problem)
		}
	}()

	var alerts []alert

	alerts = append(alerts, a.panelAlerts(ctx)...)
	alerts = append(alerts, a.sshAlerts()...)
	alerts = append(alerts, a.healthAlerts(ctx)...)
	alerts = append(alerts, a.taskAlerts(ctx)...)

	a.pruneLogs(ctx)

	// Shown in the panel, and emailed — on the queue — when it can be.
	a.deliver(ctx, alerts)
}

// panelAlerts are the panel's own sign-ins and lockouts since the last look.
func (a *App) panelAlerts(ctx context.Context) []alert {
	cursor, _ := strconv.ParseInt(a.Store.Setting(alertCursorSetting, ""), 10, 64)

	if cursor == 0 {
		a.Store.SetSetting(alertCursorSetting, strconv.FormatInt(a.Store.LatestLoginAttemptID(ctx), 10))
		return nil
	}

	attempts, err := a.Store.LoginAttemptsAfter(ctx, cursor)
	if err != nil || len(attempts) == 0 {
		return nil
	}

	a.Store.SetSetting(alertCursorSetting, strconv.FormatInt(attempts[len(attempts)-1].ID, 10))

	limit, window := a.loginLimits()
	alerts := []alert{}
	lockedSeen := map[string]bool{}

	for _, attempt := range attempts {
		when := time.Unix(attempt.CreatedAt, 0).Format(time.RFC1123)

		if attempt.Success && a.Store.SettingBool(alertPanelLoginSetting, true) &&
			!a.Store.KnownLoginAddress(ctx, attempt.UserID, attempt.IP, attempt.ID) {
			alerts = append(alerts, newAlert("panel_login", levelWarning, "/security?tab=logins", fmt.Sprintf("New sign-in to the panel from %s\n\n"+
				"Account: %s\nAddress: %s\nBrowser: %s\nTime: %s\n\n"+
				"This address had not signed in before. If it was not you, change your password "+
				"and sign the other browsers out on the Security page.",
				attempt.IP, attempt.Email, attempt.IP, attempt.UserAgent, when)).with("ips", []string{attempt.IP}))
		}

		if !attempt.Success && attempt.Reason != store.LoginLocked && !lockedSeen[attempt.IP] &&
			a.Store.SettingBool(alertPanelLockSetting, true) {
			count, _ := a.Store.FailedLoginsSince(ctx, attempt.IP,
				time.Unix(attempt.CreatedAt, 0).Add(-window))

			if count == limit {
				lockedSeen[attempt.IP] = true
				alerts = append(alerts, newAlert("panel_lockout", levelWarning, "/security?tab=logins", fmt.Sprintf("%s was locked out of the panel\n\n"+
					"%d failed sign-ins in %d minutes, the last for %q at %s. It cannot try again "+
					"for %d minutes.", attempt.IP, limit, int(window.Minutes()), attempt.Email, when,
					int(window.Minutes()))).with("ips", []string{attempt.IP}))
			}
		}
	}

	return alerts
}

// sshAlerts are SSH logins and new bans since the last look.
func (a *App) sshAlerts() []alert {
	alerts := []alert{}

	wantLogins := a.Store.SettingBool(alertSSHLoginSetting, true)
	wantBans := a.Store.SettingBool(alertSSHBanSetting, false)

	checked, _ := strconv.ParseInt(a.Store.Setting(alertSSHCheckedSetting, ""), 10, 64)
	now := time.Now()
	a.Store.SetSetting(alertSSHCheckedSetting, strconv.FormatInt(now.Unix(), 10))

	if wantLogins && checked > 0 {
		since := now.Sub(time.Unix(checked, 0)) + 5*time.Second

		for _, event := range security.ReadEvents(since, 50) {
			if event.Kind != security.EventAccepted || !event.Time.After(time.Unix(checked, 0)) {
				continue
			}

			alerts = append(alerts, newAlert("ssh_login", levelWarning, "/logs?view=panel&source=ssh", fmt.Sprintf("SSH login as %s from %s\n\n"+
				"User: %s\nAddress: %s\nMethod: %s\nTime: %s\n\n"+
				"If this was not you, ban the address on the Security page and change the "+
				"account's password or keys.", event.User, event.IP, event.User, event.IP,
				event.Method, event.Time.Format(time.RFC1123))).with("ips", []string{event.IP}))
		}
	}

	// The ban list is compared with the one seen last time, so an alert is
	// about addresses that are new to it.
	if wantBans || a.Store.Setting(alertBannedSetting, "") != "" {
		current := security.Status().Banned

		var previous []string
		json.Unmarshal([]byte(a.Store.Setting(alertBannedSetting, "[]")), &previous)

		known := map[string]bool{}
		for _, address := range previous {
			known[address] = true
		}

		fresh := []string{}
		for _, address := range current {
			if !known[address] {
				fresh = append(fresh, address)
			}
		}

		// Stored bounded: a busy server bans thousands, and only the newest
		// matter for telling what is new next minute.
		sort.Strings(current)
		if len(current) > 5000 {
			current = current[len(current)-5000:]
		}

		encoded, _ := json.Marshal(current)
		a.Store.SetSetting(alertBannedSetting, string(encoded))

		if wantBans && len(previous) > 0 && len(fresh) > 0 {
			shown := fresh
			if len(shown) > 50 {
				shown = shown[:50]
			}

			alerts = append(alerts, newAlert("ssh_ban", levelInfo, "/security?tab=bans", fmt.Sprintf("%d %s banned from SSH\n\n%s\n\n"+
				"Each failed to log in too many times. They are refused on the SSH port; "+
				"unban one from the Security page.", len(fresh),
				map[bool]string{true: "address", false: "addresses"}[len(fresh) == 1],
				strings.Join(shown, "\n"))).with("ips", shown))
		}
	}

	return alerts
}
