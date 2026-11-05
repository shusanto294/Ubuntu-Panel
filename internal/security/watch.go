package security

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The backdoor watch: what an intruder adds to keep a way back in once the
// hole they came through is closed. A new account that can log in, a new
// member of sudo, a cron job, an SSH key, a systemd service, a library forced
// into every program (/etc/ld.so.preload, the usual rootkit trick). The panel
// takes a picture of all of it, compares every few minutes, and says what
// appeared or went. It never undoes anything: the owner may well have added
// it, and one press on the Protection tab takes the new state as the normal
// one.

// WatchItem is one thing the watch keeps an eye on.
type WatchItem struct {
	Kind   string `json:"kind"`   // user | sudo | cron | key | service | preload
	Name   string `json:"name"`   // what it is called
	Detail string `json:"detail"` // a fingerprint, so a changed one is seen
}

func (w WatchItem) key() string { return w.Kind + "\x00" + w.Name + "\x00" + w.Detail }

// WatchChange is an item that appeared or went since the last picture.
type WatchChange struct {
	At      int64     `json:"at"`
	Change  string    `json:"change"` // added | removed
	Item    WatchItem `json:"item"`
	Summary string    `json:"summary"`
}

// watchScript prints the picture, one item per line: kind, name, detail,
// separated by tabs. One sudo for all of it.
const watchScript = `sudo sh -c '
getent passwd | while IFS=: read -r name pw uid gid gecos home shell; do
  case "$shell" in */nologin|*/false|*/sync|*/halt|*/shutdown|"") continue;; esac
  printf "user\t%s\tuid=%s shell=%s home=%s\n" "$name" "$uid" "$shell" "$home"
done
getent passwd | awk -F: '"'"'$3 == 0 && $1 != "root" { printf "user\t%s\tuid=0\n", $1 }'"'"'
for g in sudo admin wheel; do
  getent group "$g" | cut -d: -f4 | tr , "\n" | while read -r m; do [ -n "$m" ] && printf "sudo\t%s\tgroup %s\n" "$m" "$g"; done
done
for f in /etc/sudoers /etc/sudoers.d/* /etc/crontab /etc/cron.d/* /var/spool/cron/crontabs/* /etc/ld.so.preload; do
  [ -f "$f" ] || continue
  sum=$(grep -v "^[[:space:]]*#" "$f" | grep -v "^[[:space:]]*$" | sha256sum | cut -c1-12)
  case "$f" in
    /etc/sudoers*) printf "sudo\t%s\t%s\n" "$f" "$sum";;
    /etc/ld.so.preload) [ -s "$f" ] && printf "preload\t%s\t%s\n" "$f" "$sum";;
    *) printf "cron\t%s\t%s\n" "$f" "$sum";;
  esac
done
getent passwd | while IFS=: read -r name pw uid gid gecos home shell; do
  for k in "$home/.ssh/authorized_keys" "$home/.ssh/authorized_keys2"; do
    [ -f "$k" ] || continue
    grep -v "^[[:space:]]*#" "$k" | grep -v "^[[:space:]]*$" | while read -r line; do
      printf "key\t%s\t%s\n" "$name" "$(printf %s "$line" | sha256sum | cut -c1-12) $(printf %s "$line" | awk "{print \$NF}" | cut -c1-60)"
    done
  done
done
for u in /etc/systemd/system/*.service /etc/systemd/system/*/*.service /etc/systemd/system/*.timer; do
  [ -e "$u" ] || continue
  printf "service\t%s\t%s\n" "${u#/etc/systemd/system/}" "$(readlink -f "$u" | sha256sum | cut -c1-8)"
done
' 2>/dev/null`

// WatchEnv says which items are the panel's own, so they never alarm.
type WatchEnv struct {
	// SiteHomes are the sites' directories: an account whose home is one is
	// a site account the panel made.
	SiteHomes []string

	// PanelUnits are the systemd units the panel writes: its own, and the
	// proxied sites'.
	PanelUnits []string
}

// TakeWatch is the picture as it is now.
func TakeWatch(env WatchEnv) []WatchItem {
	return ParseWatch(proc.Run(watchScript, 2*time.Minute).Output, env)
}

