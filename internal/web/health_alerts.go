package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// The alerts about the machine rather than about who signed in: a CPU that
// stays pinned, a disk filling up, mail piling up in the queue, a certificate
// that is not being renewed, and addresses the Protection tab's jails banned. Each is a checkbox on the Alerts tab, and
// each is sent at most once in its cooldown, so a problem that lasts a day is
// one email and not fourteen hundred.
const (
	alertCPUSetting       = "alert_cpu_high"
	alertDiskSetting      = "alert_disk_full"
	alertMailQueueSetting = "alert_mail_queue"
	alertCertsSetting     = "alert_cert_expiry"
	alertWebBanSetting    = "alert_web_ban"
	alertWebBannedSetting = "alert_web_banned"

	alertAttacksSetting     = "alert_attacks"
	alertAttacksSeenSetting = "alert_attacks_seen"
	alertCriticalSetting    = "alert_critical_logs"
	alertCriticalCursor     = "alert_critical_cursor"

	alertSuspiciousSetting = "alert_suspicious_process"
)

// healthAlertDefaults are on for the problems and off for the digest: a busy
// server bans hundreds of addresses a day.
var healthAlertDefaults = map[string]bool{
	alertCPUSetting:       true,
	alertDiskSetting:      true,
	alertMailQueueSetting: true,
	alertCertsSetting:     true,
	alertWebBanSetting:    false,
	alertAttacksSetting:   true,
	alertCriticalSetting:  true,

	alertSuspiciousSetting: true,
}

// healthAlertKeys are the form's names for them.
var healthAlertKeys = map[string]string{
	"cpu_high":    alertCPUSetting,
	"disk_full":   alertDiskSetting,
	"mail_queue":  alertMailQueueSetting,
	"cert_expiry": alertCertsSetting,
	"web_ban":     alertWebBanSetting,
	"attacks":     alertAttacksSetting,
	"critical":    alertCriticalSetting,
	"suspicious":  alertSuspiciousSetting,
}

func (a *App) healthAlert(setting string) bool {
	return a.Store.SettingBool(setting, healthAlertDefaults[setting])
}

const (
	cpuAlertPercent = 90
	cpuAlertMinutes = 5
	diskAlertAt     = 90
	mailQueueAlert  = 100
	certAlertDays   = 14
)

var health struct {
	sync.Mutex
	cpu        system.CPUMeter
	hotMinutes int
	certsAt    time.Time

	// minute counts the checks, for the jobs that run every few minutes.
	minute int
}

// The every-five-minutes jobs, each on a minute of its own (minute%5) rather
// than all in the same one: together they were one solid run at 100% on a
// one-core VPS, every five minutes. The nginx sync in runSecurityAlerts has
// minute 0.
const (
	webBansMinute    = 1
	scanMinute       = 2
	attacksMinute    = 3
	watchMinute      = 4
	watchEveryMinute = 15
)

// cooled is whether an alert may be sent again, and marks it sent if so.
func (a *App) cooled(kind string, cooldown time.Duration) bool {
	key := "alert_last_" + kind

	last, _ := strconv.ParseInt(a.Store.Setting(key, ""), 10, 64)
	if last > 0 && time.Since(time.Unix(last, 0)) < cooldown {
		return false
	}

	a.Store.SetSetting(key, strconv.FormatInt(time.Now().Unix(), 10))

	return true
}

