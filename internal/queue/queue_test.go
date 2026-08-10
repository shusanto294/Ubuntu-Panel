package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return st
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAQueuedJobReachesItsHandlerAndIsRemoved(t *testing.T) {
	st := newStore(t)
	q := New(st, quiet(), 1)

	done := make(chan int64, 1)

	q.Handle(SiteDeploy, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Site int64 `json:"site"`
		}

		if err := job.Decode(&payload); err != nil {
			return err
		}

		done <- payload.Site

		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)
	defer q.Stop()

	if _, err := q.PushID(SiteDeploy, "site", 42); err != nil {
		t.Fatal(err)
	}

	select {
	case site := <-done:
		if site != 42 {
			t.Fatalf("the handler was given site %d", site)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the job never reached its handler")
	}

	// The row goes when the work is done: keeping finished jobs would make the
	// table the log, and the log already exists.
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := st.ClaimJob(ctx); errors.Is(err, store.ErrNotFound) {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("the finished job is still on the queue")
}

// Only one UPDATE can match a queued row, which is what stops two workers
// running the same deployment.
func TestAJobIsClaimedExactlyOnce(t *testing.T) {
	st := newStore(t)
	q := New(st, quiet(), 4)

	var mutex sync.Mutex
	runs := 0

	q.Handle(SiteDeploy, func(ctx context.Context, job *store.Job) error {
		mutex.Lock()
		runs++
		mutex.Unlock()

		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	q.PushID(SiteDeploy, "site", 1)

	time.Sleep(3 * time.Second)
	q.Stop()

	mutex.Lock()
	defer mutex.Unlock()

	if runs != 1 {
		t.Fatalf("the job ran %d times", runs)
	}
}

// A job for a kind nothing handles must not be silently swallowed: it is a
// programming error, and the row records it.
func TestPushRefusesAKindWithNoHandler(t *testing.T) {
	q := New(newStore(t), quiet(), 1)

	if _, err := q.Push("nothing.handles.this", nil); err == nil {
		t.Fatal("Push accepted a job nothing can run")
	}
}

// Every handler is written to be safe to run twice, which is what makes a
// deployment survive a restart: the row goes back on the queue.
func TestJobsRunningAtStartupAreRequeued(t *testing.T) {
	st := newStore(t)

	id, err := st.PushJob(SiteDeploy, map[string]any{"site": 7})
	if err != nil {
		t.Fatal(err)
	}

	// Claim it and then walk away, which is what a panel restart mid-job looks
	// like from the table's side.
	if _, err := st.ClaimJob(context.Background()); err != nil {
		t.Fatal(err)
	}

	if requeued := st.RequeueRunningJobs(); requeued != 1 {
		t.Fatalf("%d jobs were requeued", requeued)
	}

	job, err := st.ClaimJob(context.Background())
	if err != nil {
		t.Fatalf("the requeued job could not be claimed: %v", err)
	}

	if job.ID != id {
		t.Fatalf("claimed job %d, want %d", job.ID, id)
	}

	if job.Attempts != 2 {
		t.Fatalf("attempts is %d, want 2", job.Attempts)
	}
}