// ParseWatch reads watchScript's lines, leaving out the panel's own items.
func ParseWatch(output string, env WatchEnv) []WatchItem {
	items := []WatchItem{}
	seen := map[string]bool{}

	ownUnit := func(name string) bool {
		base := name[strings.LastIndex(name, "/")+1:]
		if strings.HasPrefix(base, "ubuntu-panel") {
			return true
		}
		for _, unit := range env.PanelUnits {
			if base == unit || base == unit+".service" {
				return true
			}
		}
		return false
	}

	siteHome := func(detail string) bool {
		_, home, found := strings.Cut(detail, "home=")
		if !found {
			return false
		}
		for _, dir := range env.SiteHomes {
			dir = strings.TrimRight(dir, "/")
			if dir != "" && (home == dir || strings.HasPrefix(home, dir+"/")) {
				return true
			}
		}
		return false
	}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}

		item := WatchItem{Kind: fields[0], Name: fields[1], Detail: fields[2]}

		if item.Kind == "service" && ownUnit(item.Name) {
			continue
		}
		if item.Kind == "user" && siteHome(item.Detail) {
			continue
		}

		if !seen[item.key()] {
			seen[item.key()] = true
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].key() < items[j].key() })

	return items
}

// DiffWatch is what appeared and went between two pictures.
func DiffWatch(before, after []WatchItem, now time.Time) []WatchChange {
	was := map[string]bool{}
	for _, item := range before {
		was[item.key()] = true
	}
	is := map[string]bool{}
	for _, item := range after {
		is[item.key()] = true
	}

	changes := []WatchChange{}

	for _, item := range after {
		if !was[item.key()] {
			changes = append(changes, WatchChange{At: now.Unix(), Change: "added", Item: item, Summary: watchSummary("added", item, before)})
		}
	}
	for _, item := range before {
		if !is[item.key()] && !replaced(item, after) {
			changes = append(changes, WatchChange{At: now.Unix(), Change: "removed", Item: item, Summary: watchSummary("removed", item, after)})
		}
	}

	return changes
}

// replaced is whether a removed item is the old version of one still there —
// a cron file that was edited — which the "changed" summary already said.
func replaced(item WatchItem, after []WatchItem) bool {
	if item.Kind == "key" || item.Kind == "sudo" && !strings.HasPrefix(item.Name, "/") {
		return false
	}
	for _, other := range after {
		if other.Kind == item.Kind && other.Name == item.Name {
			return true
		}
	}
	return false
}

// watchSummary says the change in a sentence.
func watchSummary(change string, item WatchItem, other []WatchItem) string {
	existed := false
	for _, o := range other {
		if o.Kind == item.Kind && o.Name == item.Name {
			existed = true
		}
	}

	if change == "added" && existed && item.Kind != "key" && !(item.Kind == "sudo" && !strings.HasPrefix(item.Name, "/")) {
		switch item.Kind {
		case "cron":
			return "The scheduled jobs in " + item.Name + " were changed"
		case "sudo":
			return "The sudo rules in " + item.Name + " were changed"
		case "service":
			return "The service " + item.Name + " now points somewhere else"
		case "user":
			return "The account " + item.Name + " changed: " + item.Detail
		case "preload":
			return item.Name + " was changed — every program now loads a different library"
		}
	}

	verb := map[string]string{"added": "New", "removed": "Removed"}[change]

	switch item.Kind {
	case "user":
		if strings.Contains(item.Detail, "uid=0") && item.Name != "root" {
			return verb + " account with root's powers: " + item.Name
		}
		return verb + " account that can log in: " + item.Name + " (" + item.Detail + ")"
	case "sudo":
		if strings.HasPrefix(item.Name, "/") {
			return verb + " sudo rules file: " + item.Name
		}
		return map[string]string{"added": "Given sudo: ", "removed": "No longer has sudo: "}[change] + item.Name + " (" + item.Detail + ")"
	case "cron":
		return verb + " scheduled jobs: " + item.Name
	case "key":
		return verb + " SSH key for " + item.Name + " (" + item.Detail + ")"
	case "service":
		return verb + " system service: " + item.Name
	case "preload":
		return verb + " " + item.Name + " — every program on the server loads a library from it, the usual rootkit trick"
	}

	return verb + " " + item.Kind + ": " + item.Name
}

// WatchFingerprint is a short digest of a picture, to tell whether it moved.
func WatchFingerprint(items []WatchItem) string {
	sum := sha256.New()
	for _, item := range items {
		sum.Write([]byte(item.key() + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}
