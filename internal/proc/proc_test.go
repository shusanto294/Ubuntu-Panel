package proc

import (
	"strings"
	"testing"
	"time"
)

// Everything user-supplied that reaches a command line goes through Quote, so
// nothing inside it may be able to end the quoting.
func TestQuoteCannotBeEscapedFrom(t *testing.T) {
	cases := map[string]string{
		"example.com":      `'example.com'`,
		"":                 `''`,
		"it's":             `'it'\''s'`,
		"; rm -rf /":       `'; rm -rf /'`,
		"$(whoami)":        `'$(whoami)'`,
		"`id`":             "'`id`'",
		"a' && rm -rf / #": `'a'\'' && rm -rf / #'`,
	}

	for input, want := range cases {
		if got := Quote(input); got != want {
			t.Errorf("Quote(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestQuotedInputStaysOneWord(t *testing.T) {
	result := Run("printf '%s' "+Quote("one; two three"), 10*time.Second)

	if result.Output != "one; two three" {
		t.Fatalf("got %q", result.Output)
	}
}

// The console is a feature and the last failed command ends up on the page
// beside the thing that failed. Neither is a place for a password.
func TestRedact(t *testing.T) {
	cases := []struct{ command, mustNotContain string }{
		{`mysql -e "CREATE USER 'a'@'localhost' IDENTIFIED BY 'hunter2';"`, "hunter2"},
		{`wp core install --admin_password='hunter2' --url=x`, "hunter2"},
		{`sudo mysqldump --password=hunter2 database`, "hunter2"},
		{`echo requirepass hunter2 >> redis.conf`, "hunter2"},
	}

	for _, test := range cases {
		redacted := Redact(test.command)

		if strings.Contains(redacted, test.mustNotContain) {
			t.Errorf("Redact(%q) leaked the secret: %s", test.command, redacted)
		}

		if !strings.Contains(redacted, "***") {
			t.Errorf("Redact(%q) did not mark the redaction: %s", test.command, redacted)
		}
	}
}

func TestRedactLeavesOrdinaryCommandsAlone(t *testing.T) {
	command := "sudo systemctl reload nginx"

	if Redact(command) != command {
		t.Fatalf("Redact rewrote a command with no secret in it: %s", Redact(command))
	}
}

func TestRunCollectsStderrWithStdout(t *testing.T) {
	result := Run("echo out; echo err >&2", 10*time.Second)

	if !strings.Contains(result.Output, "out") || !strings.Contains(result.Output, "err") {
		t.Fatalf("got %q", result.Output)
	}
}

func TestFailingCommandReportsItsCode(t *testing.T) {
	if result := Run("exit 3", 10*time.Second); result.Code != 3 {
		t.Fatalf("got exit code %d", result.Code)
	}
}

// A command that hangs must not hold a queue worker for the rest of the day,
// and killing bash alone would leave the real process behind.
func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	started := time.Now()

	result := Run("sleep 30", time.Second)

	if !result.TimedOut {
		t.Fatal("the command was not reported as timed out")
	}

	if time.Since(started) > 10*time.Second {
		t.Fatalf("the timeout took %s to take effect", time.Since(started))
	}
}

func TestStreamDeliversOutputAsItArrives(t *testing.T) {
	var chunks []string

	result := Stream("echo first; echo second", 10*time.Second, func(chunk []byte) {
		chunks = append(chunks, string(chunk))
	})

	if !result.OK() {
		t.Fatalf("exit code %d", result.Code)
	}

	joined := strings.Join(chunks, "")

	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") {
		t.Fatalf("got %q", joined)
	}
}
