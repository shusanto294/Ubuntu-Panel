package databases

import "testing"

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
