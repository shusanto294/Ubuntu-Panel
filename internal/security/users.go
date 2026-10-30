package security

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// SSHUser is an account on this machine that can open a shell, which is what
// "can log in over SSH" comes down to.
type SSHUser struct {
	Name     string   `json:"name"`
	UID      int      `json:"uid"`
	Home     string   `json:"home"`
	Shell    string   `json:"shell"`
	Groups   []string `json:"groups"`
	Sudo     bool     `json:"sudo"`
	Locked   bool     `json:"locked"`
	Password bool     `json:"has_password"`
	Keys     []SSHKey `json:"keys"`

	// Why it is locked, when it is: a locked password still lets a key in,
	// which is how Ubuntu ships root; an expired account lets nothing in,
	// which is how the panel locks one.
	PasswordLocked bool `json:"password_locked"`
	Expired        bool `json:"expired"`

	// Protected accounts are shown but not deleted, locked or demoted from the
	// panel: root, and the account the panel and its terminal run as. Losing
	// either is a way to lose the machine or the panel. Root can still be
	// given a password and unlocked, which only ever opens a way back in.
	Protected bool   `json:"protected"`
	Reason    string `json:"protected_reason"`

	// Site is the domain whose account this is, when its home is a site's
	// directory. Those belong to the site's page: from here they are shown
	// and nothing else, so nothing done on this tab — sudo above all — can
	// reach past the one site they were made for.
	Site string `json:"site"`
}

// SiteHomes maps a site's directory to its domain. The web package sets it;
// security has no way to know where sites live.
var SiteHomes func() map[string]string

// SSHKey is one line of an authorized_keys file.
type SSHKey struct {
	Type        string `json:"type"`
	Comment     string `json:"comment"`
	Fingerprint string `json:"fingerprint"`
}

var (
	usernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

	keyTypes = map[string]bool{
		"ssh-ed25519": true, "ssh-rsa": true, "ecdsa-sha2-nistp256": true,
		"ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true,
		"sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true,
	}
)

// ValidUsername is the shape useradd accepts by default, which is also what
// is safe on a command line unquoted.
func ValidUsername(name string) bool { return usernamePattern.MatchString(name) }

// ReadUsers lists the accounts that have a login shell: root, every user from
// uid 1000 up, and the panel's own account — a system account below 1000, but
// the one the built-in terminal opens as, so the one people go looking for.
// One sudo for all of it, because a machine can have dozens.
func ReadUsers(panelUser string) []SSHUser {
	if !ValidUsername(panelUser) {
		panelUser = ""
	}

	script := `getent passwd | while IFS=: read -r name _ uid _ _ home shell; do
  case "$shell" in */nologin|*/false|"") continue;; esac
  if [ "$uid" -ne 0 ] && [ "$uid" -lt 1000 ] && [ "$name" != "` + panelUser + `" ]; then continue; fi
  [ "$uid" -ge 65534 ] && continue
  echo "@@user $name $uid $home $shell"
  echo "@@groups $(id -nG "$name" 2>/dev/null)"
  echo "@@status $(passwd -S "$name" 2>/dev/null | awk '{print $2}') $(chage -l "$name" 2>/dev/null | awk -F': ' '/Account expires/ {print $2}' | tr ' ' '_')"
  if [ -f "$home/.ssh/authorized_keys" ]; then
    grep -E '^[^#]' "$home/.ssh/authorized_keys" 2>/dev/null | sed 's/^/@@key /'
  fi
done`

	result := proc.Run("sudo bash -c "+proc.Quote(script), time.Minute)

	users := parseUsers(result.Output, panelUser)

	if SiteHomes != nil {
		markSiteAccounts(users, SiteHomes())
	}

	return users
}

// markSiteAccounts protects the accounts whose home is a site's directory.
func markSiteAccounts(users []SSHUser, homes map[string]string) {
	for i := range users {
		if domain, ok := homes[strings.TrimRight(users[i].Home, "/")]; ok && users[i].UID != 0 {
			users[i].Site = domain
			users[i].Protected = true
			users[i].Reason = "This account belongs to " + domain + " and can reach that site's " +
				"files only. Manage it from the site's own page."
		}
	}
}

