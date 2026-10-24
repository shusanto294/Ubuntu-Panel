package sites

import (
	"strings"
	"testing"
)

// The URLs in a WordPress database are stored in more than one encoding, and a
// clone that rewrites one of them is a clone that still points at the site it
// came from — in whichever places happened to use the other.
//
// This is the test that matters most in this package. The failure it guards
// against does not look like a failure: the staging site loads, and then
// redirects whoever is editing it back to production.
func TestEveryEncodingOfTheOldURLIsRewritten(t *testing.T) {
	pairs := wordPressReplacements("https://example.com", "https://staging.example.com")

	// What each of these looks like in a real database: an option value, a
	// block editor attribute, a theme's protocol-relative asset.
	wanted := map[string]string{
		"https://example.com":   "https://staging.example.com",
		"http://example.com":    "https://staging.example.com",
		"//example.com":         "//staging.example.com",
		`https:\/\/example.com`: `https:\/\/staging.example.com`,
		`http:\/\/example.com`:  `https:\/\/staging.example.com`,
		`\/\/example.com`:       `\/\/staging.example.com`,
	}

	got := map[string]string{}

	for _, pair := range pairs {
		got[pair[0]] = pair[1]
	}

	for from, to := range wanted {
		actual, ok := got[from]

		if !ok {
			t.Errorf("%q is never searched for, so it would be left pointing at the "+
				"site this was copied from", from)

			continue
		}

		if actual != to {
			t.Errorf("%q is rewritten to %q, wanted %q", from, actual, to)
		}
	}

	if len(pairs) != len(wanted) {
		t.Errorf("%d replacements, wanted %d: %v", len(pairs), len(wanted), pairs)
	}
}

// The scheme-ful forms run first.
//
// Order is not cosmetic here. `//example.com` is a substring of
// `https://example.com`, so replacing the short form first turns every absolute
// URL into `https://staging.example.com` *before* anybody has decided what
// scheme the destination serves — and a staging site with no certificate is
// then full of https:// links that do not answer.
func TestTheSchemeFulFormsAreRewrittenFirst(t *testing.T) {
	pairs := wordPressReplacements("https://example.com", "http://staging.example.com")

	position := func(needle string) int {
		for i, pair := range pairs {
			if pair[0] == needle {
				return i
			}
		}

		return -1
	}

	absolute := position("https://example.com")
	relative := position("//example.com")

	if absolute < 0 || relative < 0 {
		t.Fatalf("the two forms are not both searched for: %v", pairs)
	}

	if absolute > relative {
		t.Errorf("the protocol-relative form is replaced before the absolute one, "+
			"which rewrites every absolute URL without settling its scheme: %v", pairs)
	}
}

// A production site on HTTPS cloned to a staging site with no certificate has
// to come out speaking HTTP, or every asset on the page is blocked as mixed
// content and the browser shows a site with no styling.
func TestADroppedCertificateChangesTheSchemeInTheDatabase(t *testing.T) {
	pairs := wordPressReplacements("https://example.com", "http://staging.example.com")

	for _, pair := range pairs {
		if pair[0] == "https://example.com" {
			if pair[1] != "http://staging.example.com" {
				t.Errorf("https:// was rewritten to %q rather than to the destination's "+
					"own scheme", pair[1])
			}

			return
		}
	}

	t.Error("the https form was not rewritten at all")
}

// A move that is not a move does nothing. Returning the pairs anyway would run
// six full-table replacements of a string onto itself.
func TestAMoveToTheSameAddressRewritesNothing(t *testing.T) {
	if pairs := wordPressReplacements("https://example.com", "https://example.com"); pairs != nil {
		t.Errorf("a move to the same address produced %d replacements", len(pairs))
	}
}

// The hostname staying the same is not the same question as the address staying
// the same.
//
// A staging clone whose certificate fails to issue keeps its hostname and drops
// to HTTP — and the URLs already written into its database say https. Treating
// "same host" as "nothing to do" is what leaves them there, and the site then
// serves pages whose every asset is an https:// address nothing answers.
func TestASchemeChangeOnOneHostnameIsStillRewritten(t *testing.T) {
	pairs := wordPressReplacements("https://staging.example.com", "http://staging.example.com")

	if len(pairs) == 0 {
		t.Fatal("dropping the certificate rewrote nothing, so the database keeps " +
			"https:// URLs on a site serving plain HTTP")
	}

	wanted := map[string]string{
		"https://staging.example.com":   "http://staging.example.com",
		`https:\/\/staging.example.com`: `http:\/\/staging.example.com`,
	}

	got := map[string]string{}

	for _, pair := range pairs {
		got[pair[0]] = pair[1]

		// And nothing that replaces a string with itself: on a scheme-only
		// move three of the six candidates are exactly that, and each one is a
		// full scan of every table to achieve nothing.
		if pair[0] == pair[1] {
			t.Errorf("%q is replaced with itself", pair[0])
		}
	}

	for from, to := range wanted {
		if got[from] != to {
			t.Errorf("%q is rewritten to %q, wanted %q", from, got[from], to)
		}
	}
}

