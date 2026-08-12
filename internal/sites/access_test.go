package sites

import "testing"

// useradd applies this rule and reports a refusal as an exit code. Checking it
// here is what turns that into a sentence somebody can act on — and it is also
// the guard that keeps a name from reaching a command line as anything but a
// plain word.
func TestAccountNamesAreOrdinaryWords(t *testing.T) {
	for _, name := range []string{"deploy", "site_user", "app-1", "abc"} {
		if !validAccountName(name) {
			t.Errorf("%q was refused", name)
		}
	}

	for _, name := range []string{
		"",
		"ab",
		"1deploy",
		"-deploy",
		"_deploy",
		"Deploy",
		"deploy user",
		"deploy;rm -rf /",
		"deploy$(id)",
		"deploy\nroot",
		"../root",
		"averyveryverylongaccountnamethatnobodywouldevertype",
	} {
		if validAccountName(name) {
			t.Errorf("%q was accepted", name)
		}
	}
}
