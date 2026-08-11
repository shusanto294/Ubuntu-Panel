package databases

import (
	"strings"
	"testing"
)

// mongosh's db.auth returns `{ok: 1}`, the shell it replaced returned `1`, and
// a check that only understands one of them calls a correct password wrong —
// which is what made every MongoDB database creation fail with "the panel's
// password is not accepted" on servers where it was.
func TestAuthScriptAcceptsBothShellsIdeaOfSuccess(t *testing.T) {
	script := mongoAuthScript(MongoAdmin, "secret")

	for _, accepted := range []string{"result === 1", "result === true", "result.ok === 1"} {
		if !strings.Contains(script, accepted) {
			t.Errorf("the login script does not accept %s as success", accepted)
		}
	}

	// The comparison that started it: an object is never loosely equal to 1.
	if strings.Contains(script, "!= 1") {
		t.Error("the login script compares mongosh's result object to a number")
	}

	if !strings.Contains(script, `"secret"`) {
		t.Error("the login script does not carry the password")
	}
}

// A password is the only value that reaches a MongoDB script without having
// been through SafeIdentifier, so it is the only place a quote could close the
// literal early and turn the rest of the password into JavaScript.
func TestJsQuoteClosesNothingItDidNotOpen(t *testing.T) {
	cases := map[string]string{
		"plain":                   `"plain"`,
		`say "hi"`:                `"say \"hi\""`,
		`back\slash`:              `"back\\slash"`,
		"two\nlines":              `"two\nlines"`,
		`");db.dropDatabase();//`: `"\");db.dropDatabase();//"`,
	}

	for input, want := range cases {
		if got := jsQuote(input); got != want {
			t.Errorf("jsQuote(%q) = %s, want %s", input, got, want)
		}
	}
}