// healthAlerts are the alerts above, checked every minute.
func (a *App) healthAlerts(ctx context.Context) []alert {
	alerts := []alert{}

	health.Lock()
	cpu := health.cpu.Read()
	if cpu >= cpuAlertPercent {
		health.hotMinutes++
	} else if cpu >= 0 {
		health.hotMinutes = 0
	}
	hot := health.hotMinutes
	health.minute++
	minute := health.minute
	health.Unlock()

	// The web bans list moves forward whether or not anything is sent, like
	// the SSH one, so switching the alert on is about bans from then on.
	// Every five minutes, and the list is compared with the one before, so
	// a ban made in between is still told about — five minutes later at most.
	webBans, webBanned := "", []string(nil)
	if minute%5 == webBansMinute {
		webBans, webBanned = a.webBanAlert(a.healthAlert(alertWebBanSetting))
	}

	if hot >= cpuAlertMinutes && a.healthAlert(alertCPUSetting) && a.cooled("cpu", time.Hour) {
		alerts = append(alerts, newAlert("cpu_high", levelCritical, "/logs", a.cpuAlert(ctx, hot)))
	}

	// A miner, or a program run from /tmp: every five minutes, at most one
	// email in six hours.
	if a.healthAlert(alertSuspiciousSetting) && minute%5 == scanMinute {
		if found := security.ScanProcesses(); len(found) > 0 && a.cooled("suspicious", 6*time.Hour) {
			alerts = append(alerts, newAlert("suspicious", levelCritical, "/logs", suspiciousAlert(found)))
		}
	}

	// The backdoor watch and the code change monitor alert whenever they
	// are on: turning one on is asking to be told. The watch every fifteen
	// minutes: its picture is a few hundred small programs (a hash per key,
	// cron file and unit), and what it looks for is how an intruder keeps a
	// way back in, not a moment-to-moment attack.
	if minute%watchEveryMinute == watchMinute {
		if changes := a.checkWatch(ctx); len(changes) > 0 {
			alerts = append(alerts, newAlert("backdoor", levelCritical, "/security?tab=protection", watchAlert(changes)))
		}
	}
	if a.protection().Integrity {
		go func() {
			if changes := a.checkIntegrity(context.WithoutCancel(ctx)); len(changes) > 0 {
				a.deliver(context.WithoutCancel(ctx), []alert{newAlert("code_changed", levelWarning, "/security?tab=protection", codeAlert(changes))})
			}
		}()
	}

	metrics := system.Read()
	if metrics.DiskPercent >= diskAlertAt && a.healthAlert(alertDiskSetting) && a.cooled("disk", 12*time.Hour) {
		alerts = append(alerts, newAlert("disk_full", levelCritical, "/dashboard", fmt.Sprintf("The disk is %.0f%% full\n\n"+
			"%.1f GB free of %.1f GB. When it is full, databases stop writing, mail is refused and "+
			"sites fail. Old backups and logs (Logs → Clear logs) are the usual places to make room.",
			metrics.DiskPercent, metrics.DiskFree, metrics.DiskTotal)))
	}

	if a.healthAlert(alertMailQueueSetting) && a.Store.ServiceInstalled("mail") {
		if queued := mailQueue(); queued >= mailQueueAlert && a.cooled("mail_queue", 6*time.Hour) {
			alerts = append(alerts, newAlert("mail_queue", levelWarning, "/logs?view=server&source=mail", fmt.Sprintf("%d messages are waiting in the mail queue\n\n"+
				"A queue this long usually means a mailbox's password was stolen and it is sending "+
				"spam, or a site's contact form is being abused. `sudo mailq` shows who they are from; "+
				"turn on mail login protection on the Security page.", queued)))
		}
	}

	if a.healthAlert(alertCertsSetting) {
		health.Lock()
		due := time.Since(health.certsAt) > 12*time.Hour
		if due {
			health.certsAt = time.Now()
		}
		health.Unlock()

		if due {
			if expiring := a.expiringCertificates(ctx); len(expiring) > 0 && a.cooled("certs", 24*time.Hour) {
				alerts = append(alerts, newAlert("cert_expiry", levelWarning, "/sites", fmt.Sprintf("%d certificate(s) expire within %d days\n\n%s\n\n"+
					"certbot renews them 30 days ahead, so one this close means renewal is failing — "+
					"usually DNS that no longer points here. Request it again from the site's page.",
					len(expiring), certAlertDays, strings.Join(expiring, "\n"))))
			}
		}
	}

	// Every five minutes: ban what the last hour says is attacking, when the
	// protection for it is on, and tell about it when the alert is on.
	if p := a.protection(); a.healthAlert(alertAttacksSetting) || p.WebLogin.Enabled || p.WebScan.Enabled {
		if minute%5 == attacksMinute {
			// Read gently: nobody is waiting on this report, so one log file
			// at a time rather than four at once.
			report, _ := a.markTrusted(ctx, a.traffic(gently(ctx), findTrafficWindow("1h")), "")
			banned := a.autoBan(report.TopIPs)

			if a.healthAlert(alertAttacksSetting) {
				if text, ips := a.attackAlert(report, banned); text != "" {
					alerts = append(alerts, newAlert("attacks", levelWarning, "/logs", text).with("ips", ips))
				}
			}
		}
	}

	if text, after, until := a.criticalAlert(ctx); text != "" {
		alerts = append(alerts, newAlert("critical", levelCritical, "/logs?view=server&level=error", text).
			with("events_after", after).with("events_until", until))
	}

	if webBans != "" {
		alerts = append(alerts, newAlert("web_ban", levelInfo, "/security?tab=bans", webBans).with("ips", webBanned))
	}

	return alerts
}

