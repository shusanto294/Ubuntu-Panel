package store

import (
	"context"
	"testing"
	"time"
)

// The SSH login alert reads (after, until] of one kind: nothing at the cursor
// itself, which the last look already covered, and nothing past the copy.
func TestSSHEventsBetween(t *testing.T) {
	st := taskStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	st.RecordSSHEvents(ctx, []SSHEvent{
		{Kind: "accepted", User: "root", IP: "1.1.1.1", Method: "publickey", CreatedAt: now - 120},
		{Kind: "accepted", User: "root", IP: "2.2.2.2", Method: "publickey", CreatedAt: now - 60},
		{Kind: "failed", User: "admin", IP: "3.3.3.3", Method: "password", CreatedAt: now - 30},
		{Kind: "accepted", User: "deploy", IP: "4.4.4.4", Method: "password", CreatedAt: now - 30},
		{Kind: "accepted", User: "root", IP: "5.5.5.5", Method: "publickey", CreatedAt: now},
	})

	got := st.SSHEventsBetween(ctx, "accepted", now-120, now-1, 50)
	if len(got) != 2 || got[0].IP != "2.2.2.2" || got[1].IP != "4.4.4.4" {
		t.Fatalf("want 2.2.2.2 then 4.4.4.4, got %+v", got)
	}

	if got := st.SSHEventsBetween(ctx, "accepted", now-200, now, 1); len(got) != 1 || got[0].IP != "1.1.1.1" {
		t.Fatalf("limit: want only the oldest, got %+v", got)
	}
}
