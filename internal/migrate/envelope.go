// Package migrate is how one Ubuntu Panel hands a site or a mail domain to
// another: the pairing key, the sealed messages the two exchange, and the
// client the sending panel talks to the receiving one with.
//
// Everything that crosses between the two is sealed with AES-256-GCM under a
// key derived from the migration key the receiving panel issued. The key
// itself never crosses the wire, so the transfer is private and cannot be
// altered even over plain HTTP to a panel reached by its address and port —
// which is how most panels are reached. Each message carries the time it was
// sealed and a random id; the receiver refuses one that is old, or that it has
// already seen, so a recorded request cannot be replayed. The path it was sent
// to is bound into the seal, so one cannot be redirected to another endpoint.
package migrate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// KeyPrefix starts every migration key, so one pasted into the wrong box is
// recognised as what it is.
const KeyPrefix = "upm1"

// Key is a migration key: an id, which is sent in the clear to say which key
// a message is sealed with, and a secret, which never leaves either panel.
type Key struct {
	ID     string
	Secret []byte
}

// NewKey makes a key with a fresh 256-bit secret.
func NewKey() (Key, error) {
	id := make([]byte, 8)
	secret := make([]byte, 32)

	if _, err := rand.Read(id); err != nil {
		return Key{}, err
	}
	if _, err := rand.Read(secret); err != nil {
		return Key{}, err
	}

	return Key{ID: hex.EncodeToString(id), Secret: secret}, nil
}

// String is the key as it is shown and pasted: upm1.<id>.<secret>.
func (k Key) String() string {
	return KeyPrefix + "." + k.ID + "." + base64.RawURLEncoding.EncodeToString(k.Secret)
}

// ParseKey reads a pasted key.
func ParseKey(text string) (Key, error) {
	parts := strings.Split(strings.TrimSpace(text), ".")
	if len(parts) != 3 || parts[0] != KeyPrefix {
		return Key{}, errors.New("that is not a migration key — copy it from Migrate → Receive on the other panel")
	}

	if _, err := hex.DecodeString(parts[1]); err != nil || len(parts[1]) != 16 {
		return Key{}, errors.New("that migration key is damaged; copy it again")
	}

	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return Key{}, errors.New("that migration key is damaged; copy it again")
	}

	return Key{ID: parts[1], Secret: secret}, nil
}

// aead is the cipher both directions are sealed with.
func (k Key) aead() (cipher.AEAD, error) {
	derived, err := hkdf.Key(sha256.New, k.Secret, []byte(k.ID), "ubuntu-panel migration v1", 32)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}

	return cipher.NewGCM(block)
}

// headerSize is the time (8 bytes) and the message id (16) at the front of
// every sealed payload.
const headerSize = 8 + 16

// MaxSkew is how far a message's time may be from the receiver's clock. The
// two clocks are a VPS's each, kept by NTP; ten minutes is generous.
const MaxSkew = 10 * time.Minute

// Seal encrypts payload for the given purpose (the path it is sent to, and
// which direction). The output is nonce || ciphertext.
func (k Key) Seal(purpose string, payload []byte) ([]byte, error) {
	aead, err := k.aead()
	if err != nil {
		return nil, err
	}

	plain := make([]byte, headerSize+len(payload))
	binary.BigEndian.PutUint64(plain, uint64(time.Now().Unix()))
	if _, err := rand.Read(plain[8:headerSize]); err != nil {
		return nil, err
	}
	copy(plain[headerSize:], payload)

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	return aead.Seal(nonce, nonce, plain, []byte(k.ID+"\x00"+purpose)), nil
}

// Open checks and decrypts a sealed message. It refuses one sealed for
// another purpose, with another key, altered on the way, or too old.
// seen, when given, refuses one already opened.
func (k Key) Open(purpose string, sealed []byte, seen *Seen) ([]byte, error) {
	aead, err := k.aead()
	if err != nil {
		return nil, err
	}

	if len(sealed) < aead.NonceSize()+aead.Overhead()+headerSize {
		return nil, errors.New("the message is too short to be one")
	}

	nonce, body := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]

	plain, err := aead.Open(nil, nonce, body, []byte(k.ID+"\x00"+purpose))
	if err != nil {
		return nil, errors.New("the message was not sealed with this migration key, or was altered on the way")
	}

	at := time.Unix(int64(binary.BigEndian.Uint64(plain)), 0)
	if skew := time.Since(at); skew > MaxSkew || skew < -MaxSkew {
		return nil, fmt.Errorf("the message is %s away from this server's clock; check both servers' time",
			skew.Round(time.Second))
	}

	if seen != nil && !seen.first(string(plain[8:headerSize])) {
		return nil, errors.New("that message has been received before")
	}

	return plain[headerSize:], nil
}

// Seen remembers the ids of the messages opened in the last MaxSkew*2, so the
// same one is never acted on twice.
type Seen struct {
	sync.Mutex
	ids map[string]time.Time
}

func (s *Seen) first(id string) bool {
	s.Lock()
	defer s.Unlock()

	if s.ids == nil {
		s.ids = map[string]time.Time{}
	}

	now := time.Now()
	for old, at := range s.ids {
		if now.Sub(at) > 2*MaxSkew {
			delete(s.ids, old)
		}
	}

	if _, ok := s.ids[id]; ok {
		return false
	}

	s.ids[id] = now

	return true
}

// SameKey compares two key ids in constant time.
func SameKey(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NewTransferID names a transfer on the receiving panel: 32 hex characters,
// which is also all a request may name one with.
func NewTransferID() string {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(id)
}

// ValidTransferID is whether text could be one.
func ValidTransferID(text string) bool {
	if len(text) != 32 {
		return false
	}
	_, err := hex.DecodeString(text)
	return err == nil
}