func parseUsers(output, panelUser string) []SSHUser {
	users := []SSHUser{}
	var current *SSHUser

	for _, line := range strings.Split(output, "\n") {
		kind, rest, _ := strings.Cut(strings.TrimSpace(line), " ")

		switch kind {
		case "@@user":
			fields := strings.Fields(rest)
			if len(fields) < 4 {
				current = nil
				continue
			}

			uid, _ := strconv.Atoi(fields[1])
			users = append(users, SSHUser{Name: fields[0], UID: uid, Home: fields[2], Shell: fields[3],
				Groups: []string{}, Keys: []SSHKey{}})
			current = &users[len(users)-1]

			switch {
			case current.UID == 0:
				current.Protected, current.Reason = true, "root cannot be locked or deleted from here. "+
					"Whether it may log in over SSH is under SSH settings."
			case current.Name == panelUser:
				current.Protected, current.Reason = true, "The panel and its built-in terminal run as "+
					"this account, so it cannot be locked or deleted from here."
			}

		case "@@groups":
			if current == nil {
				continue
			}

			current.Groups = strings.Fields(rest)
			for _, group := range current.Groups {
				if group == "sudo" || group == "admin" || group == "wheel" {
					current.Sudo = true
				}
			}

			if current.UID == 0 {
				current.Sudo = true
			}

		case "@@status":
			if current == nil {
				continue
			}

			fields := strings.Fields(rest)
			if len(fields) > 0 {
				// passwd -S: P has a password, L is locked, NP has none.
				current.Password = fields[0] == "P"
				current.PasswordLocked = fields[0] == "L"
			}

			// An account expired in the past is refused even with a key,
			// which is how the panel locks one.
			if len(fields) > 1 && fields[1] != "never" {
				if expires, err := time.Parse("Jan_02,_2006", fields[1]); err == nil && expires.Before(time.Now()) {
					current.Expired = true
				}
			}

			current.Locked = current.PasswordLocked || current.Expired

		case "@@key":
			if current == nil {
				continue
			}

			if key, ok := ParseKey(rest); ok {
				current.Keys = append(current.Keys, key)
			}
		}
	}

	sort.SliceStable(users, func(i, j int) bool { return users[i].UID < users[j].UID })

	return users
}

// ParseKey reads a public key line. Options before the key type
// (`from="…",no-pty ssh-ed25519 …`) are skipped over.
func ParseKey(line string) (SSHKey, bool) {
	fields := strings.Fields(strings.TrimSpace(line))

	for i, field := range fields {
		if !keyTypes[field] || i+1 >= len(fields) {
			continue
		}

		blob, err := base64.StdEncoding.DecodeString(fields[i+1])
		if err != nil || len(blob) < 16 {
			return SSHKey{}, false
		}

		sum := sha256.Sum256(blob)

		return SSHKey{
			Type:        field,
			Comment:     strings.Join(fields[i+2:], " "),
			Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
		}, true
	}

	return SSHKey{}, false
}

// ValidPublicKey is one key on one line, and nothing that could become a
// second line or an option in authorized_keys.
func ValidPublicKey(line string) bool {
	line = strings.TrimSpace(line)

	if line == "" || strings.ContainsAny(line, "\r\n") || len(line) > 16384 {
		return false
	}

	fields := strings.Fields(line)
	if len(fields) < 2 || !keyTypes[fields[0]] {
		return false
	}

	_, ok := ParseKey(line)

	return ok
}

// findUser refuses anything the panel should not touch.
func findUser(name, panelUser string) (SSHUser, error) {
	if !ValidUsername(name) {
		return SSHUser{}, errors.New("that is not a username")
	}

	for _, user := range ReadUsers(panelUser) {
		if user.Name == name {
			return user, nil
		}
	}

	return SSHUser{}, errors.New("there is no such login account")
}

// unlockable is editable, and root as well: setting root's password or
// unlocking it adds a way in rather than taking one away.
func unlockable(name, panelUser string) (SSHUser, error) {
	user, err := findUser(name, panelUser)
	if err != nil {
		return user, err
	}

	if user.Protected && user.UID != 0 {
		return user, errors.New(user.Reason)
	}

	return user, nil
}

func editable(name, panelUser string) (SSHUser, error) {
	user, err := findUser(name, panelUser)
	if err != nil {
		return user, err
	}

	if user.Protected {
		return user, errors.New(user.Reason)
	}

	return user, nil
}

// NewUser is the "add a user" form.
type NewUser struct {
	Name      string
	Password  string
	PublicKey string
	Sudo      bool
}

