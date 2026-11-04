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
)

// The alerts about the machine rather than about who signed in: a CPU that
// stays pinned, a disk filling up, mail piling up in the queue, a certificate
// that is not being renewed, files the malware scan found, and addresses the
// Protection tab's jails banned. Each is a checkbox on the Alerts tab, and
// each is sent at most once in its cooldown, so a problem that lasts a day is
// one email and not fourteen hundred.
const (
	alertCPUSetting       = "alert_cpu_high"
	alertDiskSetting      = "alert_disk_full"
	alertMailQueueSetting = "alert_mail_queue"
	alertCertsSetting     = "alert_cert_expiry"
	alertMalwareSetting   = "alert_malware"
	alertWebBanSetting    = "alert_web_ban"
	alertWebBannedSetting = "alert_web_banned"
)

// healthAlertDefaults are on for the problems and off for the digest: a busy
// server bans hundreds of addresses a day.
var healthAlertDefaults = map[string]bool{
	alertCPUSetting:       true,
	alertDiskSetting:      true,
	alertMailQueueSetting: true,
	alertCertsSetting:     true,
	alertMalwareSetting:   true,
	alertWebBanSetting:    false,
}

// healthAlertKeys are the form's names for them.
var healthAlertKeys = map[string]string{
	"cpu_high":    alertCPUSetting,
	"disk_full":   alertDiskSetting,
	"mail_queue":  alertMailQueueSetting,
	"cert_expiry": alertCertsSetting,
	"malware":     alertMalwareSetting,
	"web_ban":     alertWebBanSetting,
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
}

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
func (a *App) healthAlerts(ctx context.Context, ready bool) []string {
	alerts := []string{}

	health.Lock()
	cpu := health.cpu.Read()
	if cpu >= cpuAlertPercent {
		health.hotMinutes++
	} else if cpu >= 0 {
		health.hotMinutes = 0
	}
	hot := health.hotMinutes
	health.Unlock()

	// The web bans list moves forward whether or not anything is sent, like
	// the SSH one, so switching the alert on is about bans from then on.
	webBans := a.webBanAlert(ready && a.healthAlert(alertWebBanSetting))

	if !ready {
		return nil
	}

	if hot >= cpuAlertMinutes && a.healthAlert(alertCPUSetting) && a.cooled("cpu", time.Hour) {
		alerts = append(alerts, a.cpuAlert(ctx, hot))
	}

	metrics := system.Read()
	if metrics.DiskPercent >= diskAlertAt && a.healthAlert(alertDiskSetting) && a.cooled("disk", 12*time.Hour) {
		alerts = append(alerts, fmt.Sprintf("The disk is %.0f%% full\n\n"+
			"%.1f GB free of %.1f GB. When it is full, databases stop writing, mail is refused and "+
			"sites fail. Old backups and logs (Logs → Clear logs) are the usual places to make room.",
			metrics.DiskPercent, metrics.DiskFree, metrics.DiskTotal))
	}

	if a.healthAlert(alertMailQueueSetting) && a.Store.ServiceInstalled("mail") {
		if queued := mailQueue(); queued >= mailQueueAlert && a.cooled("mail_queue", 6*time.Hour) {
			alerts = append(alerts, fmt.Sprintf("%d messages are waiting in the mail queue\n\n"+
				"A queue this long usually means a mailbox's password was stolen and it is sending "+
				"spam, or a site's contact form is being abused. `sudo mailq` shows who they are from; "+
				"turn on mail login protection on the Security page.", queued))
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
				alerts = append(alerts, fmt.Sprintf("%d certificate(s) expire within %d days\n\n%s\n\n"+
					"certbot renews them 30 days ahead, so one this close means renewal is failing — "+
					"usually DNS that no longer points here. Request it again from the site's page.",
					len(expiring), certAlertDays, strings.Join(expiring, "\n")))
			}
		}
	}

	if a.healthAlert(alertMalwareSetting) {
		if unreported, _ := strconv.Atoi(a.Store.Setting(scanUnreportedSetting, "0")); unreported > 0 {
			a.Store.SetSetting(scanUnreportedSetting, "0")

			lines := []string{}
			if last := a.lastScan(); last != nil {
				for i, finding := range last.Findings {
					if i == 30 {
						lines = append(lines, fmt.Sprintf("…and %d more", len(last.Findings)-30))
						break
					}
					lines = append(lines, fmt.Sprintf("%s: %s — %s", finding.Site, finding.Path,
						security.FindingLabels[finding.Kind]))
				}
			}

			alerts = append(alerts, fmt.Sprintf("The malware scan found %d new file(s) to review\n\n%s\n\n"+
				"Nothing was changed or deleted. The Protection tab on the Security page lists them all.",
				unreported, strings.Join(lines, "\n")))
		}
	}

	return append(alerts, webBans...)
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

	report := a.traffic(ctx, findTrafficWindow("15m"))

	if len(report.Sites) > 0 {
		body.WriteString("\nRequests to PHP or the app, last 15 minutes:\n")
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
func (a *App) webBanAlert(want bool) []string {
	p := a.protection()
	if !p.AnyJail() {
		return nil
	}

	if !want && a.Store.Setting(alertWebBannedSetting, "") == "" {
		return nil
	}

	current := []string{}
	byAddress := map[string]string{}

	for jail, status := range security.ProtectStatus(p) {
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
	for _, ip := range current {
		if !known[ip] {
			fresh = append(fresh, ip+"  ("+byAddress[ip]+")")
		}
	}

	sort.Strings(current)
	if len(current) > 5000 {
		current = current[len(current)-5000:]
	}

	encoded, _ := json.Marshal(current)
	a.Store.SetSetting(alertWebBannedSetting, string(encoded))

	if !want || len(previous) == 0 || len(fresh) == 0 {
		return nil
	}

	sort.Strings(fresh)
	shown := fresh
	if len(shown) > 50 {
		shown = shown[:50]
	}

	return []string{fmt.Sprintf("%d %s banned by website or mail protection\n\n%s\n\n"+
		"Unban one from the Banned IPs tab on the Security page.", len(fresh),
		map[bool]string{true: "address", false: "addresses"}[len(fresh) == 1], strings.Join(shown, "\n"))}
}
