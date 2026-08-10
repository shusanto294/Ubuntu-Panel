// Package crypto encrypts the credentials the panel stores: DNS API tokens,
// database and mailbox passwords, WordPress admin passwords.
//
// The format is the C panel's, byte for byte — AES-256-GCM, and the stored
// value is base64(nonce ‖ ciphertext ‖ tag) — so a panel.db written by either
// binary can be read by the other as long as PANEL_APP_KEY is the same file it
// always was.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	nonceLen = 12
	tagLen   = 16
	keyLen   = 32
)

// ErrNoKey is what every call returns when no application key was configured.
// It is deliberately not a panic: a panel with no key still serves every page
// that does not touch a credential, and the pages that do say why.
var ErrNoKey = errors.New("no application key: PANEL_APP_KEY is not set in /etc/ubuntu-panel/panel.conf")

// Cipher holds the application key.
type Cipher struct {
	aead cipher.AEAD
}

// New decodes a base64 application key. An empty key gives a Cipher that
// refuses to encrypt or decrypt rather than a nil pointer every caller has to
// check for.
func New(appKeyBase64 string) (*Cipher, error) {
	if appKeyBase64 == "" {
		return &Cipher{}, nil
	}

	key, err := base64.StdEncoding.DecodeString(appKeyBase64)
	if err != nil {
		return &Cipher{}, fmt.Errorf("the application key is not valid base64: %w", err)
	}

	if len(key) != keyLen {
		return &Cipher{}, fmt.Errorf("the application key is %d bytes; it has to be %d", len(key), keyLen)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return &Cipher{}, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return &Cipher{}, err
	}

	return &Cipher{aead: aead}, nil
}

// Ready reports whether a usable key was configured.
func (c *Cipher) Ready() bool { return c != nil && c.aead != nil }

// Encrypt returns the stored form of a secret. An empty string stays empty:
// a column with nothing in it should not become 44 bytes of ciphertext that
// decrypts to nothing.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	if !c.Ready() {
		return "", ErrNoKey
	}

	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Seal appends the tag, so the layout is nonce ‖ ciphertext ‖ tag.
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)

	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}

	if !c.Ready() {
		return "", ErrNoKey
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("stored value is not base64: %w", err)
	}

	if len(raw) < nonceLen+tagLen {
		return "", errors.New("stored value is too short to be encrypted")
	}

	plaintext, err := c.aead.Open(nil, raw[:nonceLen], raw[nonceLen:], nil)
	if err != nil {
		// Nearly always one thing: the application key changed.
		return "", errors.New("could not decrypt with the current application key")
	}

	return string(plaintext), nil
}

// MustDecrypt is for the read paths that would rather show an empty field than
// fail a whole page. The error is dropped on purpose; callers that need to
// report it use Decrypt.
func (c *Cipher) MustDecrypt(encoded string) string {
	value, err := c.Decrypt(encoded)
	if err != nil {
		return ""
	}

	return value
}

// Token returns n random bytes as URL-safe base64, for generated passwords and
// one-shot tickets.
func Token(n int) string {
	buffer := make([]byte, n)

	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		// crypto/rand does not fail on Linux; if it ever did, a predictable
		// password is worse than a panic that systemd restarts.
		panic("crypto/rand: " + err.Error())
	}

	return base64.RawURLEncoding.EncodeToString(buffer)
}

// Password is a generated password made only of characters that survive a
// shell, a MySQL statement and a Dovecot configuration file without quoting
// surprises.
func Password(length int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	buffer := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		panic("crypto/rand: " + err.Error())
	}

	out := make([]byte, length)
	for i, b := range buffer {
		out[i] = alphabet[int(b)%len(alphabet)]
	}

	return string(out)
}
