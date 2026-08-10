// Package proc runs commands on the machine the panel manages.
//
// This is the whole execution layer, and it is the most dangerous package in
// the codebase: everything it runs, it runs with the panel user's passwordless
// sudo. Two rules hold throughout:
//
//  1. The command string is always a literal in this codebase. User input is
//     never concatenated into one — it goes through Quote, which produces a
//     single-quoted shell word nothing can escape from.
//  2. Files are written by staging to a temporary file and copying it into
//     place, never by echoing contents through a command line. Contents full
//     of quotes, newlines or `$` cannot be mangled, and the bytes never appear
//     in `ps`.
//
// Every command is handed to `bash -c "( ... ) 2>&1"`: stderr folded into
// stdout, and a real shell so heredocs and loops work.
package proc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultTimeout is the ceiling for a single command. An apt install can
// legitimately take a long time; anything past this is hung.
const DefaultTimeout = 30 * time.Minute

// maxOutput is where a command's collected output is truncated from the front.
// An apt install on a slow mirror can produce megabytes, and the console only
// ever shows the tail.
const maxOutput = 4 << 20

// Result is what a finished command left behind.
type Result struct {
	Output   string // combined stdout and stderr, trimmed
	Code     int    // exit status, or -1 if it never ran
	TimedOut bool
}

// OK reports whether the command succeeded.
func (r Result) OK() bool { return r.Code == 0 }

// environment is what every command runs with.
//
// The panel inherits whatever systemd gave it, which can be almost nothing —
// no PATH above all. Every `command -v` then fails and the panel concludes
// that MariaDB, PHP and nginx are not installed on a machine that is plainly
// running all three.
func environment() []string {
	env := os.Environ()

	replace := func(key, value string) {
		prefix := key + "="

		for i, entry := range env {
			if strings.HasPrefix(entry, prefix) {
				env[i] = prefix + value
				return
			}
		}

		env = append(env, prefix+value)
	}

	replace("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	// composer, npm and pm2 all write here and fall over without it.
	if home := os.Getenv("HOME"); home == "" {
		replace("HOME", "/tmp")
	}

	// Nothing the panel runs may open an interactive prompt: there is no
	// terminal attached, so a prompt is a command that hangs until the
	// timeout.
	replace("DEBIAN_FRONTEND", "noninteractive")
	replace("LC_ALL", "C.UTF-8")

	return env
}

// wrap puts the command in a subshell so `2>&1` applies to the whole thing —
// appending it to the last line of a heredoc would corrupt the heredoc.
func wrap(command string) string {
	if strings.TrimSpace(command) == "" {
		command = "true"
	}

	return "(\n" + command + "\n) 2>&1"
}

// Run executes a command and waits for it.
func Run(command string, timeout time.Duration) Result {
	var buffer bytes.Buffer

	result := Stream(command, timeout, func(chunk []byte) {
		if buffer.Len() < maxOutput {
			buffer.Write(chunk)
		}
	})

	result.Output = strings.TrimSpace(buffer.String())

	return result
}

// Stream executes a command, handing each chunk of output to sink as it
// arrives. Used by the task runner so the browser sees a deployment happen
// rather than waiting for it to finish.
func Stream(command string, timeout time.Duration, sink func(chunk []byte)) Result {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.Command("/bin/bash", "-c", wrap(command))
	cmd.Env = environment()
	cmd.Dir = "/"

	// Its own process group, so a timeout kills the whole tree rather than
	// just bash and leaving apt behind holding the dpkg lock.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Code: -1, Output: err.Error()}
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return Result{Code: -1, Output: err.Error()}
	}

	// The reader has to finish before Wait, or the pipe closes under it.
	var pumped sync.WaitGroup
	pumped.Add(1)

	go func() {
		defer pumped.Done()

		buffer := make([]byte, 8192)

		for {
			n, err := pipe.Read(buffer)
			if n > 0 && sink != nil {
				chunk := make([]byte, n)
				copy(chunk, buffer[:n])
				sink(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	timedOut := false

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				timedOut = true
				// Negative pid: the group, not just bash.
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	pumped.Wait()
	waitErr := cmd.Wait()
	close(done)

	code := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
			if code < 0 {
				code = 137 // killed
			}
		} else {
			code = -1
		}
	}

	if timedOut && code == 0 {
		code = 124
	}

	return Result{Code: code, TimedOut: timedOut}
}

// Ok runs a command and reports whether it succeeded.
func Ok(command string) bool {
	return Run(command, 60*time.Second).OK()
}

// Which reports whether a binary is on PATH.
func Which(binary string) bool {
	return Ok("command -v " + Quote(binary) + " >/dev/null 2>&1")
}

// Quote returns a single-quoted shell word. Nothing inside it is interpreted
// by the shell — the only character that needs care is the quote itself, which
// is closed, escaped and reopened.
func Quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// PutFile writes contents to path, creating parent directories, then reads it
// back and compares digests.
//
// The verify step is not paranoia for its own sake: a config file that is
// subtly wrong fails much later and much less clearly — an nginx syntax error
// three steps down a deployment — so it is worth one extra command to catch it
// where it happened.
func PutFile(path, contents string, sudo bool) error {
	prefix := ""
	if sudo {
		prefix = "sudo "
	}

	staged, err := os.CreateTemp("", "ubuntu-panel-*")
	if err != nil {
		return fmt.Errorf("could not stage %s for writing: %w", path, err)
	}

	name := staged.Name()
	defer os.Remove(name)

	// The staged file briefly holds whatever is being written, which for a
	// Laravel .env or a Dovecot SQL config is credentials. Nobody else on the
	// box gets to read it.
	if err := staged.Chmod(0o600); err != nil {
		staged.Close()
		return fmt.Errorf("could not stage %s for writing: %w", path, err)
	}

	if _, err := staged.WriteString(contents); err != nil {
		staged.Close()
		return fmt.Errorf("could not stage %s for writing: %w", path, err)
	}

	if err := staged.Close(); err != nil {
		return fmt.Errorf("could not stage %s for writing: %w", path, err)
	}

	// cp then chmod, rather than mv: mv across filesystems is a copy anyway,
	// and cp keeps the destination's existing inode, so anything holding the
	// file open (nginx reading a vhost during a reload) sees a complete file.
	command := fmt.Sprintf("%smkdir -p %s && %scp %s %s && %schmod 644 %s",
		prefix, Quote(filepath.Dir(path)),
		prefix, Quote(name), Quote(path),
		prefix, Quote(path))

	if result := Run(command, 2*time.Minute); !result.OK() {
		return fmt.Errorf("could not write %s: %s", path, result.Output)
	}

	result := Run(prefix+"sha256sum "+Quote(path), time.Minute)
	if !result.OK() {
		return fmt.Errorf("wrote %s but could not read it back to verify it: %s", path, result.Output)
	}

	digest := sha256.Sum256([]byte(contents))
	written, _, _ := strings.Cut(strings.TrimSpace(result.Output), " ")

	if written != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%s did not survive the write intact", path)
	}

	return nil
}

// ReadFile reads a file, using sudo only if the plain read is refused.
func ReadFile(path string, sudo bool) (string, error) {
	if contents, err := os.ReadFile(path); err == nil {
		return string(contents), nil
	}

	if !sudo {
		return "", fmt.Errorf("could not read %s", path)
	}

	result := Run("sudo cat "+Quote(path), 30*time.Second)
	if !result.OK() {
		return "", fmt.Errorf("could not read %s: %s", path, result.Output)
	}

	return result.Output, nil
}

// Exists reports whether a path exists, even behind a directory this user
// cannot traverse.
func Exists(path string, sudo bool) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}

	if !sudo {
		return false
	}

	return Ok("sudo test -e " + Quote(path))
}

