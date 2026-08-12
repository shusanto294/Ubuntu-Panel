package sites

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

const (
	// SFTPGroup is the group that gets SFTP and nothing else. sshd is told
	// about it once, in a drop-in of the panel's own.
	SFTPGroup = "ubuntu-panel-sftp"

	// sshdDropIn is where that instruction lives. Its own file rather than an
	// edit of sshd_config: the packaged file belongs to apt, and an upgrade
	// that replaced it would take the panel's Match block with it.
	sshdDropIn = "/etc/ssh/sshd_config.d/ubuntu-panel.conf"
)

// sshdConfig is that drop-in.
//
// ForceCommand internal-sftp is what makes an account SFTP-only: the shell is
// already nologin, and without this sshd would refuse the SFTP session too —
// which is the usual way an "SFTP user" turns out not to be able to connect at
// all.
//
// No ChrootDirectory. A chroot has to be owned by root and not writable by the
// user inside it, and the directory this account exists to write to is the
// site's own — so a chroot here would mean either giving up the writes or
// giving root's ownership away. The account is confined by being able to reach
// only what www-data can reach.
const sshdConfig = `# Managed by Ubuntu Panel.
#
# Site accounts created with "SFTP only" land in this group: no shell, no port
# forwarding, and file transfer through sshd's own subsystem.
Match Group ` + SFTPGroup + `
    ForceCommand internal-sftp
    AllowTcpForwarding no
    X11Forwarding no
    PermitTunnel no
`

// AccessUser is one system account that can reach a site's files.
type AccessUser struct {
	Name     string `json:"name"`
	Shell    string `json:"shell"`
	Home     string `json:"home"`
	SFTPOnly bool   `json:"sftp_only"`
}

// validAccountName is the rule useradd itself applies, spelled out so the
// refusal happens here with a sentence rather than there with an exit code.
func validAccountName(name string) bool {
	if len(name) < 3 || len(name) > 32 {
		return false
	}

	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}

	return true
}

// AccessUsers are the accounts whose home is this site.
//
// Read from the machine's own password file rather than from a table of the
// panel's: an account is a real system account, `useradd` is what makes one,
// and the answer to "who can log in to this site" is whatever getent says.
func (s *Service) AccessUsers(site *store.Site) []AccessUser {
	root := strings.TrimRight(site.RootPath, "/")

	result := proc.Run("getent passwd", 30*time.Second)

	inGroup := map[string]bool{}

	// One call for the group rather than one per user.
	if members := proc.Run("getent group "+SFTPGroup+" 2>/dev/null", 30*time.Second); members.OK() {
		if _, list, found := strings.Cut(strings.TrimSpace(members.Output), ":"); found {
			if _, names, ok := strings.Cut(list, ":"); ok {
				for _, name := range strings.Split(names, ",") {
					inGroup[strings.TrimSpace(name)] = true
				}
			}
		}
	}

	users := []AccessUser{}

	for _, line := range strings.Split(result.Output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) < 7 {
			continue
		}

		name, home, shell := fields[0], fields[5], fields[6]

		if strings.TrimRight(home, "/") != root {
			continue
		}

		// www-data owns every site directory and is not an account anybody
		// signs in as.
		if name == "www-data" || name == "root" {
			continue
		}

		users = append(users, AccessUser{
			Name:     name,
			Shell:    shell,
			Home:     home,
			SFTPOnly: inGroup[name] || strings.Contains(shell, "nologin"),
		})
	}

	return users
}

// CreateAccessUser adds a system account for a site.
//
// Its home is the site directory and its group is www-data, so what it writes
// is what the web server can read — an SFTP account that uploads files PHP
// cannot open is the same as no account at all.
func (s *Service) CreateAccessUser(ctx context.Context, siteID int64,
	name, password string, sftpOnly bool) error {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return errors.New("that site no longer exists")
	}

	if !validAccountName(name) {
		return errors.New("a username is 3 to 32 characters, starts with a letter, " +
			"and holds only lowercase letters, digits, - and _")
	}

	if len(password) < 10 {
		return errors.New("use a password of at least 10 characters — this one opens a " +
			"shell on the server")
	}

	if proc.Ok("id -u " + proc.Quote(name) + " >/dev/null 2>&1") {
		return fmt.Errorf("%s already exists on this machine", name)
	}

	if err := s.prepareSSHAccess(); err != nil {
		return err
	}

	shell := "/bin/bash"
	if sftpOnly {
		shell = "/usr/sbin/nologin"
	}

	// -M, because the home directory is the site and already exists; creating
	// it would mean writing a skeleton into somebody's document root.
	command := fmt.Sprintf("sudo useradd -M -d %s -s %s -g www-data %s",
		proc.Quote(strings.TrimRight(site.RootPath, "/")), proc.Quote(shell), proc.Quote(name))

	if result := proc.Run(command, 2*time.Minute); !result.OK() {
		return fmt.Errorf("the account was not created: %s", strings.TrimSpace(result.Output))
	}

	if err := s.setAccountPassword(name, password); err != nil {
		proc.Ok("sudo userdel " + proc.Quote(name))

		return err
	}

	if sftpOnly && !proc.Ok(fmt.Sprintf("sudo usermod -aG %s %s", SFTPGroup, proc.Quote(name))) {
		proc.Ok("sudo userdel " + proc.Quote(name))

		return errors.New("the account could not be put in the SFTP group")
	}

	return s.shareSiteDirectory(site)
}

