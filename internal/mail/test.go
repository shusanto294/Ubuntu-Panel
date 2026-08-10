package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// TestDelivery does what a mail client does, from this machine, and writes down
// every answer.
//
// A webmail says "SMTP Error (): Connection to server failed" and a phone says
// "the server is not responding", and both mean the same nothing. This holds a
// real SMTP conversation on the port the webmail uses, with the credentials of
// a real mailbox, and puts the server's own replies in the log — where a 535
// says the password was refused, a 554 says relaying was, and no answer at all
// says nothing is listening.
func (s *Service) TestDelivery(ctx context.Context, userID int64) error {
	address, password := s.firstMailboxAddress()

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.test", userID, 0,
		"Testing mail delivery")
	if err != nil {
		return err
	}

	if address == "" {
		s.Store.FinishTask(taskID, "failed",
			"There is no mailbox to test with. Add one on the Email page first.", 100)

		return errors.New("no mailbox to test with")
	}

	list := &tasks.List{}

	list.Add("What is listening",
		"sudo ss -lntp | grep -E ':(25|143|465|587|993|995)\\b' || "+
			"echo 'nothing is listening on any mail port'")

	list.AddFn("Talk to the submission port", func(runner *tasks.Runner) (string, error) {
		return s.converse(runner, address, password)
	})

	list.Add("Did it arrive",
		fmt.Sprintf("sleep 2; sudo ls -l %s/Maildir/new 2>/dev/null | tail -n 5 || "+
			"echo 'no Maildir for this mailbox yet'", proc.Quote(maildir(address))))

	list.Add("The mail log",
		"sudo tail -n 40 /var/log/mail.log 2>/dev/null || "+
			"sudo journalctl -u postfix -u dovecot --no-pager -n 40 || true")

	if !tasks.New(s.Store, taskID).Run(list) {
		return errors.New("the delivery test did not finish")
	}

	return nil
}

// converse holds the SMTP conversation the webmail would hold.
//
// The whole exchange is staged into a file and fed to openssl, so the password
// — which travels as base64 in an AUTH PLAIN line — never appears in `ps`.
func (s *Service) converse(runner *tasks.Runner, address, password string) (string, error) {
	// AUTH PLAIN is NUL, username, NUL, password, base64-encoded.
	credentials := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + address + "\x00" + password))

	conversation := "EHLO ubuntu-panel\r\n" +
		"AUTH PLAIN " + credentials + "\r\n" +
		"MAIL FROM:<" + address + ">\r\n" +
		"RCPT TO:<" + address + ">\r\n" +
		"DATA\r\n" +
		"Subject: Ubuntu Panel delivery test\r\n" +
		"From: <" + address + ">\r\n" +
		"To: <" + address + ">\r\n" +
		"\r\n" +
		"If this message is in your inbox, submission and delivery both work.\r\n" +
		".\r\n" +
		"QUIT\r\n"

	staged, err := os.CreateTemp("", "ubuntu-panel-smtp-*")
	if err != nil {
		return "", errors.New("could not stage the conversation")
	}

	name := staged.Name()
	defer os.Remove(name)

	staged.Chmod(0o600)
	staged.WriteString(conversation)
	staged.Close()

	runner.Writef("Sending a message from %s to itself through submission on 587, "+
		"exactly as the webmail does.\n", address)

	// -quiet keeps the certificate dump out of the log; the SMTP replies are
	// the point. -starttls smtp is what makes this the same conversation
	// Roundcube has.
	code := runner.Exec(fmt.Sprintf(
		"timeout 30 openssl s_client -quiet -starttls smtp -connect 127.0.0.1:587 < %s 2>&1 "+
			"| grep -vE '^(depth|verify|notAfter|notBefore|subject|issuer)' || true",
		proc.Quote(name)), time.Minute)

	if code != 0 {
		return "", errors.New("nothing answered on 587. Submission is not running: repair the " +
			"mail server from the Email page, and if that does not fix it the repair log says why")
	}

	return "The replies above are the server's own. 235 is an accepted login, 250 an accepted " +
		"message, 535 a refused password, 554 a refused relay.", nil
}

// firstMailboxAddress is a mailbox to test with, and its password.
func (s *Service) firstMailboxAddress() (string, string) {
	password, address := s.firstMailbox()

	return address, password
}

// maildir is where Dovecot puts a mailbox's mail.
func maildir(address string) string {
	local, domain, found := strings.Cut(address, "@")
	if !found {
		return vmailRoot
	}

	return vmailRoot + "/" + domain + "/" + local
}