// Validate is what the form checks.
func (n NewUser) Validate() map[string]string {
	problems := map[string]string{}

	if !ValidUsername(n.Name) {
		problems["name"] = "Use lower-case letters, digits, - and _, starting with a letter; at most 32."
	}

	if n.Password == "" && n.PublicKey == "" {
		problems["password"] = "Give a password, an SSH key, or both — otherwise nobody can log in as it."
	}

	if n.Password != "" && len(n.Password) < 12 {
		problems["password"] = "Use at least 12 characters."
	}

	if strings.ContainsAny(n.Password, "\r\n:") {
		problems["password"] = "A password cannot contain a colon or a line break."
	}

	if n.PublicKey != "" && !ValidPublicKey(n.PublicKey) {
		problems["public_key"] = "Paste one public key, the single line from a .pub file."
	}

	return problems
}

// CreateUser adds an account with a home directory and a bash shell.
func CreateUser(user NewUser) error {
	if proc.Ok("getent passwd " + user.Name + " >/dev/null") {
		return errors.New("an account with that name already exists")
	}

	if result := proc.Run("sudo useradd -m -s /bin/bash "+user.Name, time.Minute); !result.OK() {
		return fmt.Errorf("useradd refused: %s", lastLines(result.Output, 3))
	}

	if user.Password != "" {
		if err := SetPassword(user.Name, user.Password, ""); err != nil {
			return err
		}
	}

	if user.Sudo {
		if err := SetSudo(user.Name, true, ""); err != nil {
			return err
		}
	}

	if user.PublicKey != "" {
		if err := AddKey(user.Name, user.PublicKey, ""); err != nil {
			return err
		}
	}

	return nil
}