// SetAccessPassword changes one account's password.
func (s *Service) SetAccessPassword(ctx context.Context, siteID int64, name, password string) error {
	if _, err := s.accessUser(ctx, siteID, name); err != nil {
		return err
	}

	if len(password) < 10 {
		return errors.New("use a password of at least 10 characters")
	}

	return s.setAccountPassword(name, password)
}

// DeleteAccessUser removes the account. The site's files stay: they are the
// site's, not the account's, whatever the passwd file says about its home.
func (s *Service) DeleteAccessUser(ctx context.Context, siteID int64, name string) error {
	if _, err := s.accessUser(ctx, siteID, name); err != nil {
		return err
	}

	if result := proc.Run("sudo userdel "+proc.Quote(name), 2*time.Minute); !result.OK() {
		return fmt.Errorf("the account was not removed: %s", strings.TrimSpace(result.Output))
	}

	return nil
}

// accessUser is the guard every change goes through: the account has to be one
// of this site's, not any account on the machine that shares a name with one.
func (s *Service) accessUser(ctx context.Context, siteID int64, name string) (AccessUser, error) {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return AccessUser{}, errors.New("that site no longer exists")
	}

	if !validAccountName(name) {
		return AccessUser{}, errors.New("that is not an account name the panel manages")
	}

	for _, user := range s.AccessUsers(site) {
		if user.Name == name {
			return user, nil
		}
	}

	return AccessUser{}, fmt.Errorf("%s is not an account for this site", name)
}

// setAccountPassword hands the password to chpasswd through a file.
//
// Never on a command line: an argument is readable by anybody who can run `ps`
// for as long as the process lives, and this one opens a session on the
// server.
func (s *Service) setAccountPassword(name, password string) error {
	staged, err := os.CreateTemp("", "ubuntu-panel-account-*")
	if err != nil {
		return errors.New("the password could not be staged")
	}

	defer os.Remove(staged.Name())

	staged.Chmod(0o600)

	if _, err := staged.WriteString(name + ":" + password + "\n"); err != nil {
		staged.Close()

		return errors.New("the password could not be staged")
	}

	staged.Close()

	if !proc.Ok("sudo chpasswd < " + proc.Quote(staged.Name())) {
		return errors.New("the password was not accepted")
	}

	return nil
}

// prepareSSHAccess makes sure the group and sshd's instruction about it exist.
//
// Checked and written every time an account is made rather than once at
// install: sshd is a package that gets upgraded and a file that gets edited,
// and the failure it produces — an SFTP account that connects and is then
// closed — says nothing about which of the two is missing.
func (s *Service) prepareSSHAccess() error {
	proc.Ok("sudo groupadd -f " + SFTPGroup)

	// The drop-in only works where sshd_config includes the directory, which
	// is every Ubuntu since 22.04 and not the ones before it.
	if !proc.Ok("sudo grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config.d/' " +
		"/etc/ssh/sshd_config") {
		return errors.New("this machine's sshd does not read /etc/ssh/sshd_config.d, so the " +
			"panel cannot add its SFTP rule — add the Match block by hand, or create the " +
			"account with a shell instead")
	}

	current, _ := proc.ReadFile(sshdDropIn, true)

	if strings.TrimSpace(current) == strings.TrimSpace(sshdConfig) {
		return nil
	}

	if err := proc.PutFile(sshdDropIn, sshdConfig, true); err != nil {
		return err
	}

	proc.Ok(fmt.Sprintf("sudo chown root:root %[1]s && sudo chmod 644 %[1]s",
		proc.Quote(sshdDropIn)))

	// Tested before the reload. A configuration sshd refuses is a daemon that
	// does not come back, and the way anybody administers this machine is
	// through it.
	if check := proc.Run("sudo sshd -t 2>&1", time.Minute); !check.OK() {
		proc.Ok("sudo rm -f " + proc.Quote(sshdDropIn))

		return fmt.Errorf("sshd refused the panel's rule, so it was taken back out:\n%s",
			strings.TrimSpace(check.Output))
	}

	proc.Ok("sudo systemctl reload ssh 2>/dev/null || sudo systemctl reload sshd")

	return nil
}

// shareSiteDirectory makes the site's files writable by its accounts.
//
// The group is www-data on every file, the group bit is set on every
// directory, and new directories inherit it — so a file uploaded over SFTP is
// a file PHP-FPM can read, and one written by PHP is a file the account can
// edit. Without the setgid bit that is true only until somebody creates a
// folder.
func (s *Service) shareSiteDirectory(site *store.Site) error {
	root := proc.Quote(strings.TrimRight(site.RootPath, "/"))

	command := fmt.Sprintf(
		"sudo chgrp -R www-data %[1]s && "+
			"sudo chmod -R g+rwX %[1]s && "+
			"sudo find %[1]s -type d -exec chmod g+s {} +", root)

	if !proc.Ok(command) {
		return errors.New("the account was created, but the site's files could not be shared " +
			"with it — check the permissions on the site directory")
	}

	return nil
}
