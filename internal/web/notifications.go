package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/notify"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// Notifications are how the panel tells its owner what they would otherwise
// have to go looking for: an attack, a sign-in from a new address, SSH
// logins, a disk filling up, a certificate not renewing, a failed backup.
//
// Every alert the minute clock finds is kept as a notification — shown under
// the bell in the top bar and on the Notifications page whether or not email
// is set up — and, when SMTP is connected, emailed as well. Emails never go
// out from the clock or a request: they are a job on the queue's quick lane,
// retried when the mail server does not answer.

// Levels, worst first.
const (
	levelCritical = "critical"
	levelWarning  = "warning"
	levelInfo     = "info"
)

// alert is one thing to tell the owner. Text is a title line, a blank line,
// and the body, which is how it reads in an email.
type alert struct {
	Kind  string
	Level string
	Link  string
	Text  string
}

func newAlert(kind, level, link, text string) alert {
	return alert{Kind: kind, Level: level, Link: link, Text: text}
}

func (al alert) title() string {
	title, _, _ := strings.Cut(al.Text, "\n")
	return strings.TrimSpace(title)
}

func (al alert) body() string {
	_, body, _ := strings.Cut(al.Text, "\n")
	return strings.TrimSpace(body)
}

// deliver keeps each alert as a notification and, when email is set up,
// queues one email with all of them.
func (a *App) deliver(ctx context.Context, alerts []alert) {
	if len(alerts) == 0 {
		return
	}

	for _, al := range alerts {
		if _, err := a.Store.AddNotification(ctx, store.Notification{
			Kind: al.Kind, Level: al.Level, Title: al.title(), Body: al.body(), Link: al.Link,
		}); err != nil {
			a.Log.Warn("could not keep a notification", "kind", al.Kind, "error", err)
		}
	}

	recipient := a.alertRecipient(ctx)
	if !a.smtpConfig().Ready() || recipient == "" {
		return
	}

	subject := fmt.Sprintf("%s alert on %s", config.Name, system.Hostname())
	if len(alerts) == 1 {
		subject = fmt.Sprintf("[%s] %s", system.Hostname(), alerts[0].title())
	}

	texts := make([]string, len(alerts))
	for i, al := range alerts {
		texts[i] = al.Text
	}

	body := strings.Join(texts, "\n\n---\n\n") +
		"\n\nEvery alert is also under the bell in the panel. Change which ones you get on the " +
		"Alerts tab of the Security page.\n"

	a.queueEmail(recipient, subject, body, false)
}

