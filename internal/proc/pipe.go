package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Pipe runs a command with stdin and stdout connected to the caller rather
// than collected, for moving bytes that are data, not output: a root-owned
// backup archive read with `sudo cat` and handed to an upload, or a download
// written into place with `sudo dd`. Either of stdin and stdout may be nil.
//
// Not wrapped in `2>&1` like everything else here — stdout is the data, so
// stderr is kept apart and is what the error says when the command fails.
func Pipe(ctx context.Context, command string, timeout time.Duration, stdin io.Reader, stdout io.Writer) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	cmd.Env = environment()
	cmd.Dir = "/"
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("timed out")
		}

		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}

		return fmt.Errorf("%s", message)
	}

	return nil
}