// autoBanJail is the protection jail that bans an attack of a kind, or "" when
// that protection is off. Floods go to the scanner jail: both are a script
// hammering the sites, and a flood is often a scanner in a hurry.
func autoBanJail(p security.Protection, attack string) string {
	switch attack {
	case "login":
		if p.WebLogin.Enabled {
			return security.JailWebLogin
		}
	case "flood":
		if p.WebFlood.Enabled {
			return security.JailWebFlood
		}
		if p.WebScan.Enabled {
			return security.JailWebScan
		}
	case "scan":
		if p.WebScan.Enabled {
			return security.JailWebScan
		}
	}

	return ""
}

// autoBan bans the addresses the traffic says are attacking — what "What
// stands out" flags and the attack alert names — through the protection jail
// for what they did, when that protection is on.
//
// fail2ban's own filters catch most of these on their own; this catches the
// rest: a flood of redirects nginx answers before its rate limit runs, a
// scanner asking for files no pattern names. Trusted addresses and useful bots
// are never flagged, so never banned. The answer is the jail each was put in.
func (a *App) autoBan(ips []weblog.IPStat) map[string]string {
	p := a.protection()
	banned := map[string]string{}

	var already map[string]security.JailStatus

	for _, ip := range ips {
		if ip.Trusted != "" || !security.ValidIP(ip.IP) {
			continue
		}

		// A bot saying it is a search engine that publishes its crawlers' DNS
		// is asked: the real one is never banned, whatever it fetched — a
		// banned Googlebot drops the sites from Google — and one only using
		// the name is judged like anybody else.
		// DNS is only asked when the answer matters.
		if ip.GoodBot != nil {
			if judged := weblog.Reassess(ip, 60); judged.Danger || ip.Danger {
				if len(ip.GoodBot.Domains) > 0 && verifyBot(context.Background(), ip.IP, ip.GoodBot).Verified {
					continue
				}
				if len(ip.GoodBot.Domains) > 0 {
					ip = judged
				}
			}
		}

		if !ip.Danger {
			continue
		}

		jail := autoBanJail(p, ip.Attack)
		if jail == "" {
			continue
		}

		// Asked once, and only when there is someone to ban.
		if already == nil {
			already = a.snapshot(false).Protect
		}
		if containsString(already[jail].Banned, ip.IP) {
			banned[ip.IP] = jail
			continue
		}

		if err := security.BanIn(jail, ip.IP); err != nil {
			a.Log.Warn("could not ban an attacking address", "ip", ip.IP, "jail", jail, "error", err)
			continue
		}

		banned[ip.IP] = jail
		a.Log.Info("banned an attacking address", "ip", ip.IP, "jail", jail, "why", ip.Verdict)
	}

	return banned
}

// attackAlert is the addresses the last hour of traffic says are attacking —
// the ones the Logs page's "What stands out" names — that have not been
// emailed about in the last day. Trusted addresses are never in it.
func (a *App) attackAlert(report trafficReport, banned map[string]string) (string, []string) {
	var seen map[string]int64
	json.Unmarshal([]byte(a.Store.Setting(alertAttacksSeenSetting, "{}")), &seen)
	if seen == nil {
		seen = map[string]int64{}
	}

	now := time.Now()
	for ip, at := range seen {
		if now.Sub(time.Unix(at, 0)) > 24*time.Hour {
			delete(seen, ip)
		}
	}

	fresh := []string{}
	addresses := []string{}
	for _, ip := range report.TopIPs {
		if !ip.Danger || ip.Trusted != "" {
			continue
		}
		if _, ok := seen[ip.IP]; ok {
			continue
		}

		seen[ip.IP] = now.Unix()

		site := ""
		if len(ip.Sites) > 0 {
			site = " on " + ip.Sites[0].Name
		}
		outcome := ""
		if banned[ip.IP] != "" {
			outcome = " — banned automatically"
		}
		fresh = append(fresh, fmt.Sprintf("  %s — %s%s (%d requests in the last hour)%s", ip.IP, ip.Verdict, site, ip.Requests, outcome))
		addresses = append(addresses, ip.IP)
	}

	encoded, _ := json.Marshal(seen)
	a.Store.SetSetting(alertAttacksSeenSetting, string(encoded))

	if len(fresh) == 0 {
		return "", nil
	}

	if len(fresh) > 30 {
		fresh = append(fresh[:30], fmt.Sprintf("  …and %d more", len(fresh)-30))
	}

	var advice []string
	p := a.protection()
	if !p.WebLogin.Enabled {
		advice = append(advice, "website login bans")
	}
	if !p.WebScan.Enabled {
		advice = append(advice, "scanner bans")
	}
	if !p.RateLogin.Enabled {
		advice = append(advice, "login rate limits")
	}

	next := "Logs → Server logs on the panel has the detail, with a button to ban each address that was not banned automatically."
	if len(advice) > 0 {
		next += " To have addresses like these banned automatically, turn on " + strings.Join(advice, ", ") +
			" in Security → Protection."
	}

	if len(addresses) > 30 {
		addresses = addresses[:30]
	}

	return fmt.Sprintf("%d %s attacking the sites\n\n%s\n\n%s",
		len(fresh), map[bool]string{true: "address is", false: "addresses are"}[len(fresh) == 1],
		strings.Join(fresh, "\n"), next), addresses
}

