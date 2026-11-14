package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/serverlog"
)

// Each file's stretch comes back as its own, in order, with a partial last
// line left as it was — the cursor only moves over whole lines.
func TestSplitLogBatch(t *testing.T) {
	marker := "@@panel-log-x-3@@"
	output := []byte(marker + " 0\nGET / 200\nGET /a 404\n" +
		marker + " 1\n" +
		marker + " 2\nhalf a li")

	contents := splitLogBatch(output, marker, 3)

	if string(contents[0]) != "GET / 200\nGET /a 404\n" {
		t.Fatalf("first is %q", contents[0])
	}
	if len(contents[1]) != 0 {
		t.Fatalf("an empty read is %q", contents[1])
	}
	if string(contents[2]) != "half a li" {
		t.Fatalf("a partial line is %q", contents[2])
	}

	// A batch cut short reads as empty from where it stopped.
	short := splitLogBatch([]byte(marker+" 0\nline\n"), marker, 2)
	if string(short[0]) != "line\n" || short[1] != nil {
		t.Fatalf("a short batch is %q", short)
	}
}

func TestLogBatchesStayUnderTheLimit(t *testing.T) {
	read := func(size int64) logRead { return logRead{file: serverlog.File{Path: "/x"}, to: size} }

	// One read is at most collectMaxBytes, half a batch: two fill one.
	batches := logBatches([]logRead{read(collectMaxBytes), read(collectMaxBytes * 3), read(collectMaxBytes), read(5)})

	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Fatalf("batches are %d: %+v", len(batches), batches)
	}

	if logBatches(nil) == nil || len(logBatches(nil)) != 0 {
		t.Fatal("no reads is no batches")
	}
}

// A file the panel can open is stat'd and read by the panel itself, from the
// offset asked for; a missing one is left out, not handed to sudo.
func TestLogsReadDirectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	stats := statLogs([]string{path, path + ".1"})
	if len(stats) != 1 || stats[path].size != 19 || stats[path].inode == "" {
		t.Fatalf("stats are %+v", stats)
	}

	contents, ok := readLogDirect(path, 6, 7)
	if !ok || string(contents) != "second\n" {
		t.Fatalf("read %q, %v", contents, ok)
	}

	// Asked for more than there is: what there is.
	if contents, ok := readLogDirect(path, 13, 100); !ok || string(contents) != "third\n" {
		t.Fatalf("read past the end %q, %v", contents, ok)
	}

	if contents, ok := readLogDirect(path+".gone", 0, 10); !ok || len(contents) != 0 {
		t.Fatalf("a missing file is %q, %v", contents, ok)
	}
}

// SSH logins come out of auth.log's lines, and a read that started mid-line
// does not count its first.
func TestSSHEventsFromAuthLog(t *testing.T) {
	text := "word password for root from 192.0.2.1 port 22 ssh2\n" +
		"2026-11-14T10:00:00.000000+00:00 web1 sshd[12]: Accepted publickey for deploy from 192.0.2.7 port 50000 ssh2: ED25519\n" +
		"2026-11-14T10:00:01.000000+00:00 web1 sudo: ubuntupanel : COMMAND=/usr/bin/stat\n" +
		"2026-11-14T10:00:02.000000+00:00 web1 sshd-session[13]: Failed password for root from 192.0.2.8 port 50001 ssh2\n"

	events := sshEventsIn([]byte(text), true)
	if len(events) != 2 {
		t.Fatalf("events are %+v", events)
	}

	kinds := map[string]string{}
	for _, event := range events {
		kinds[event.IP] = event.Kind
	}
	if kinds["192.0.2.7"] != "accepted" || kinds["192.0.2.8"] != "failed" {
		t.Fatalf("events are %+v", events)
	}
}
