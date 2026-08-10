package mail

import "testing"

// `doveadm pw` asks for the password twice and, given it once, prints
//
//	Error: Password mismatch
//
// on standard output and exits zero. That went into the password column as
// though it were a hash, and every mailbox was refused at login with the right
// password while the table looked plausible.
func TestOnlyARealHashIsAccepted(t *testing.T) {
	valid := "{SHA512-CRYPT}$6$B2Ee7YvJmR4Vd1kQ$0M4c1Iu9Jj0kZ5vN0jvPxYqzHn0gI0h2Q7t8kZ" +
		"3wS9pJ4wU9rC1kK6y0nQ2sT8vX5b7lD3mF1gH0jK9nP2qR"

	if !isPasswordHash(valid) {
		t.Fatal("a SHA512-CRYPT hash was rejected")
	}

	rubbish := []string{
		"Error: Password mismatch",
		"Error: Passwor",
		"",
		"$6$abc$def",                    // the scheme is missing
		"{PLAIN}hunter2",                // not the scheme that was asked for
		"{SHA512-CRYPT}Error: mismatch", // right prefix, wrong everything else
		"{SHA512-CRYPT}$6$with space$x",
	}

	for _, value := range rubbish {
		if isPasswordHash(value) {
			t.Errorf("%q was accepted as a password hash", value)
		}
	}
}
