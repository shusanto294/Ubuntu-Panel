package web

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// The alerts about the machine rather than about who signed in: a CPU that
// stays pinned, a disk filling up, mail piling up in the queue, a certificate
// that is not being renewed, and critical lines in the server's logs. Each is a checkbox on the Alerts tab, and
// each is sent at most once in its cooldown, so a problem that lasts a day is
// one email and not fourteen hundred.
const (
	alertCPUSetting       = "alert_cpu_high"
	alertDiskSetting      = "alert_disk_full"
	alertMailQueueSetting = "alert_mail_queue"
	alertCertsSetting     = "alert_cert_expiry"
	alertCriticalSetting  = "alert_critical_logs"
	alertCriticalCursor   = "alert_critical_cursor"
)

// healthAlertDefaults are all on: each is a problem.
var healthAlertDefaults = map[string]bool{
	alertCPUSetting:       true,
	alertDiskSetting:      true,
	alertMailQueueSetting: true,
	alertCertsSetting:     true,
	alertCriticalSetting:  true,
}

// healthAlertKeys are the form's names for them.
var healthAlertKeys = map[string]string{
	"cpu_high":    alertCPUSetting,
	"disk_full":   alertDiskSetting,
	"mail_queue":  alertMailQueueSetting,
	"cert_expiry": alertCertsSetting,
	"critical":    alertCriticalSetting,
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
	health.Unlock()

	if hot >= cpuAlertMinutes && a.healthAlert(alertCPUSetting) && a.cooled("cpu", time.Hour) {
		alerts = append(alerts, newAlert("cpu_high", levelCritical, "/logs", a.cpuAlert(ctx, hot)))
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
				"change that mailbox's password on the Email page.", queued)))
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

	if text, after, until := a.criticalAlert(ctx); text != "" {
		alerts = append(alerts, newAlert("critical", levelCritical, "/logs?view=server&level=error", text).
			with("events_after", after).with("events_until", until))
	}

	return alerts
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

	body.WriteString("\nLogs → Website traffic on the panel has the detail. If one site is being flooded, " +
		"Cloudflare's Under Attack mode (Security → Hardening) stops it before it reaches this server.")

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
