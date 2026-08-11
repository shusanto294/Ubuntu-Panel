package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func taskStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return st
}

// finish marks a task done and moves its finish time back, which is the only
// way to test a window without waiting for one.
func finish(t *testing.T, st *Store, id int64, agoSeconds int) {
	t.Helper()

	if _, err := st.db.Exec(
		`UPDATE activity_logs SET status = 'success',
		        finished_at = datetime('now', ?) WHERE id = ?`,
		"-"+strconv.Itoa(agoSeconds)+" seconds", id); err != nil {
		t.Fatal(err)
	}
}

// The Apps page shows a console while something is happening and for a short
// while afterwards. A task that finished hours ago is not something happening.
func TestRecentTaskIDForgetsAFinishedTask(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()

	id, err := st.CreateTask(ctx, "provision", "services.install", 0, 0, "Installing")
	if err != nil {
		t.Fatal(err)
	}

	if got := st.RecentTaskID(ctx, "provision", nil, 10*time.Minute); got != id {
		t.Fatalf("a running task is %d, want %d", got, id)
	}

	finish(t, st, id, 60)

	if got := st.RecentTaskID(ctx, "provision", nil, 10*time.Minute); got != id {
		t.Fatalf("a task that finished a minute ago is %d, want %d", got, id)
	}

	finish(t, st, id, 4*60*60)

	if got := st.RecentTaskID(ctx, "provision", nil, 10*time.Minute); got != 0 {
		t.Fatalf("a task that finished four hours ago is %d, want none", got)
	}
}

// Several unrelated things are `provision` tasks. A page that only drives one
// of them should not show another's console.
func TestRecentTaskIDStaysWithinItsOwnActions(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()

	if _, err := st.CreateTask(ctx, "provision", "panel.domain", 0, 0, "panel.domain"); err != nil {
		t.Fatal(err)
	}

	apps := []string{"services.install", "services.uninstall"}

	if got := st.RecentTaskID(ctx, "provision", apps, 10*time.Minute); got != 0 {
		t.Fatalf("the Apps page sees task %d, want none", got)
	}

	if got := st.LatestTaskByActions(ctx, "provision", apps, true); got != 0 {
		t.Fatalf("the Apps page sees running task %d, want none", got)
	}
}
