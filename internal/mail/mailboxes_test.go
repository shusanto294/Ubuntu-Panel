package mail

import (
	"strings"
	"testing"
)

// A new mailbox has the folders a mail client expects before anybody uses it.
//
// This is the bug it exists for. A fresh Maildir is an inbox and nothing else,
// and Thunderbird will not send at all without somewhere to file the copy — it
// says "no Sent folder" and stops, so a brand new mailbox receives and cannot
// send. Roundcube hid it by creating the folders itself on first use, which is
// why sending one webmail message appeared to repair the account.
//
// `auto = subscribe` rather than `auto = create`: the folder has to exist *and*
// be in the subscription list, or a client that lists only subscribed folders
// is looking at an empty tree.
func TestANewMailboxHasSomewhereToPutSentMail(t *testing.T) {
	config := dovecotConfig()

	if !strings.Contains(config, "namespace inbox {") {
		t.Fatalf("Dovecot is given no inbox namespace, so it creates no folders:\n%s", config)
	}

	// The IMAP flag is what makes a client file mail correctly whatever the
	// folder is called, rather than matching on the English name.
	for folder, use := range map[string]string{
		"Drafts":  `\Drafts`,
		"Sent":    `\Sent`,
		"Junk":    `\Junk`,
		"Trash":   `\Trash`,
		"Archive": `\Archive`,
	} {
		block := "mailbox " + folder + " {"

		index := strings.Index(config, block)
		if index < 0 {
			t.Errorf("a new mailbox gets no %s folder", folder)
			continue
		}

		rest := config[index:]
		end := strings.Index(rest, "\n  }")

		if end < 0 {
			t.Errorf("the %s block is never closed:\n%s", folder, rest)
			continue
		}

		body := rest[:end]

		if !strings.Contains(body, "special_use = "+use) {
			t.Errorf("%s is not flagged %s, so a client has to guess:\n%s", folder, use, body)
		}

		if !strings.Contains(body, "auto = subscribe") {
			t.Errorf("%s is not created and subscribed on first login:\n%s", folder, body)
		}
	}
}
