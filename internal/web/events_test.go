package web

import "testing"

// The collector moves its cursor past whole lines only, and skips the first
// line of a read that starts mid-file.
func TestCursorWriter(t *testing.T) {
	var lines []string
	w := &cursorWriter{skip: true, line: func(line string) { lines = append(lines, line) }}

	w.Write([]byte("tail of a line\nfirst\nsec"))
	w.Write([]byte("ond\nunfinished"))

	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Fatalf("lines = %q", lines)
	}

	if w.consumed != int64(len("tail of a line\nfirst\nsecond\n")) {
		t.Fatalf("consumed %d", w.consumed)
	}
}