// criticalAlert is the critical lines the server's logs gained since the last
// look — an out-of-memory kill, a crash, a fatal error — at most one email
// every half hour, each carrying everything since the one before.
func (a *App) criticalAlert(ctx context.Context) (string, int64, int64) {
	cursor, _ := strconv.ParseInt(a.Store.Setting(alertCriticalCursor, ""), 10, 64)
	latest := a.Store.LatestServerEventID(ctx)

	// From now on, the first time: not a month of the past.
	if cursor == 0 || !a.healthAlert(alertCriticalSetting) {
		a.Store.SetSetting(alertCriticalCursor, strconv.FormatInt(latest, 10))
		return "", 0, 0
	}

	if latest <= cursor {
		return "", 0, 0
	}

	events, total := a.Store.ServerEventsAfter(ctx, cursor, []string{"critical"}, 30)
	if total == 0 {
		a.Store.SetSetting(alertCriticalCursor, strconv.FormatInt(latest, 10))
		return "", 0, 0
	}

	// Held, not lost, while the cooldown runs: the next email has them.
	if !a.cooled("critical", 30*time.Minute) {
		return "", 0, 0
	}

	a.Store.SetSetting(alertCriticalCursor, strconv.FormatInt(latest, 10))

	lines := []string{}
	for _, event := range events {
		where := event.Source
		if event.Site != "" {
			where += " · " + event.Site
		}

		message := event.Message
		if first, _, ok := strings.Cut(message, "\n"); ok {
			message = first
		}
		if len(message) > 300 {
			message = message[:300] + "…"
		}

		lines = append(lines, fmt.Sprintf("  %s  [%s]  %s",
			time.Unix(event.CreatedAt, 0).Format("Jan 2 15:04"), where, message))
	}
	if total > len(events) {
		lines = append(lines, fmt.Sprintf("  …and %d more", total-len(events)))
	}

	return fmt.Sprintf("%d critical %s in the server's logs\n\n%s\n\n"+
		"Logs → Server logs on the panel lists them, with errors only under Level.",
		total, map[bool]string{true: "line", false: "lines"}[total == 1], strings.Join(lines, "\n")), cursor, latest
}

// cpuAlert says what the CPU is busy with: the busiest programs now, and
// where the last fifteen minutes of requests went.
func (a *App) cpuAlert(ctx context.Context, minutes int) string {
	var body strings.Builder

	fmt.Fprintf(&body, "The CPU has been above %d%% for %d minutes\n\n", cpuAlertPercent, minutes)

	processes := system.SampleProcesses(time.Second)
	body.WriteString("Busiest programs right now (100% is one core):\n")
	for i, group := range processes.Groups {
		if i == 5 {
			break
		}
		fmt.Fprintf(&body, "  %-18s %6.0f%%  %d process(es)\n", group.Name, group.CPU, group.Processes)
	}

	report, _ := a.markTrusted(ctx, a.traffic(ctx, findTrafficWindow("1h")), "")

	if len(report.Sites) > 0 {
		body.WriteString("\nRequests to PHP or the app, last hour:\n")
		for i, site := range report.Sites {
			if i == 5 {
				break
			}
			fmt.Fprintf(&body, "  %-30s %7d  (%.0f%%)\n", site.Domain, site.Dynamic, site.PHPShare)
		}
	}

	attackers := []string{}
	for _, ip := range report.TopIPs {
		if ip.Danger && len(attackers) < 5 {
			attackers = append(attackers, fmt.Sprintf("  %s — %s", ip.IP, ip.Verdict))
		}
	}
	if len(attackers) > 0 {
		body.WriteString("\nAddresses that look like an attack:\n" + strings.Join(attackers, "\n") + "\n")
	}

	body.WriteString("\nLogs → Website traffic on the panel has the detail, with a button to ban each address.")

	return body.String()
}

// mailQueue is how many messages Postfix is holding.
func mailQueue() int {
	output := proc.Run("sudo find /var/spool/postfix/deferred /var/spool/postfix/active "+
		"/var/spool/postfix/incoming /var/spool/postfix/hold -type f 2>/dev/null | wc -l", time.Minute).Output

	count, _ := strconv.Atoi(strings.TrimSpace(output))

	return count
}

