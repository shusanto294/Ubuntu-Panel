package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func backupStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return st
}

func newBackup(t *testing.T, st *Store, siteID int64, kind, status string) int64 {
	t.Helper()

	id, err := st.CreateBackup(context.Background(), &Backup{
		SiteID:   siteID,
		Kind:     kind,
		HasFiles: true,
	})
	if err != nil || id <= 0 {
		t.Fatalf("creating a backup: %v", err)
	}

	if status != "pending" {
		st.FinishBackup(id, status, "", 1024)
	}

	return id
}

// Retention deletes the panel's own old copies and nothing else.
//
// This is the one that matters. Somebody who takes a backup before upgrading a
// plugin means to keep it until they say otherwise, and a nightly run filling
// the quota is not them saying otherwise — a rule that counted every backup
// would quietly delete the one thing it was asked to hold on to.
func TestRetentionOnlyEverDeletesTheScheduledBackups(t *testing.T) {
	st := backupStore(t)
	ctx := context.Background()

	manual := newBackup(t, st, 1, "manual", "complete")

	var scheduled []int64
	for range 5 {
		scheduled = append(scheduled, newBackup(t, st, 1, "scheduled", "complete"))
	}

	old, err := st.PrunableBackups(ctx, 1, 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(old) != 2 {
		t.Fatalf("keeping 3 of 5 scheduled backups offered %d to delete, wanted 2", len(old))
	}

	// The two oldest scheduled ones, and never the manual one.
	for _, backup := range old {
		if backup.Kind != "scheduled" {
			t.Errorf("a %s backup was offered for deletion", backup.Kind)
		}

		if backup.ID == manual {
			t.Error("the backup somebody took by hand was offered for deletion")
		}
	}

	if old[0].ID != scheduled[1] || old[1].ID != scheduled[0] {
		t.Errorf("the wrong two were offered: %d and %d", old[0].ID, old[1].ID)
	}

	// Keeping more than there are deletes nothing at all.
	if old, _ := st.PrunableBackups(ctx, 1, 50); len(old) != 0 {
		t.Errorf("keeping 50 of 5 still offered %d to delete", len(old))
	}
}

// A backup that never finished is not one to count against the quota or to
// delete in place of a good one.
func TestRetentionIgnoresTheBackupsThatDidNotFinish(t *testing.T) {
	st := backupStore(t)

	newBackup(t, st, 1, "scheduled", "failed")
	newBackup(t, st, 1, "scheduled", "pending")
	keep := newBackup(t, st, 1, "scheduled", "complete")

	old, err := st.PrunableBackups(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(old) != 0 {
		t.Fatalf("one complete backup kept at 1 still offered %d to delete", len(old))
	}

	if _, err := st.FindBackup(context.Background(), keep); err != nil {
		t.Fatalf("the one complete backup is gone: %v", err)
	}
}

// A site is due when its due time has passed, and a due time in the past is
// still due.
//
// That last part is the whole reason the panel keeps a due time rather than
// running a cron line: a machine switched off overnight takes the backup it
// missed as soon as it is back, instead of skipping a day because nothing was
// listening at three in the morning.
func TestOnlyTheSitesWhoseBackupIsDueAreOffered(t *testing.T) {
	st := backupStore(t)
	ctx := context.Background()

	now := time.Now()

	site := func(domain, schedule string, next int64) int64 {
		id, err := st.CreateSite(ctx, &Site{Domain: domain, Type: "html",
			RootPath: "/var/www/" + domain, Status: "active"})
		if err != nil {
			t.Fatal(err)
		}

		st.SetSiteBackupSchedule(id, schedule, 7, next, "local")

		return id
	}

	overdue := site("overdue.example.com", "daily", now.Add(-48*time.Hour).Unix())
	site("later.example.com", "daily", now.Add(time.Hour).Unix())
	site("off.example.com", "off", 0)
	site("never.example.com", "", 0)

	due, err := st.DueBackups(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(due) != 1 {
		names := []string{}
		for _, s := range due {
			names = append(names, s.Domain)
		}

		t.Fatalf("%d sites were due, wanted 1: %v", len(due), names)
	}

	if due[0].ID != overdue {
		t.Errorf("%s was offered rather than the overdue one", due[0].Domain)
	}

	// And the schedule survives the round trip, because the worker reads it off
	// the row to work out when the next one is.
	if due[0].BackupSchedule != "daily" || due[0].BackupKeep != 7 {
		t.Errorf("the schedule came back as %q keeping %d",
			due[0].BackupSchedule, due[0].BackupKeep)
	}
}

// A remote backup remembers where it went, and a local one says it is local
// even though the column was empty before remote storage existed.
func TestBackupDestination(t *testing.T) {
	st := backupStore(t)
	ctx := context.Background()

	id, err := st.CreateBackup(ctx, &Backup{SiteID: 1, Kind: "manual", HasFiles: true, Destination: "s3"})
	if err != nil {
		t.Fatal(err)
	}

	st.StartBackup(id, "/var/backups/ubuntu-panel/sites/example.com/x-1")
	st.MarkBackupUploaded(id, "servers/one/example.com/x-1")

	backup, err := st.FindBackup(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if !backup.Remote() || backup.RemoteKey != "servers/one/example.com/x-1" || backup.Path != "" {
		t.Fatalf("remote backup not recorded: %+v", backup)
	}

	local, _ := st.FindBackup(ctx, newBackup(t, st, 1, "manual", "complete"))
	if local.Remote() || local.Destination != "local" {
		t.Fatalf("a backup with no destination should be local: %+v", local)
	}
}
