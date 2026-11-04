package security

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Instant bans: nginx refuses an address the moment a website jail bans it.
//
// The firewall ban fail2ban makes is immediate, but it cannot stop a visitor
// whose requests arrive through Cloudflare — the firewall sees Cloudflare.
// Only nginx can, and the full list of banned addresses (NginxBansPath) is
// rebuilt once a minute from a copy of the Security page's state that can
// itself be a minute or two old. A scanner had minutes to keep going.
//
// So the website jails carry one more fail2ban action, which runs the panel's
// binary with --nginx-ban <ip> as fail2ban bans: it adds the address to a file
// of its own and reloads nginx, within a second of the request. fail2ban's
// unban takes it out again. The minute's full list stays what is
// authoritative; this file only gets there first, and anything left in it
// that is no longer banned is pruned by the panel (StaleInstantBans).

// InstantBansPath is the file the action writes. In conf.d, beside the full
// list: nginx is fine with an address denied twice.
const InstantBansPath = "/etc/nginx/conf.d/panel-banned-instant.conf"

// instantBansLock serialises the action: fail2ban runs each jail's actions on
// a thread of their own, and two bans at once must not lose one another.
const instantBansLock = "/run/ubuntu-panel-nginx-bans.lock"

// NginxActionName and nginxActionPath are the fail2ban action.
const (
	NginxActionName = "ubuntu-panel-nginx"
	nginxActionPath = "/etc/fail2ban/action.d/ubuntu-panel-nginx.conf"
)

// nginxActionFile is the action. actionflush is a command that does nothing,
// on purpose: without one fail2ban unbans every address one by one when a jail
// stops — on every fail2ban restart — and bans them again one by one when it
// starts, a reload of nginx for each. Kept, the file rides through a restart
// untouched, and a ban fail2ban restores is already there.
var nginxActionFile = "# Managed by Ubuntu Panel: nginx refuses an address the moment a website jail bans it.\n" +
	"[Definition]\n" +
	"actionstart =\n" +
	"actionstop =\n" +
	"actioncheck =\n" +
	"actionflush = true\n" +
	"actionban = " + BotCheckBinary + " --nginx-ban <ip>\n" +
	"actionunban = " + BotCheckBinary + " --nginx-unban <ip>\n"

// InstantBan adds an address to the instant file (ban) or takes it out, and
// reloads nginx when the file changed. Run as root — by fail2ban, or by the
// panel through sudo. Nothing to do on a machine without nginx.
func InstantBan(address string, ban bool) error {
	address = strings.TrimSpace(address)
	if !ValidAddress(address) {
		return fmt.Errorf("%q is not an address", address)
	}

	// Cloudflare's own addresses never: a site behind it would refuse
	// everybody. fail2ban ignores them already; this is the second lock.
	if ban && FromCloudflare(address) {
		return nil
	}

	if info, err := os.Stat(filepath.Dir(InstantBansPath)); err != nil || !info.IsDir() {
		return nil
	}

	lock, err := os.OpenFile(instantBansLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	previous, err := os.ReadFile(InstantBansPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	bans := parseInstantBans(string(previous))

	if _, there := bans[address]; there == ban {
		return nil
	}

	if ban {
		bans[address] = time.Now().Unix()
	} else {
		delete(bans, address)
	}

	if len(bans) == 0 {
		if err := os.Remove(InstantBansPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		temp := InstantBansPath + ".tmp"
		if err := os.WriteFile(temp, []byte(renderInstantBans(bans)), 0o644); err != nil {
			return err
		}
		if err := os.Rename(temp, InstantBansPath); err != nil {
			os.Remove(temp)
			return err
		}
	}

	return reloadNginxNow()
}

// reloadNginxNow has systemd reload nginx without waiting for it. A reload
// queued while another is still waiting is merged into it, so a burst of bans
// costs nginx one or two reloads rather than one each. nginx checks the new
// configuration itself and keeps the running one if it is wrong.
func reloadNginxNow() error {
	if output, err := exec.Command("systemctl", "reload", "--no-block", "nginx").CombinedOutput(); err != nil {
		if fallback, err2 := exec.Command("nginx", "-s", "reload").CombinedOutput(); err2 != nil {
			return fmt.Errorf("nginx would not reload: %s %s", strings.TrimSpace(string(output)),
				strings.TrimSpace(string(fallback)))
		}
	}

	return nil
}

// parseInstantBans reads the file: one "deny <address>; # <unix time>" a
// line, the time it was banned. Anything else is skipped.
func parseInstantBans(contents string) map[string]int64 {
	bans := map[string]int64{}

	for _, line := range strings.Split(contents, "\n") {
		rule, comment, _ := strings.Cut(strings.TrimSpace(line), "#")
		rule = strings.TrimSpace(rule)

		if !strings.HasPrefix(rule, "deny ") || !strings.HasSuffix(rule, ";") {
			continue
		}

		address := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rule, "deny "), ";"))
		if !ValidAddress(address) {
			continue
		}

		at, _ := strconv.ParseInt(strings.TrimSpace(comment), 10, 64)
		bans[address] = at
	}

	return bans
}

func renderInstantBans(bans map[string]int64) string {
	addresses := make([]string, 0, len(bans))
	for address := range bans {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)

	var file strings.Builder
	file.WriteString("# Managed by Ubuntu Panel — addresses the website jails banned, refused\n")
	file.WriteString("# by nginx the moment they were. The full list is panel-banned.conf.\n")
	for _, address := range addresses {
		fmt.Fprintf(&file, "deny %s; # %d\n", address, bans[address])
	}

	return file.String()
}

// StaleInstantBans are the addresses in the instant file that are banned no
// longer — a jail turned off takes its bans away without unbanning each — and
// were added longer than grace ago: what the panel knows as banned can lag the
// jails by a few minutes, and a ban that new is not to be second-guessed.
func StaleInstantBans(contents string, banned []string, now time.Time, grace time.Duration) []string {
	still := map[string]bool{}
	for _, address := range banned {
		still[strings.TrimSpace(address)] = true
	}

	stale := []string{}
	for address, at := range parseInstantBans(contents) {
		if !still[address] && now.Sub(time.Unix(at, 0)) > grace {
			stale = append(stale, address)
		}
	}
	sort.Strings(stale)

	return stale
}

// ReleaseInstant takes an address off the instant file from the panel, which
// is not root: through sudo and the same binary, so it takes the same lock
// fail2ban's action does. Nothing to do when the file does not name it.
func ReleaseInstant(address string) error {
	if !ValidAddress(strings.TrimSpace(address)) {
		return fmt.Errorf("%q is not an address", address)
	}

	contents, err := proc.ReadFile(InstantBansPath, true)
	if err != nil {
		return nil
	}

	if _, there := parseInstantBans(contents)[strings.TrimSpace(address)]; !there {
		return nil
	}

	if result := proc.Run("sudo "+BotCheckBinary+" --nginx-unban "+proc.Quote(strings.TrimSpace(address))+" 2>&1",
		time.Minute); !result.OK() {
		return fmt.Errorf("nginx still refuses %s: %s", address, lastLines(result.Output, 2))
	}

	return nil
}
