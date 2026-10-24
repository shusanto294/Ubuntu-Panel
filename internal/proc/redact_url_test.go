package proc

import "testing"

func TestRedactStripsURLCredentials(t *testing.T) {
	cases := map[string]string{
		"git clone https://x-access-token:ghp_abc123@github.com/o/r.git /var/www/x": "git clone https://x-access-token:***@github.com/o/r.git /var/www/x",
		"git fetch https://oauth2:glpat-zzz@gitlab.com/o/r.git":                     "git fetch https://oauth2:***@gitlab.com/o/r.git",
		"git clone https://github.com/o/r.git":                                      "git clone https://github.com/o/r.git",
		"git clone git@github.com:o/r.git":                                          "git clone git@github.com:o/r.git",
	}

	for given, wanted := range cases {
		if got := Redact(given); got != wanted {
			t.Errorf("Redact(%q)\n  = %q\nwant %q", given, got, wanted)
		}
	}
}