// SetPassword changes an account's password. The pair is fed to chpasswd from
// a file only this process can read, so the password is never on a command
// line where `ps` would show it.
func SetPassword(name, password, panelUser string) error {
	if panelUser != "" {
		if _, err := unlockable(name, panelUser); err != nil {
			return err
		}
	}

	if len(password) < 12 || strings.ContainsAny(password, "\r\n:") {
		return errors.New("use at least 12 characters, with no colon or line break")
	}

	staged, err := os.CreateTemp("", "ubuntu-panel-passwd-*")
	if err != nil {
		return errors.New("could not stage the password")
	}

	path := staged.Name()
	defer os.Remove(path)

	staged.Chmod(0o600)
	staged.WriteString(name + ":" + password + "\n")
	staged.Close()

	if result := proc.Run("sudo chpasswd < "+proc.Quote(path), time.Minute); !result.OK() {
		return fmt.Errorf("chpasswd refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// SetSudo adds an account to the sudo group or takes it out.
func SetSudo(name string, enabled bool, panelUser string) error {
	if panelUser != "" {
		if _, err := editable(name, panelUser); err != nil {
			return err
		}
	}

	command := "sudo gpasswd -d " + name + " sudo"
	if enabled {
		command = "sudo usermod -aG sudo " + name
	}

	if result := proc.Run(command, time.Minute); !result.OK() {
		return fmt.Errorf("could not change sudo: %s", lastLines(result.Output, 3))
	}

	return nil
}

// SetLocked locks an account against every kind of login, keys included, or
// lets it back in. A locked password alone would still let a key through, so
// the account is also expired, which sshd refuses whatever the method.
func SetLocked(name string, locked bool, panelUser string) error {
	if !locked {
		return unlock(name, panelUser)
	}

	if _, err := editable(name, panelUser); err != nil {
		return err
	}

	command := "sudo usermod -L " + name + " && sudo chage -E 0 " + name +
		" && sudo pkill -KILL -u " + name + " 2>/dev/null; true"

	if result := proc.Run(command, time.Minute); !result.OK() {
		return fmt.Errorf("could not change the account: %s", lastLines(result.Output, 3))
	}

	return nil
}

// unlock lifts the expiry and the password lock. usermod will not unlock a
// password that is only the lock — that would leave an account anybody can
// log in to without one — so for an account with no password underneath,
// which is root on a fresh Ubuntu, the way to unlock it is to give it one.
func unlock(name, panelUser string) error {
	user, err := unlockable(name, panelUser)
	if err != nil {
		return err
	}

	if result := proc.Run("sudo chage -E -1 "+name+" && (sudo usermod -U "+name+" 2>/dev/null || true)",
		time.Minute); !result.OK() {
		return fmt.Errorf("could not change the account: %s", lastLines(result.Output, 3))
	}

	// Anybody else locked by the panel was locked by expiry, which is lifted
	// now, so their keys work whatever the password says.
	if user.UID != 0 {
		return nil
	}

	status := strings.Fields(proc.Run("sudo passwd -S root", 30*time.Second).Output)
	if len(status) > 1 && status[1] == "L" {
		return errors.New("root has no password underneath the lock, so there is nothing to " +
			"unlock. Set a password for it below — that unlocks it.")
	}

	return nil
}

// DeleteUser removes an account, its processes, and optionally its home.
func DeleteUser(name string, removeHome bool, panelUser string) error {
	if _, err := editable(name, panelUser); err != nil {
		return err
	}

	flag := ""
	if removeHome {
		flag = "-r "
	}

	result := proc.Run("sudo pkill -KILL -u "+name+" 2>/dev/null; sleep 1; sudo userdel "+flag+name+
		" 2>&1 | grep -v 'mail spool' ; getent passwd "+name+" >/dev/null && exit 1 || exit 0", time.Minute)
	if !result.OK() {
		return fmt.Errorf("userdel refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// authorizedKeysPath is where an account's keys live.
func authorizedKeysPath(user SSHUser) string { return user.Home + "/.ssh/authorized_keys" }

// AddKey appends a public key to an account's authorized_keys, creating the
// directory and file with the permissions sshd insists on.
func AddKey(name, key, panelUser string) error {
	user, err := findUser(name, panelUser)
	if err != nil {
		return err
	}

	// Adding a key to root is allowed — it is how passwords get turned off —
	// but not to the panel's own account.
	if user.Protected && user.UID != 0 {
		return errors.New(user.Reason)
	}

	key = strings.TrimSpace(key)
	if !ValidPublicKey(key) {
		return errors.New("that is not a public key")
	}

	parsed, _ := ParseKey(key)
	for _, existing := range user.Keys {
		if existing.Fingerprint == parsed.Fingerprint {
			return errors.New("that key is already installed for " + name)
		}
	}

	path := authorizedKeysPath(user)
	current, _ := proc.ReadFile(path, true)

	if current != "" && !strings.HasSuffix(current, "\n") {
		current += "\n"
	}

	return writeAuthorizedKeys(user, path, current+key+"\n")
}

// RemoveKey takes a key out by its fingerprint, leaving every other line —
// comments and options included — exactly as it was.
func RemoveKey(name, fingerprint, panelUser string) error {
	user, err := findUser(name, panelUser)
	if err != nil {
		return err
	}

	if user.Protected && user.UID != 0 {
		return errors.New(user.Reason)
	}

	path := authorizedKeysPath(user)
	current, err := proc.ReadFile(path, true)
	if err != nil {
		return errors.New("could not read " + path)
	}

	kept := []string{}
	removed := false

	for _, line := range strings.Split(strings.TrimRight(current, "\n"), "\n") {
		if key, ok := ParseKey(line); ok && key.Fingerprint == fingerprint && !removed {
			removed = true
			continue
		}

		kept = append(kept, line)
	}

	if !removed {
		return errors.New("that key is not installed for " + name)
	}

	contents := strings.Join(kept, "\n")
	if contents != "" {
		contents += "\n"
	}

	return writeAuthorizedKeys(user, path, contents)
}

func writeAuthorizedKeys(user SSHUser, path, contents string) error {
	directory := user.Home + "/.ssh"

	if result := proc.Run(fmt.Sprintf("sudo install -d -m 700 -o %s -g $(id -gn %s) %s",
		user.Name, user.Name, proc.Quote(directory)), time.Minute); !result.OK() {
		return fmt.Errorf("could not prepare %s: %s", directory, lastLines(result.Output, 2))
	}

	if err := proc.PutFile(path, contents, true); err != nil {
		return err
	}

	if result := proc.Run(fmt.Sprintf("sudo chown %s:$(id -gn %s) %s && sudo chmod 600 %s",
		user.Name, user.Name, proc.Quote(path), proc.Quote(path)), time.Minute); !result.OK() {
		return fmt.Errorf("could not set the permissions on %s: %s", path, lastLines(result.Output, 2))
	}

	return nil
}
