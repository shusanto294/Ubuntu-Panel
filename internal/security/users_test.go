package security

import "testing"

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMWalKt6KFGX3W0487UdSFWRjlY7xOG/VjeUJxmaLtGP me@laptop"

func TestParseKeyMatchesSSHKeygen(t *testing.T) {
	key, ok := ParseKey("no-pty,from=\"10.0.0.0/8\" " + testKey)
	if !ok {
		t.Fatal("the key was not parsed")
	}

	if key.Fingerprint != "SHA256:8lkFv7ld+DGC5b1aAxhAd8zLCdMwezn0lwBDAnGfrg0" || key.Type != "ssh-ed25519" || key.Comment != "me@laptop" {
		t.Fatalf("parsed: %+v", key)
	}

	if !ValidPublicKey(testKey) {
		t.Fatal("a plain key should be valid")
	}

	for _, bad := range []string{"", "ssh-ed25519", "ssh-ed25519 notbase64!!", testKey + "\nssh-rsa AAAA", "command=\"x\" " + testKey} {
		if ValidPublicKey(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestParseUsers(t *testing.T) {
	output := "@@user root 0 /root /bin/bash\n" +
		"@@groups root\n" +
		"@@status P never\n" +
		"@@user ubuntu-panel 998 /home/ubuntu-panel /bin/bash\n" +
		"@@groups ubuntu-panel\n" +
		"@@status NP never\n" +
		"@@user deploy 1001 /home/deploy /bin/bash\n" +
		"@@groups deploy sudo\n" +
		"@@status P Jan_01,_1970\n" +
		"@@key " + testKey + "\n" +
		"@@key garbage line\n"

	users := parseUsers(output, "ubuntu-panel")

	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %+v", users)
	}

	if !users[0].Protected || !users[0].Sudo {
		t.Fatalf("root: %+v", users[0])
	}

	if !users[1].Protected {
		t.Fatalf("the panel's own account should be protected: %+v", users[1])
	}

	deploy := users[2]
	if !deploy.Sudo || !deploy.Locked || !deploy.Expired || deploy.PasswordLocked ||
		!deploy.Password || len(deploy.Keys) != 1 {
		t.Fatalf("deploy: %+v", deploy)
	}
}

func TestNewUserValidate(t *testing.T) {
	if problems := (NewUser{Name: "deploy", PublicKey: testKey}).Validate(); len(problems) != 0 {
		t.Fatalf("unexpected: %v", problems)
	}

	problems := (NewUser{Name: "Root; reboot", Password: "short"}).Validate()
	if problems["name"] == "" || problems["password"] == "" {
		t.Fatalf("expected problems: %v", problems)
	}
}

// An account whose home is a site's directory belongs to that site: the SSH
// users tab shows it protected, so sudo, locking and deleting all go through
// the site's page — and root is never mistaken for one.
func TestSiteAccountsAreProtected(t *testing.T) {
	users := []SSHUser{
		{Name: "root", UID: 0, Home: "/root"},
		{Name: "shop", UID: 1002, Home: "/var/www/shop.example/"},
		{Name: "deploy", UID: 1001, Home: "/home/deploy"},
	}

	markSiteAccounts(users, map[string]string{"/var/www/shop.example": "shop.example", "/root": "nope"})

	if users[0].Protected || users[0].Site != "" {
		t.Fatalf("root was taken for a site account: %+v", users[0])
	}

	if !users[1].Protected || users[1].Site != "shop.example" {
		t.Fatalf("the site account is not protected: %+v", users[1])
	}

	if users[2].Protected || users[2].Site != "" {
		t.Fatalf("a machine account was taken for a site's: %+v", users[2])
	}
}
