// Package notify sends the panel's own email: security alerts and the test
// message that proves the SMTP settings work.
//
// Through an SMTP server somebody configured rather than the mail server this
// machine may be running. The panel's alerts are about this machine — a ban,
// a sign-in, a lockout — and an alert that has to be delivered by the machine
// it is warning about is not one to rely on. A relay elsewhere (a transactional
// provider, a mailbox at a host) also has the reputation a fresh server does
// not, so the message lands in an inbox rather than a spam folder.
package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Encryption is how the connection to the relay is protected.
const (
	// EncryptionTLS is implicit TLS from the first byte, usually port 465.
	EncryptionTLS = "tls"

	// EncryptionSTARTTLS is a plain connection upgraded before anything is
	// said, usually port 587. Refused if the server does not offer it: a relay
	// that silently stays in the clear would send the password that way.
	EncryptionSTARTTLS = "starttls"

	// EncryptionNone is for a relay on this machine or a private network.
	EncryptionNone = "none"
)

// timeout is the whole conversation. An alert is not worth holding a request,
// or a worker, for longer than this.
const timeout = 20 * time.Second

// ErrNotConfigured is what Send answers when nobody has entered a relay.
var ErrNotConfigured = errors.New("SMTP is not configured")

// Config is a relay and the identity to send as.
type Config struct {
	Host       string
	Port       int
	Username   string
	Password   string
	Encryption string
	FromEmail  string
	FromName   string
}

// Ready says whether there is enough here to try.
func (c Config) Ready() bool {
	return c.Host != "" && c.Port > 0 && c.FromEmail != ""
}

// Validate is what the settings form checks before saving.
func (c Config) Validate() map[string]string {
	problems := map[string]string{}

	if c.Host == "" || strings.ContainsAny(c.Host, " /\r\n") {
		problems["smtp_host"] = "Enter the relay's hostname, such as smtp.example.com."
	}
	if c.Port < 1 || c.Port > 65535 {
		problems["smtp_port"] = "Enter a port between 1 and 65535 — usually 587 or 465."
	}

	switch c.Encryption {
	case EncryptionTLS, EncryptionSTARTTLS, EncryptionNone:
	default:
		problems["smtp_encryption"] = "Choose TLS, STARTTLS or none."
	}

	if _, err := mail.ParseAddress(c.FromEmail); err != nil || strings.ContainsAny(c.FromEmail, "\r\n") {
		problems["smtp_from_email"] = "Enter the address messages are sent from."
	}
	if strings.ContainsAny(c.FromName, "\r\n") {
		problems["smtp_from_name"] = "The name has to be on one line."
	}
	if strings.ContainsAny(c.Username, "\r\n") {
		problems["smtp_username"] = "The username has to be on one line."
	}

	return problems
}

// Message is one email.
type Message struct {
	To      []string
	Subject string
	Body    string // plain text
}

// Send delivers a message through the relay.
func Send(ctx context.Context, config Config, message Message) error {
	if !config.Ready() {
		return ErrNotConfigured
	}

	if len(message.To) == 0 {
		return errors.New("there is nobody to send it to")
	}

	for _, recipient := range message.To {
		if _, err := mail.ParseAddress(recipient); err != nil || strings.ContainsAny(recipient, "\r\n") {
			return fmt.Errorf("%q is not an email address", recipient)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dialer := &net.Dialer{Timeout: timeout}

	var (
		conn net.Conn
		err  error
	)

	tlsConfig := &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12}

	if config.Encryption == EncryptionTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return fmt.Errorf("could not reach %s: %w", address, err)
	}

	// The deadline covers every read and write that follows, which net/smtp
	// has no timeout of its own for. A relay that accepts the connection and
	// then says nothing would otherwise hold this forever.
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, config.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%s did not greet us as an SMTP server: %w", address, err)
	}
	defer client.Close()

	if err := client.Hello(heloName()); err != nil {
		return fmt.Errorf("the relay refused our greeting: %w", err)
	}

	if config.Encryption == EncryptionSTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the relay does not offer STARTTLS. Choose TLS on port 465, or " +
				"none only if it is on this machine or a private network")
		}

		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("could not start TLS: %w", err)
		}
	}

	if config.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("the relay does not accept a login on this connection")
		}

		// net/smtp's PlainAuth refuses to send the password over a connection
		// that is not encrypted unless the server is localhost, which is the
		// behaviour wanted here as well.
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
			return fmt.Errorf("the relay refused the username or password: %w", err)
		}
	}

	if err := client.Mail(config.FromEmail); err != nil {
		return fmt.Errorf("the relay refused the sender address: %w", err)
	}

	for _, recipient := range message.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("the relay refused %s: %w", recipient, err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("the relay would not take the message: %w", err)
	}

	if _, err := writer.Write([]byte(compose(config, message))); err != nil {
		writer.Close()
		return fmt.Errorf("could not send the message: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("the relay refused the message: %w", err)
	}

	return client.Quit()
}

// compose is the message on the wire: headers, a blank line, the body with
// bare newlines turned into the CRLFs SMTP requires.
func compose(config Config, message Message) string {
	from := (&mail.Address{Name: config.FromName, Address: config.FromEmail}).String()

	headers := []string{
		"From: " + from,
		"To: " + strings.Join(message.To, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", message.Subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: " + messageID(config.FromEmail),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"Auto-Submitted: auto-generated",
	}

	body := strings.ReplaceAll(message.Body, "\r\n", "\n")

	// A line that is a lone dot ends the DATA section early; the writer
	// net/smtp hands back dot-stuffs, but only lines it sees as lines.
	body = strings.ReplaceAll(body, "\n", "\r\n")

	return strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n"
}

func messageID(from string) string {
	domain := "localhost"
	if _, after, found := strings.Cut(from, "@"); found && after != "" {
		domain = after
	}

	return fmt.Sprintf("<%d.ubuntu-panel@%s>", time.Now().UnixNano(), domain)
}

// heloName is what this machine says it is. A bare "localhost" is what some
// relays refuse outright.
func heloName() string {
	if name, err := os.Hostname(); err == nil && name != "" && name != "localhost" {
		return name
	}

	return "ubuntu-panel.localdomain"
}
