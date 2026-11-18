//go:build linux

package notfound

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// maxSize is when the panel empties the log: everything in it has been read,
// and nginx appends, so the next line lands at the start.
const maxSize = 4 << 20

// Follow calls line for every line appended to path until ctx ends. It sleeps
// in the kernel between writes: inotify wakes it when the file grows, and
// nothing else does. recreate is called when the file has been moved or
// deleted out from under it, to put it back and have nginx write to it again.
func Follow(ctx context.Context, path string, line func(string), recreate func() error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { file.Close() }()

	offset, _ := file.Seek(0, io.SeekEnd)

	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return err
	}

	// Through os.File so that reads park in Go's poller rather than a thread,
	// and closing it ends a read in progress.
	events := os.NewFile(uintptr(fd), "inotify")
	defer events.Close()

	current := -1

	watch := func() error {
		wd, err := syscall.InotifyAddWatch(fd, path,
			syscall.IN_MODIFY|syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF|syscall.IN_ATTRIB)
		if err == nil {
			current = wd
		}
		return err
	}

	// replaced is whether the name now points somewhere other than the file
	// held open. A delete only shows as a change of link count while the file
	// is open — which this always holds it — so it is checked rather than
	// waited for.
	replaced := func() bool {
		held, err := file.Stat()
		if err != nil {
			return true
		}
		named, err := os.Stat(path)
		return err != nil || !os.SameFile(held, named)
	}

	if err := watch(); err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		events.Close()
	}()

	var pending []byte
	buffer := make([]byte, 64<<10)
	eventBuf := make([]byte, 4096)

	drain := func() {
		info, err := file.Stat()
		if err != nil {
			return
		}

		// Emptied by somebody else: start again from the top.
		if info.Size() < offset {
			offset, pending = 0, nil
		}

		for {
			n, err := file.ReadAt(buffer, offset)
			if n > 0 {
				offset += int64(n)
				pending = append(pending, buffer[:n]...)

				for {
					i := bytes.IndexByte(pending, '\n')
					if i < 0 {
						break
					}
					line(string(pending[:i]))
					pending = pending[i+1:]
				}
			}

			if err != nil || n == 0 {
				break
			}
		}

		if offset >= maxSize && len(pending) == 0 {
			if os.Truncate(path, 0) == nil {
				offset = 0
			}
		}
	}

	for {
		n, err := events.Read(eventBuf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		reopen := false

		for i := 0; i+syscall.SizeofInotifyEvent <= n; {
			event := (*syscall.InotifyEvent)(unsafePointer(&eventBuf[i]))
			i += syscall.SizeofInotifyEvent + int(event.Len)

			// Events for a file already let go of are history.
			if int(event.Wd) != current {
				continue
			}

			if event.Mask&(syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF|syscall.IN_IGNORED) != 0 {
				reopen = true
			}
			if event.Mask&syscall.IN_ATTRIB != 0 && replaced() {
				reopen = true
			}
		}

		drain()

		if !reopen {
			continue
		}

		// Moved or deleted under it: put it back, and wait for that to work.
		syscall.InotifyRmWatch(fd, uint32(current))
		current = -1
		file.Close()

		for {
			if ctx.Err() != nil {
				return nil
			}

			if _, statErr := os.Stat(path); statErr != nil {
				recreate()
			}

			if file, err = os.Open(path); err == nil {
				break
			}

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}

		offset, pending = 0, nil

		if err := watch(); err != nil {
			return errors.Join(errors.New("could not watch the 404 log again"), err)
		}
	}
}
