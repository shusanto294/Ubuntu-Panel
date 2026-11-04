package web

import (
	"context"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
)

// Every alert is kept as a notification whether or not email is set up, and
// with email set up, one email for the lot goes on the queue — never sent from
// the clock that found them.
func TestAlertsBecomeNotificationsAndAQueuedEmail(t *testing.T) {
	app := bootstrapApp(t, "none")
	app.registerNotificationJobs()
	ctx := context.Background()

	alerts := []alert{
		newAlert("attacks", levelWarning, "/logs", "1 address is attacking the sites\n\n  203.0.113.9 — a scanner"),
		newAlert("disk_full", levelCritical, "/dashboard", "The disk is 95% full\n\nMake room."),
	}

	// No SMTP: notifications only.
	app.deliver(ctx, alerts)

	if unread := app.Store.UnreadNotifications(ctx); unread != 2 {
		t.Fatalf("unread = %d", unread)
	}
	if jobs, _ := app.Store.QueuedJobs(ctx); len(jobs) != 0 {
		t.Fatalf("an email was queued with no SMTP: %d jobs", len(jobs))
	}

	list, _, _ := app.Store.Notifications(ctx, false, "", 10, 0)
	if list[0].Title != "The disk is 95% full" || list[0].Body != "Make room." || list[0].Level != levelCritical {
		t.Fatalf("newest notification: %+v", list[0])
	}

	// With SMTP: one email for both, on the queue.
	app.Store.SetSetting(smtpHostSetting, "smtp.example.com")
	app.Store.SetSetting(smtpFromEmailSetting, "panel@example.com")
	app.Store.SetSetting(alertEmailSetting, "owner@example.com")

	app.deliver(ctx, alerts)

	jobs, _ := app.Store.QueuedJobs(ctx)
	if len(jobs) != 1 || jobs[0].Kind != queue.NotifyEmail {
		t.Fatalf("queued: %+v", jobs)
	}

	var payload emailPayload
	if err := jobs[0].Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.To != "owner@example.com" || payload.Test {
		t.Fatalf("payload: %+v", payload)
	}

	app.Store.ReadNotifications(ctx, nil)
	if unread := app.Store.UnreadNotifications(ctx); unread != 0 {
		t.Fatalf("unread after reading all = %d", unread)
	}
}