// expiringCertificates are the sites whose Let's Encrypt certificate ends
// within certAlertDays. Sites whose TLS ends at Cloudflare have none here.
func (a *App) expiringCertificates(ctx context.Context) []string {
	list, err := a.Store.Sites(ctx)
	if err != nil {
		return nil
	}

	expiring := []string{}

	for _, site := range list {
		if !site.SSL || site.EdgeTLS() {
			continue
		}

		path := sites.CertificatePath(site) + "/fullchain.pem"
		if proc.Run("sudo openssl x509 -checkend "+strconv.Itoa(certAlertDays*86400)+" -noout -in "+
			proc.Quote(path)+" >/dev/null 2>&1", 30*time.Second).OK() {
			continue
		}

		end := strings.TrimPrefix(strings.TrimSpace(proc.Run("sudo openssl x509 -enddate -noout -in "+
			proc.Quote(path)+" 2>/dev/null", 30*time.Second).Output), "notAfter=")
		if end == "" {
			continue
		}

		expiring = append(expiring, site.Domain+" — "+end)
	}

	return expiring
}

// webBanAlert is the addresses the optional jails banned since the last look.
func (a *App) webBanAlert(want bool) (string, []string) {
	p := a.protection()
	if !p.AnyJail() {
		return "", nil
	}

	if !want && a.Store.Setting(alertWebBannedSetting, "") == "" {
		return "", nil
	}

	current := []string{}
	byAddress := map[string]string{}

	// From the Security page's copy rather than fail2ban: asking is a Python
	// start-up per jail, and the ban worker writes every ban into the copy as
	// it happens. The nginx sync a few minutes before keeps it fresh.
	for jail, status := range a.snapshot(false).Protect {
		for _, ip := range status.Banned {
			if _, ok := byAddress[ip]; !ok {
				current = append(current, ip)
			}
			byAddress[ip] = jail
		}
	}

	var previous []string
	json.Unmarshal([]byte(a.Store.Setting(alertWebBannedSetting, "[]")), &previous)

	known := map[string]bool{}
	for _, ip := range previous {
		known[ip] = true
	}

	fresh := []string{}
	freshIPs := []string{}
	for _, ip := range current {
		if !known[ip] {
			fresh = append(fresh, ip+"  ("+byAddress[ip]+")")
			freshIPs = append(freshIPs, ip)
		}
	}

	sort.Strings(current)
	if len(current) > 5000 {
		current = current[len(current)-5000:]
	}

	encoded, _ := json.Marshal(current)
	a.Store.SetSetting(alertWebBannedSetting, string(encoded))

	if !want || len(previous) == 0 || len(fresh) == 0 {
		return "", nil
	}

	sort.Strings(fresh)
	shown := fresh
	if len(shown) > 50 {
		shown = shown[:50]
	}

	if len(freshIPs) > 50 {
		freshIPs = freshIPs[:50]
	}

	return fmt.Sprintf("%d %s banned by website or mail protection\n\n%s\n\n"+
		"Unban one from the Banned IPs tab on the Security page.", len(fresh),
		map[bool]string{true: "address", false: "addresses"}[len(fresh) == 1], strings.Join(shown, "\n")), freshIPs
}

// suspiciousAlert is the email about programs that look like malware.
func suspiciousAlert(found []security.SuspiciousProcess) string {
	var text strings.Builder

	fmt.Fprintf(&text, "%d suspicious program%s running on the server\n\n", len(found), map[bool]string{true: " is", false: "s are"}[len(found) == 1])

	for _, process := range found {
		name := process.Name
		if name == "" {
			name = "unknown program"
		}
		fmt.Fprintf(&text, "  %s (pid %d", name, process.PID)
		if process.User != "" {
			fmt.Fprintf(&text, ", user %s", process.User)
		}
		text.WriteString(")")
		if process.Path != "" {
			fmt.Fprintf(&text, " — %s", process.Path)
		}
		fmt.Fprintf(&text, "\n    %s\n", process.Reason)
	}

	text.WriteString("\nThis is what a cryptocurrency miner or other malware dropped through a vulnerable site, " +
		"an exposed service or a stolen password looks like. Nothing was stopped. Check it in the Terminal " +
		"(ps -fp <pid>, ls -l /proc/<pid>/exe); if it is not yours, kill it, find how it got in — the site's access log, " +
		"its user's crontab (crontab -l -u <user>), ~/.ssh/authorized_keys — and change that account's passwords.")

	return text.String()
}
