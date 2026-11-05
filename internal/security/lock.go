package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// Locking a site's files: tamper protection.
//
// PHP runs as www-data, and so do the panel's deploys, so a site's code
// belongs to the same account every request runs as. One vulnerable plugin
// and a request can rewrite any file of the site — that is how a web shell
// gets in and how index.php gets its injected script. A locked site's code is
// handed to root and made unwritable by anyone else, so PHP can still read
// and run every file but can change none of them, nor add a file beside them.
// The directories a site writes to as it works — uploads, caches, Laravel's
// storage — stay as they were.
//
// It is how aaPanel's tamper protection works without a kernel module, and
// it has the same price: WordPress cannot update itself, its plugins or its
// themes while locked. The panel unlocks a site for its own work — deploys,
// restores, PHP version moves — and locks it again after, and the Protection
// tab unlocks one for thirty minutes for updates by hand.
//
// What each locked entry's owner and mode were is written to a record first,
// and unlocking puts back exactly that.

// writableDirs are directories left alone: the site writes to them as it runs.
// Matched by name at any depth.
var writableDirs = []string{
	"uploads", "cache", "caches", "tmp", "temp", "storage", "sessions", "logs", "log",
	"upgrade", "upgrade-temp-backup", "wflogs", "w3tc-config", "litespeed", "et-cache", "wpo-cache",
	"backups", "backup", "ai1wm-backups", "updraft", "languages", "fonts", ".well-known",
}

// LockRecord is where a locked site's original owners and modes are kept.
func LockRecord(dataDir, domain string) string {
	return filepath.Join(dataDir, "locks", strings.ReplaceAll(domain, "/", "_")+".tsv")
}

// lockFind is the find that selects what a lock covers, ending in what to do
// with it.
func lockFind(root, action string) string {
	prune := make([]string, len(writableDirs))
	for i, dir := range writableDirs {
		prune[i] = "-name " + proc.Quote(dir)
	}

	// The root itself is never pruned, whatever it is called.
	return fmt.Sprintf("find %s -xdev -mindepth 1 \\( -type d \\( %s \\) -prune \\) -o \\( -type f -o -type d \\) %s",
		proc.Quote(root), strings.Join(prune, " -o "), action)
}

// LockSite hands the site's code to root, read-only to everyone else.
func LockSite(root, record string) error {
	root = strings.TrimRight(root, "/")
	if root == "" || root == "/" || !strings.HasPrefix(root, "/") {
		return errors.New("that site has no directory to lock")
	}
	if !proc.Exists(root, true) {
		return fmt.Errorf("%s is not there", root)
	}

	// The record first: without it, unlocking cannot put the owners back.
	listing := proc.Run("sudo "+lockFind(root, `-printf '%u\t%m\t%p\n'`)+" 2>/dev/null; "+
		"sudo stat -c '%U\t%a\t%n' "+proc.Quote(root), 10*time.Minute)
	if !listing.OK() && strings.TrimSpace(listing.Output) == "" {
		return fmt.Errorf("could not list %s: %s", root, lastLines(listing.Output, 2))
	}

	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		return fmt.Errorf("could not keep the lock's record: %w", err)
	}
	if err := os.WriteFile(record, []byte(listing.Output), 0o600); err != nil {
		return fmt.Errorf("could not keep the lock's record: %w", err)
	}

	result := proc.Run("sudo "+lockFind(root, `-exec chown root {} + -exec chmod go-w {} +`)+
		" && sudo chown root "+proc.Quote(root)+" && sudo chmod go-w "+proc.Quote(root), 15*time.Minute)
	if !result.OK() {
		UnlockSite(root, record)
		return fmt.Errorf("the files could not be locked, so they were left as they were: %s", lastLines(result.Output, 3))
	}

	return nil
}

// UnlockSite puts back every owner and mode the record holds, grouped so it
// is a handful of commands rather than one per file.
func UnlockSite(root, record string) error {
	contents, err := os.ReadFile(record)
	if err != nil {
		return errors.New("the record of how the files were is missing, so the site cannot be unlocked automatically — " +
			"run chown -R on its directory to give it back to its owner")
	}

	owners := map[string][]string{}
	modes := map[string][]string{}

	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || !strings.HasPrefix(fields[2], strings.TrimRight(root, "/")) {
			continue
		}
		owner, mode, path := fields[0], fields[1], fields[2]
		if ValidUsername(owner) || owner == "root" || isNumber(owner) {
			owners[owner] = append(owners[owner], path)
		}
		if number, err := strconv.ParseUint(mode, 8, 32); err == nil && number&0o022 != 0 {
			modes[mode] = append(modes[mode], path)
		}
	}

	apply := func(command string, paths []string) error {
		list, err := os.CreateTemp("", "ubuntu-panel-unlock-*")
		if err != nil {
			return err
		}
		defer os.Remove(list.Name())
		list.WriteString(strings.Join(paths, "\x00"))
		list.Close()
		os.Chmod(list.Name(), 0o644)

		if result := proc.Run("sudo xargs -0 -a "+proc.Quote(list.Name())+" "+command+" 2>&1", 15*time.Minute); !result.OK() {
			return fmt.Errorf("%s", lastLines(result.Output, 2))
		}
		return nil
	}

	keys := func(m map[string][]string) []string {
		list := make([]string, 0, len(m))
		for key := range m {
			list = append(list, key)
		}
		sort.Strings(list)
		return list
	}

	for _, owner := range keys(owners) {
		if err := apply("chown -h -- "+proc.Quote(owner), owners[owner]); err != nil {
			return fmt.Errorf("could not give the files back to %s: %w", owner, err)
		}
	}
	for _, mode := range keys(modes) {
		if err := apply("chmod -- "+mode, modes[mode]); err != nil {
			return fmt.Errorf("could not put the permissions back: %w", err)
		}
	}

	os.Remove(record)

	return nil
}

func isNumber(text string) bool {
	_, err := strconv.Atoi(text)
	return err == nil
}

// SiteLocked is whether a site's lock record exists — the sign that it is
// locked, whatever the settings say.
func SiteLocked(record string) bool {
	_, err := os.Stat(record)
	return err == nil
}