func TestTheHostnameIsReadOutOfEveryFormOfURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com":      "example.com",
		"http://Example.COM":       "example.com",
		"//example.com":            "example.com",
		"https://example.com/":     "example.com",
		"https://example.com/path": "example.com",
		"example.com":              "example.com",
		"":                         "",
	}

	for given, wanted := range cases {
		if got := hostOf(given); got != wanted {
			t.Errorf("hostOf(%q) = %q, wanted %q", given, got, wanted)
		}
	}
}

// ---------------------------------------------------------------- .env

// A .env is hand-edited, commented, and full of API keys the panel knows
// nothing about. Only the keys being changed may change.
func TestRewritingAnEnvFileLeavesEverythingElseAlone(t *testing.T) {
	before := strings.Join([]string{
		"APP_NAME=Example",
		"APP_ENV=production",
		"APP_KEY=base64:abcdef==",
		"APP_URL=https://example.com",
		"",
		"# The mail credentials, which nobody but us has",
		"MAIL_PASSWORD=secret-value",
		"DB_DATABASE=example_prod",
		"DB_USERNAME=example_prod",
		"DB_PASSWORD=oldpassword",
		"STRIPE_KEY=sk_live_xyz",
	}, "\n")

	after := setEnvValues(before, map[string]string{
		"APP_URL":     "https://staging.example.com",
		"DB_DATABASE": "staging_db",
		"DB_USERNAME": "staging_user",
		"DB_PASSWORD": "newpassword",
	})

	for _, wanted := range []string{
		"APP_URL=https://staging.example.com",
		"DB_DATABASE=staging_db",
		"DB_USERNAME=staging_user",
		"DB_PASSWORD=newpassword",
	} {
		if !strings.Contains(after, wanted) {
			t.Errorf("%q is not in the rewritten file:\n%s", wanted, after)
		}
	}

	// Everything the panel was not asked about, including the comment and the
	// blank line that keeps the file readable.
	for _, kept := range []string{
		"APP_NAME=Example",
		"APP_ENV=production",
		"APP_KEY=base64:abcdef==",
		"# The mail credentials, which nobody but us has",
		"MAIL_PASSWORD=secret-value",
		"STRIPE_KEY=sk_live_xyz",
	} {
		if !strings.Contains(after, kept) {
			t.Errorf("%q was lost from the file:\n%s", kept, after)
		}
	}

	// And the old values are gone rather than left as a second line.
	for _, gone := range []string{"APP_URL=https://example.com", "DB_PASSWORD=oldpassword"} {
		if strings.Contains(after, gone) {
			t.Errorf("%q is still in the file:\n%s", gone, after)
		}
	}
}

// A Laravel application with no APP_URL defaults to localhost, which is not the
// answer on either half of a staging pair.
func TestAKeyTheEnvFileDidNotHaveIsAdded(t *testing.T) {
	after := setEnvValues("APP_NAME=Example\n", map[string]string{
		"APP_URL": "https://staging.example.com",
	})

	if !strings.Contains(after, "APP_URL=https://staging.example.com") {
		t.Errorf("APP_URL was not added:\n%s", after)
	}
}

// A commented-out key is a comment, not a key. Rewriting it would leave the
// real value unset and the file looking as though it had been changed.
func TestACommentedKeyIsNotMistakenForTheSetting(t *testing.T) {
	after := setEnvValues("# APP_URL=https://old.example.com\nAPP_URL=https://example.com\n",
		map[string]string{"APP_URL": "https://staging.example.com"})

	if !strings.Contains(after, "# APP_URL=https://old.example.com") {
		t.Errorf("the comment was rewritten:\n%s", after)
	}

	if !strings.Contains(after, "APP_URL=https://staging.example.com") {
		t.Errorf("the real key was not rewritten:\n%s", after)
	}
}

// A generated password contains characters Laravel's own .env parser reads as
// syntax. Unquoted, the application comes up unable to reach its database.
func TestAPasswordWithSyntaxInItIsQuoted(t *testing.T) {
	cases := map[string]string{
		"plainpassword": "plainpassword",
		"has space":     `"has space"`,
		"has#hash":      `"has#hash"`,
		`has"quote`:     `"has\"quote"`,
		"has$dollar":    `"has\$dollar"`,
		`has\backslash`: `"has\\backslash"`,
		"":              `""`,
		"a-b_c.d":       "a-b_c.d",
	}

	for given, wanted := range cases {
		if got := quoteEnvValue(given); got != wanted {
			t.Errorf("quoteEnvValue(%q) = %s, wanted %s", given, got, wanted)
		}
	}
}

// ------------------------------------------------------------- the domain

// The suggestion is a subdomain of the site, because that is the one hostname
// whose DNS the panel can already publish — and `staging.www.example.com` is
// not a name anybody wants.
func TestTheSuggestedStagingDomainIsASubdomainOfTheSite(t *testing.T) {
	cases := map[string]string{
		"example.com":      "staging.example.com",
		"www.example.com":  "staging.example.com",
		"shop.example.com": "staging.shop.example.com",
		"EXAMPLE.COM":      "staging.example.com",
		"":                 "",
	}

	for given, wanted := range cases {
		if got := SuggestStagingDomain(given); got != wanted {
			t.Errorf("SuggestStagingDomain(%q) = %q, wanted %q", given, got, wanted)
		}
	}
}
