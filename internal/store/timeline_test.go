package store

import (
	"context"
	"testing"
	"time"
)

// The Logs page interleaves the three tables by time, leaves failed SSH
// attempts out of "everything", and pages over the result.
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

	if total != 3 || len(everything) != 3 {
		t.Fatalf("everything: total %d, %d entries", total, len(everything))
	}

	if everything[0].Source != "ssh" || everything[1].Source != "panel" || everything[2].Source != "task" {
		t.Fatalf("wrong order: %s %s %s", everything[0].Source, everything[1].Source, everything[2].Source)
	}

	ssh, total, _ := st.Timeline(ctx, TimelineSSH, 10, 0)
	if total != 2 || ssh[0].SSH.Kind != "failed" {
		t.Fatalf("ssh: total %d, first %+v", total, ssh[0].SSH)
	}

	sites, total, _ := st.Timeline(ctx, "site", 10, 0)
	if total != 1 || sites[0].Task.ID != task {
		t.Fatalf("site tasks: total %d", total)
	}

	second, _, _ := st.Timeline(ctx, "", 2, 2)
	if len(second) != 1 || second[0].Source != "task" {
		t.Fatalf("second page: %+v", second)
	}
}
