//go:build linux

package notfound

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "404.log")
	if err := os.WriteFile(path, []byte("old line, already there\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, path, func(l string) { lines <- l }, func() error {
			return os.WriteFile(path, nil, 0o644)
		})
	}()

	time.Sleep(200 * time.Millisecond)

	appendLine := func(text string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}

	expect := func(want string) {
		t.Helper()
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no line, wanted %q", want)
		}
	}

	appendLine("first\n")
	expect("first")

	// A line written in two pieces is one line.
	appendLine("sec")
	appendLine("ond\n")
	expect("second")

	// Emptied from outside: reading starts again from the top.
	os.Truncate(path, 0)
	time.Sleep(100 * time.Millisecond)
	appendLine("after truncate\n")
	expect("after truncate")

	// Deleted: recreated and followed again.
	os.Remove(path)
	time.Sleep(300 * time.Millisecond)
	appendLine("after recreate\n")
	expect("after recreate")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Follow ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Follow did not stop when asked")
	}
}
