package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

const key = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes, base64

func TestRoundTrip(t *testing.T) {
	cipher, err := New(key)
	if err != nil {
		t.Fatal(err)
	}

	secret := "a token with 'quotes', a newline\n and unicode — é"

	encrypted, err := cipher.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(encrypted, secret) {
		t.Fatal("the stored form contains the plaintext")
	}

	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}

	if decrypted != secret {
		t.Fatalf("got %q, want %q", decrypted, secret)
	}
}

// Two encryptions of the same value must differ, or a stored column tells an
// attacker which credentials are shared.
func TestNonceIsFresh(t *testing.T) {
	cipher, _ := New(key)

	first, _ := cipher.Encrypt("same")
	second, _ := cipher.Encrypt("same")

	if first == second {
		t.Fatal("the same plaintext encrypted to the same ciphertext twice")
	}
}

// An empty column stays empty: 44 bytes of ciphertext that decrypt to nothing
// would make "no token stored" indistinguishable from "a token stored".
func TestEmptyStaysEmpty(t *testing.T) {
	cipher, _ := New(key)

	encrypted, err := cipher.Encrypt("")
	if err != nil || encrypted != "" {
		t.Fatalf("got %q, %v", encrypted, err)
	}
}

// The stored form is the C panel's: base64(nonce ‖ ciphertext ‖ tag), so a
// panel.db written by either binary can be read by the other.
func TestStoredFormLayout(t *testing.T) {
	cipher, _ := New(key)

	encrypted, _ := cipher.Encrypt("x")

	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		t.Fatalf("the stored form is not base64: %v", err)
	}

	if len(raw) != nonceLen+1+tagLen {
		t.Fatalf("the stored form is %d bytes, want %d", len(raw), nonceLen+1+tagLen)
	}
}

func TestWrongKeyIsReportedRatherThanReturningRubbish(t *testing.T) {
	cipher, _ := New(key)
	other, _ := New("ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")

	encrypted, _ := cipher.Encrypt("secret")

	if _, err := other.Decrypt(encrypted); err == nil {
		t.Fatal("decrypting with the wrong key succeeded")
	}
}

// A panel with no key serves every page that does not touch a credential, and
// says why on the ones that do — rather than panicking on a nil cipher.
func TestNoKeyIsAnErrorNotAPanic(t *testing.T) {
	cipher, err := New("")
	if err != nil {
		t.Fatal(err)
	}

	if cipher.Ready() {
		t.Fatal("an empty key produced a usable cipher")
	}

	if _, err := cipher.Encrypt("x"); err != ErrNoKey {
		t.Fatalf("got %v, want ErrNoKey", err)
	}
}

func TestKeyOfTheWrongLengthIsRefused(t *testing.T) {
	if _, err := New(base64.StdEncoding.EncodeToString([]byte("too short"))); err == nil {
		t.Fatal("a short key was accepted")
	}
}

// Generated passwords go into shell commands, SQL statements and Dovecot
// configuration files, so they may not contain anything those quote.
func TestGeneratedPasswordsAreShellSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		password := Password(24)

		if len(password) != 24 {
			t.Fatalf("password is %d characters", len(password))
		}

		if strings.ContainsAny(password, `'"\$`+"`\n ") {
			t.Fatalf("password %q contains a character that needs quoting", password)
		}
	}
}