// emailPayload is a queued email.
type emailPayload struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`

	// Test is the Settings page's test message: its outcome is a
	// notification either way, because nobody is waiting on the request.
	Test bool `json:"test"`
}

func (a *App) queueEmail(to, subject, body string, test bool) {
	if _, err := a.Queue.Push(queue.NotifyEmail, emailPayload{To: to, Subject: subject, Body: body, Test: test}); err != nil {
		a.Log.Error("could not queue an email", "to", to, "error", err)
	}
}

// emailRetries are the waits before each further try: a minute, five, a
// quarter of an hour, an hour. After them the email is given up on.
var emailRetries = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// registerNotificationJobs is the email job.
func (a *App) registerNotificationJobs() {
	a.Queue.HandleQuick(queue.NotifyEmail, func(ctx context.Context, job *store.Job) error {
		var payload emailPayload
		if err := job.Decode(&payload); err != nil {
			return err
		}

		smtp := a.smtpConfig()
		if !smtp.Ready() {
			return errors.New("SMTP is not set up any more")
		}

		sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()

		err := notify.Send(sendCtx, smtp, notify.Message{
			To: []string{payload.To}, Subject: payload.Subject, Body: payload.Body,
		})

		if err == nil {
			if payload.Test {
				a.Store.AddNotification(ctx, store.Notification{
					Kind: "email", Level: levelInfo, Link: "/settings",
					Title: "Test email sent to " + payload.To,
					Body:  "The panel can email you alerts.",
				})
			}
			return nil
		}

		// The test is told at once; anybody waiting for it wants to know now.
		if !payload.Test && job.Attempts <= len(emailRetries) {
			return &queue.RetryLater{After: emailRetries[job.Attempts-1], Reason: err}
		}

		title := "An alert email could not be sent"
		if payload.Test {
			title = "The test email to " + payload.To + " was not sent"
		}

		a.Store.AddNotification(ctx, store.Notification{
			Kind: "email", Level: levelWarning, Link: "/settings", Title: title,
			Body: fmt.Sprintf("%s\n\nSubject: %s\n\nCheck the SMTP settings on the Settings page.", err, payload.Subject),
		})

		return err
	})
}

// ------------------------------------------------------------- task alerts

const (
	alertTaskFailedSetting = "alert_task_failed"
	alertTaskCursorSetting = "alert_task_checked_at"
)

// taskAlerts are the panel's own work that failed since the last look: a
// deployment, a backup, a certificate, an install.
func (a *App) taskAlerts(ctx context.Context) []alert {
	now := time.Now()

	last, _ := strconv.ParseInt(a.Store.Setting(alertTaskCursorSetting, ""), 10, 64)
	a.Store.SetSetting(alertTaskCursorSetting, strconv.FormatInt(now.Unix(), 10))

	if last == 0 || !a.Store.SettingBool(alertTaskFailedSetting, true) {
		return nil
	}

	tasks, err := a.Store.FailedTasksSince(ctx, time.Unix(last, 0).Add(-time.Second))
	if err != nil {
		return nil
	}

	alerts := []alert{}
	for _, task := range tasks {
		message := strings.TrimSpace(task.Message)
		if message == "" {
			message = "It stopped with an error."
		}

		alerts = append(alerts, newAlert("task_failed", levelWarning, "/logs/"+strconv.FormatInt(task.ID, 10),
			fmt.Sprintf("Failed: %s\n\n%s\n\nThe whole output is on the task's page in Logs.", task.Action, message)))
	}

	return alerts
}

// ------------------------------------------------------------------ pages

const notificationsPerPage = 20

// notificationsSummary is what the bell shows: how many are unread, and the
// newest few. Shared with every page, and polled by the bell.
func (a *App) notificationsSummary(ctx context.Context) map[string]any {
	latest, _, err := a.Store.Notifications(ctx, false, "", 8, 0)
	if err != nil {
		latest = []store.Notification{}
	}

	return map[string]any{
		"unread": a.Store.UnreadNotifications(ctx),
		"latest": latest,
	}
}

func (a *App) notificationsJSON(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.notificationsSummary(r.Context()))
}

func (a *App) notificationsIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	unreadOnly := query.Get("filter") == "unread"
	level := query.Get("level")
	if level != levelCritical && level != levelWarning && level != levelInfo {
		level = ""
	}

	page, _ := strconv.Atoi(query.Get("page"))
	if page < 1 {
		page = 1
	}

	list, total, err := a.Store.Notifications(r.Context(), unreadOnly, level, notificationsPerPage,
		(page-1)*notificationsPerPage)
	if err != nil {
		a.serverError(w, r, "could not read the notifications", err)
		return
	}

	pages := max(1, (total+notificationsPerPage-1)/notificationsPerPage)
	if page > pages {
		page = pages
		list, _, _ = a.Store.Notifications(r.Context(), unreadOnly, level, notificationsPerPage,
			(page-1)*notificationsPerPage)
	}

	a.Render.Render(w, r, "Notifications/Index", inertia.Props{
		"notifications": list,
		"filters":       map[string]any{"filter": map[bool]string{true: "unread", false: "all"}[unreadOnly], "level": level},
		"pagination": map[string]any{
			"page": page, "pages": pages, "total": total, "per_page": notificationsPerPage,
		},
		"unreadTotal": a.Store.UnreadNotifications(r.Context()),
		"smtpReady":   a.smtpConfig().Ready(),
	})
}

// notificationsRead marks some read — the ids given — or all of them.
func (a *App) notificationsRead(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	ids := []int64{}
	for _, part := range strings.Split(input["ids"], ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 && input["all"] != "1" {
		a.failBack(w, r, "Choose which notifications to mark as read.")
		return
	}

	a.Store.ReadNotifications(r.Context(), ids)

	if wantsJSON(r) {
		a.writeJSON(w, http.StatusOK, a.notificationsSummary(r.Context()))
		return
	}

	inertia.Back(w, r, "/notifications")
}

// notificationsClear deletes the read ones, or every one.
func (a *App) notificationsClear(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	removed := a.Store.ClearNotifications(r.Context(), input["which"] != "all")

	if removed == 0 {
		a.okBack(w, r, "There was nothing to clear.")
		return
	}

	a.okBack(w, r, fmt.Sprintf("Cleared %d notifications.", removed))
}

// wantsJSON is a fetch from the bell rather than an Inertia visit.
func wantsJSON(r *http.Request) bool {
	return !inertia.IsInertia(r) && strings.Contains(r.Header.Get("Accept"), "application/json")
}
