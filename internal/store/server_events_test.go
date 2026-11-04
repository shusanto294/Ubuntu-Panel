package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The combined list puts the machine's events among the panel's own, and every
// filter narrows all four tables together.
func TestEventsCombinesFiltersAndPages(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	task, err := st.CreateTask(ctx, "site", "site.create", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE activity_logs SET created_at = datetime(?, 'unixepoch') WHERE id = ?`, now-300, task)

	st.RecordLoginAttempt(ctx, LoginAttempt{IP: "203.0.113.5", Email: "a@b.c", Reason: LoginPassword, CreatedAt: now - 200})
	st.RecordSSHEvents(ctx, []SSHEvent{{Kind: "failed", User: "admin", IP: "192.0.2.9", Method: "password", CreatedAt: now - 50}})

	if err := st.RecordServerEvents(ctx, []ServerEvent{
		{CreatedAt: now - 10, Source: "nginx", Level: "error", IP: "203.0.113.9", Message: "upstream timed out"},
		{CreatedAt: now - 20, Source: "site", Site: "a.example", Level: "info", Message: "cache cleared"},
		{CreatedAt: now - 3*86400, Source: "kernel", Level: "critical", Message: "Out of memory: Killed process"},
	}); err != nil {
		t.Fatal(err)
	}

	day := EventFilter{Since: now - 86400}

	all, total, err := st.Events(ctx, day, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(all) != 5 {
		t.Fatalf("a day: total %d, %d entries", total, len(all))
	}
	if all[0].Event == nil || all[0].Event.Message != "upstream timed out" || all[4].Task == nil {
		t.Fatalf("wrong order: first %+v, last %+v", all[0], all[4])
	}

	if _, total, _ := st.Events(ctx, EventFilter{}, 10, 0); total != 6 {
		t.Fatalf("everything kept: total %d", total)
	}

	errors, total, _ := st.Events(ctx, EventFilter{Since: 0, Level: "error"}, 10, 0)
	if total != 2 || errors[0].Event.Source != "nginx" || errors[1].Event.Source != "kernel" {
		t.Fatalf("errors: total %d", total)
	}

	if _, total, _ := st.Events(ctx, EventFilter{Since: day.Since, Level: "warning"}, 10, 0); total != 3 {
		t.Fatalf("warnings and worse in a day: total %d", total) // nginx error, refused sign-in, failed SSH
	}

	site, total, _ := st.Events(ctx, EventFilter{Site: "a.example"}, 10, 0)
	if total != 1 || site[0].Event.Message != "cache cleared" {
		t.Fatalf("one site: total %d", total)
	}

	found, total, _ := st.Events(ctx, EventFilter{Search: "203.0.113"}, 10, 0)
	if total != 2 || found[0].Event == nil || found[1].Login == nil {
		t.Fatalf("search: total %d", total)
	}

	if _, total, _ := st.Events(ctx, EventFilter{Search: "100%"}, 10, 0); total != 0 {
		t.Fatalf("a %% in a search is a percent sign, not a wildcard: total %d", total)
	}

	kernel, total, _ := st.Events(ctx, EventFilter{Source: "kernel"}, 10, 0)
	if total != 1 || kernel[0].Event.Level != "critical" {
		t.Fatalf("one source: total %d", total)
	}

	second, _, _ := st.Events(ctx, day, 2, 2)
	if len(second) != 2 || second[0].SSH == nil {
		t.Fatalf("second page: %+v", second)
	}

	if _, total, _ := st.Events(ctx, EventFilter{Scope: "server"}, 10, 0); total != 3 {
		t.Fatalf("the server's own: total %d", total)
	}

	if _, total, _ := st.Events(ctx, EventFilter{Scope: "panel"}, 10, 0); total != 3 {
		t.Fatalf("the panel's: total %d", total)
	}

	if _, total, _ := st.Events(ctx, EventFilter{Scope: "panel", Source: "kernel"}, 10, 0); total != 0 {
		t.Fatalf("a server source in the panel's scope: total %d", total)
	}

	if counts := st.EventCounts(ctx, day.Since); counts["nginx"] != 1 || counts[EventsSSH] != 1 || counts["kernel"] != 0 {
		t.Fatalf("counts: %v", counts)
	}

	if removed := st.ClearServerEvents(ctx, []string{"kernel"}, time.Time{}); removed != 1 {
		t.Fatalf("cleared %d", removed)
	}
}

func TestServerEventsAfter(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	st.RecordServerEvents(ctx, []ServerEvent{
		{CreatedAt: now, Source: "kernel", Level: "critical", Message: "Out of memory: Killed process 1"},
		{CreatedAt: now, Source: "nginx", Level: "error", Message: "upstream timed out"},
	})

	cursor := st.LatestServerEventID(ctx)

	st.RecordServerEvents(ctx, []ServerEvent{
		{CreatedAt: now, Source: "kernel", Level: "critical", Message: "Out of memory: Killed process 2"},
		{CreatedAt: now, Source: "php", Level: "warning", Message: "max_children"},
		{CreatedAt: now, Source: "site", Level: "critical", Message: "PHP Fatal error"},
	})

	events, total := st.ServerEventsAfter(ctx, cursor, []string{"critical"}, 1)
	if total != 2 || len(events) != 1 || events[0].Message != "Out of memory: Killed process 2" {
		t.Fatalf("total %d, %+v", total, events)
	}
}

func TestForgetServerEvents(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	st.RecordServerEvents(ctx, []ServerEvent{
		{CreatedAt: now, Source: "system", Level: "info", Message: "web1 ubuntu-panel[1]: level=INFO msg=x"},
		{CreatedAt: now, Source: "system", Level: "info", Message: "web1 systemd[1]: Reloading nginx.service"},
		{CreatedAt: now, Source: "system", Level: "error", Message: "web1 systemd[1]: Reload failed for nginx.service"},
		{CreatedAt: now, Source: "kernel", Level: "info", Message: "ubuntu-panel mentioned by the kernel"},
	})

	drop := func(source, message string) bool { return !strings.Contains(message, "failed") }
	if n := st.ForgetServerEvents(ctx, []string{"system"}, []string{"ubuntu-panel", "Reload"}, drop); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}

	_, total, _ := st.Events(ctx, EventFilter{Since: now - 60}, 10, 0)
	if total != 2 {
		t.Errorf("%d left, want the failed reload and the kernel line", total)
	}
}
