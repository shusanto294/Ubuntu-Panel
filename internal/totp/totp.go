// Package totp is the time-based one-time password of RFC 6238: the six digits
// an authenticator app shows, changing every thirty seconds.
//
// Written against the standard library rather than pulled in, because the
// whole algorithm is an HMAC over a counter and a few lines of truncation, and
// a dependency for that is a dependency on somebody else's idea of which
// parameters an authenticator app accepts. These are the ones every app does:
// SHA-1, six digits, a thirty-second step.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Step is how long one code is valid for.
	Step = 30 * time.Second

	// Digits is the length of a code.
	Digits = 6

	// skew is how many steps either side of now are accepted. One each way is
	// a minute and a half of clock drift between the phone and the server,
	// which is what the RFC suggests and what every app expects.
	skew = 1
)

// encoding is base32 without padding, which is what an otpauth:// URI carries
// and what people type when they cannot scan the code.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret is twenty random bytes, base32-encoded: the size of a SHA-1 block's
// worth of key, and the length the RFC recommends.
func NewSecret() string {
	buffer := make([]byte, 20)
	rand.Read(buffer)

	return encoding.EncodeToString(buffer)
}

// Code is the code for the step a moment falls in.
func Code(secret string, at time.Time) (string, error) {
	return codeAt(secret, uint64(at.Unix())/uint64(Step.Seconds()))
}

func codeAt(secret string, counter uint64) (string, error) {
	key, err := encoding.DecodeString(normalise(secret))
	if err != nil {
		return "", fmt.Errorf("the secret is not valid base32: %w", err)
	}

	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(message)
	sum := mac.Sum(nil)

	// Dynamic truncation: the low nibble of the last byte says where in the
	// digest the four bytes of the code start.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	return fmt.Sprintf("%06d", value%1_000_000), nil
}

// Verify checks a code typed at a moment, and says which step it matched.
//
// The step comes back so the caller can refuse to accept the same one twice:
// a code read over somebody's shoulder is otherwise good for another minute.
// `after` is the last step already used — nothing at or before it matches.
func Verify(secret, code string, at time.Time, after int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}

	now := int64(at.Unix()) / int64(Step.Seconds())

	for delta := -skew; delta <= skew; delta++ {
		step := now + int64(delta)
		if step <= after || step < 0 {
			continue
		}

		expected, err := codeAt(secret, uint64(step))
		if err != nil {
			return 0, false
		}

		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step, true
		}
	}

	return 0, false
}

// URI is what the QR code encodes: the otpauth:// address an authenticator app
// reads the issuer, the account and the secret from.
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)

	query := url.Values{}
	query.Set("secret", normalise(secret))
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(Digits))
	query.Set("period", fmt.Sprint(int(Step.Seconds())))

	return "otpauth://totp/" + label + "?" + query.Encode()
}

// normalise accepts a secret the way somebody might copy it: lower case, in
// groups of four, with the padding left on.
func normalise(secret string) string {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))

	return strings.TrimRight(secret, "=")
}
