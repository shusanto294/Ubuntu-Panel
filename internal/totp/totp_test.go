package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// The RFC 6238 appendix B vectors for SHA-1, whose key is the ASCII string
// "12345678901234567890". The RFC prints eight digits; the last six are what
// a six-digit code is.
func TestRFCVectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("12345678901234567890"))

	vectors := map[int64]string{
		59:          "94287082",
		1111111109:  "07081804",
		1111111111:  "14050471",
		1234567890:  "89005924",
		2000000000:  "69279037",
		20000000000: "65353130",
	}

	for unix, want := range vectors {
		got, err := Code(secret, time.Unix(unix, 0))
		if err != nil {
			t.Fatal(err)
		}

		if got != want[2:] {
			t.Errorf("at %d: got %s, want %s", unix, got, want[2:])
		}
	}
}

func TestVerifyAcceptsNeighbouringStepsAndRefusesReplay(t *testing.T) {
	secret := NewSecret()
	now := time.Unix(1_700_000_000, 0)

	previous, _ := Code(secret, now.Add(-Step))

	step, ok := Verify(secret, previous, now, 0)
	if !ok {
		t.Fatal("a code from the previous step should be accepted")
	}

	if _, ok := Verify(secret, previous, now, step); ok {
		t.Fatal("a code whose step was already used should be refused")
	}

	stale, _ := Code(secret, now.Add(-3*Step))
	if _, ok := Verify(secret, stale, now, 0); ok {
		t.Fatal("a code from ninety seconds ago should be refused")
	}

	current, _ := Code(secret, now)
	spaced := current[:3] + " " + current[3:]
	if _, ok := Verify(strings.ToLower(secret), spaced, now, 0); !ok {
		t.Fatal("a code typed with a space against a lower-cased secret should be accepted")
	}
}

func TestURI(t *testing.T) {
	uri := URI("Ubuntu Panel", "me@example.com", "ABCDEF")

	if !strings.HasPrefix(uri, "otpauth://totp/Ubuntu%20Panel:me@example.com?") {
		t.Fatalf("unexpected label: %s", uri)
	}

	if !strings.Contains(uri, "secret=ABCDEF") || !strings.Contains(uri, "issuer=Ubuntu+Panel") {
		t.Fatalf("missing parameters: %s", uri)
	}
}
