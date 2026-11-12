package web

import (
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
