package migrate

import (
	"bytes"
	"testing"
)

func TestKeysRoundTrip(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseKey("  " + key.String() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID != key.ID || !bytes.Equal(parsed.Secret, key.Secret) {
		t.Fatal("a key does not survive being written out and read back")
	}

	for _, bad := range []string{"", "upm1", "upm2.0011223344556677.AAAA", key.String() + "x", "upm1.zz." + key.String()[22:]} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("%q was accepted as a key", bad)
		}
	}
}

func TestSealedMessages(t *testing.T) {
	key, _ := NewKey()
	other, _ := NewKey()
	seen := &Seen{}

	sealed, err := key.Seal("request /migrate/api/hello", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(sealed, []byte(`"a"`)) {
		t.Error("the payload is readable in the sealed message")
	}

	got, err := key.Open("request /migrate/api/hello", sealed, seen)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("open = %q, %v", got, err)
	}

	if _, err := key.Open("request /migrate/api/hello", sealed, seen); err == nil {
		t.Error("the same message was accepted twice")
	}

	if _, err := key.Open("request /migrate/api/begin", sealed, nil); err == nil {
		t.Error("a message was accepted at a path it was not sealed for")
	}

	if _, err := other.Open("request /migrate/api/hello", sealed, nil); err == nil {
		t.Error("a message was opened with another key")
	}

	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := key.Open("request /migrate/api/hello", tampered, nil); err == nil {
		t.Error("an altered message was accepted")
	}
}
