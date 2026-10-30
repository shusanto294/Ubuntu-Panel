package store

import (
	"context"
	"testing"
	"time"
)

// The Logs page interleaves the three tables by time and pages over the result.
func TestTimelineInterleavesAndFilters(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	task, err := st.CreateTask(ctx, "site", "site.create", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE activity_logs SET created_at = datetime(?, 'unixepoch') WHERE id = ?`, now-300, task)

	st.RecordLoginAttempt(ctx, LoginAttempt{IP: "203.0.113.5", Email: "a@b.c", Reason: LoginPassword, CreatedAt: now - 200})

	events := []SSHEvent{
		{Kind: "accepted", User: "root", IP: "198.51.100.7", Method: "publickey", CreatedAt: now - 100},
		{Kind: "failed", User: "admin", IP: "192.0.2.9", Method: "password", CreatedAt: now - 50},
	}
	st.RecordSSHEvents(ctx, events)
	st.RecordSSHEvents(ctx, events) // a second, overlapping copy is ignored

	everything, total, err := st.Timeline(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	if total != 4 || len(everything) != 4 {
		t.Fatalf("everything: total %d, %d entries", total, len(everything))
	}

	if everything[0].SSH == nil || everything[0].SSH.Kind != "failed" || everything[1].SSH == nil ||
		everything[2].Source != "panel" || everything[3].Source != "task" {
		t.Fatalf("wrong order: %s %s %s %s", everything[0].Source, everything[1].Source,
			everything[2].Source, everything[3].Source)
	}

	ssh, total, _ := st.Timeline(ctx, TimelineSSH, 10, 0)
	if total != 2 || ssh[0].SSH.Kind != "failed" {
		t.Fatalf("ssh: total %d, first %+v", total, ssh[0].SSH)
	}

	sites, total, _ := st.Timeline(ctx, "site", 10, 0)
	if total != 1 || sites[0].Task.ID != task {
		t.Fatalf("site tasks: total %d", total)
	}

	second, _, _ := st.Timeline(ctx, "", 3, 3)
	if len(second) != 1 || second[0].Source != "task" {
		t.Fatalf("second page: %+v", second)
	}
}

// Clearing removes what is older than the cutoff, of the kinds asked for, and
// never a task that is still running.
func TestClearLogs(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	running, _ := st.CreateTask(ctx, "site", "site.deploy", 0, 0, "")
	finished, _ := st.CreateTask(ctx, "site", "site.create", 0, 0, "")
	st.FinishTask(finished, "success", "", 100)
	st.db.Exec(`UPDATE activity_logs SET created_at = datetime(?, 'unixepoch')`, now-40*86400)

	st.RecordLoginAttempt(ctx, LoginAttempt{IP: "203.0.113.5", Reason: LoginPassword, CreatedAt: now - 40*86400})
	st.RecordLoginAttempt(ctx, LoginAttempt{IP: "203.0.113.6", Reason: LoginPassword, CreatedAt: now - 60})
	st.RecordSSHEvents(ctx, []SSHEvent{{Kind: "failed", User: "a", IP: "192.0.2.1", Method: "password", CreatedAt: now - 40*86400}})

	if removed := st.ClearLogs(ctx, []string{LogsTasks, LogsLogins, LogsSSH}, time.Now().AddDate(0, 0, -30)); removed != 3 {
		t.Fatalf("removed %d, want 3: the finished task, the old sign-in and the old SSH event", removed)
	}

	if _, total, _ := st.Timeline(ctx, "", 10, 0); total != 2 {
		t.Fatalf("left %d, want the running task and the recent sign-in", total)
	}

	st.ClearLogs(ctx, []string{LogsLogins}, time.Time{})

	if _, total, _ := st.Timeline(ctx, TimelinePanelLogins, 10, 0); total != 0 {
		t.Fatalf("clearing every sign-in left %d", total)
	}

	if task, _ := st.FindTask(ctx, running); task == nil {
		t.Fatal("a running task was removed")
	}

	if err := st.Compact(ctx); err != nil {
		t.Fatal(err)
	}
}