// redactions are the places a secret ends up on a command line.
var redactions = []struct {
	needle string
	quoted bool
}{
	{"IDENTIFIED BY ", true},
	{"--password=", false},
	{"--admin_password=", false},
	{"--dbpass=", false},
	{"PASSWORD=", false},
	{"password=", false},
	{"requirepass ", false},
	{"-p'", true},
}

// Redact removes secrets from a command before it is logged or shown in the
// task console. A backstop, not a licence: a command that needs a secret
// should be taking it somewhere `ps` cannot read it either.
//
// The console is a feature — you can watch a deployment happen — and the last
// failed command ends up on the page beside the thing that failed. Neither is
// a place for a password.
func Redact(command string) string {
	var out strings.Builder

	for i := 0; i < len(command); {
		matched := false

		for _, pattern := range redactions {
			if !strings.HasPrefix(command[i:], pattern.needle) {
				continue
			}

			out.WriteString(pattern.needle)
			out.WriteString("***")
			i += len(pattern.needle)

			// Skip the value. A quoted value ends at its closing quote; an
			// unquoted one ends at whitespace. Getting this wrong in the safe
			// direction — swallowing too much — loses a fragment of a log
			// line. Getting it wrong the other way prints a password.
			if i < len(command) && (command[i] == '\'' || command[i] == '"') {
				quote := command[i]
				i++
				for i < len(command) && command[i] != quote {
					i++
				}
				if i < len(command) {
					i++
				}
			} else {
				for i < len(command) && !strings.ContainsRune(" \t\n;", rune(command[i])) {
					i++
				}
			}

			matched = true
			break
		}

		if !matched {
			out.WriteByte(command[i])
			i++
		}
	}

	return out.String()
}
