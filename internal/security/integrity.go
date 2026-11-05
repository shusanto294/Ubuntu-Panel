package security

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The code change monitor. A hacked site is nearly always a site whose PHP
// changed: a web shell added, an include slipped into index.php, a .htaccess
// that redirects visitors from search engines. The monitor lists each PHP
// site's code files — PHP, .htaccess, .user.ini — with their size and the
// time the kernel says they last changed (ctime, which `touch` cannot fake),
// and says what is new, changed or gone since the last look.
//
// It judges nothing. The malware scan it replaces (removed in 2.11.0)
// guessed which files looked malicious and was too often wrong; this only
// says what changed, which is always true, and leaves the judging to the
// owner. A deploy or restore from the panel is a change the panel made, and
// is never reported.

// codeFiles is the find expression for what counts as code.
const codeFiles = `\( -name '*.php' -o -name '*.php[0-9]' -o -name '*.phtml' -o -name '*.phar' -o -name '.htaccess' -o -name '.user.ini' \)`

// skippedDirs are directories whose files are expected to churn: caches,
// dependencies the panel installs, version control.
var skippedDirs = []string{"cache", "node_modules", ".git", "upgrade-temp-backup", "wflogs"}

// FileState is one code file as the monitor last saw it.
type FileState struct {
	Size  int64
	CTime string
}

// CodeSnapshot lists a site's code files, path relative to root.
func CodeSnapshot(root string) (map[string]FileState, error) {
	prune := make([]string, len(skippedDirs))
	for i, dir := range skippedDirs {
		prune[i] = "-name " + proc.Quote(dir)
	}

	command := fmt.Sprintf("sudo nice -n 19 ionice -c3 find %s -xdev \\( -type d \\( %s \\) -prune \\) -o -type f %s -printf '%%P\\t%%s\\t%%C@\\n' 2>/dev/null",
		proc.Quote(root), strings.Join(prune, " -o "), codeFiles)

	result := proc.Run(command, 10*time.Minute)
	if !proc.Exists(root, true) {
		return nil, fmt.Errorf("%s is not there", root)
	}

	return ParseSnapshot(result.Output), nil
}

// ParseSnapshot reads the find output above.
func ParseSnapshot(output string) map[string]FileState {
	files := map[string]FileState{}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] == "" {
			continue
		}

		var size int64
		fmt.Sscan(fields[1], &size)

		// Whole seconds: the fraction is noise between filesystems.
		ctime, _, _ := strings.Cut(fields[2], ".")
		files[fields[0]] = FileState{Size: size, CTime: ctime}
	}

	return files
}

// CodeChange is what changed in one site between two looks.
type CodeChange struct {
	At      int64    `json:"at"`
	Site    string   `json:"site"`
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`

	// InUploads are added or changed PHP files under an uploads directory,
	// where no real site keeps PHP: the strongest sign of a web shell.
	InUploads []string `json:"in_uploads"`
}

// Empty is whether nothing changed.
func (c CodeChange) Empty() bool {
	return len(c.Added) == 0 && len(c.Changed) == 0 && len(c.Removed) == 0
}

// DiffSnapshots compares two looks at one site.
func DiffSnapshots(site string, before, after map[string]FileState, now time.Time) CodeChange {
	change := CodeChange{At: now.Unix(), Site: site, Added: []string{}, Changed: []string{}, Removed: []string{}, InUploads: []string{}}

	for path, state := range after {
		old, existed := before[path]
		switch {
		case !existed:
			change.Added = append(change.Added, path)
		case old != state:
			change.Changed = append(change.Changed, path)
		default:
			continue
		}
		if strings.Contains("/"+path, "/uploads/") && strings.Contains(strings.ToLower(path), ".ph") {
			change.InUploads = append(change.InUploads, path)
		}
	}
	for path := range before {
		if _, still := after[path]; !still {
			change.Removed = append(change.Removed, path)
		}
	}

	sort.Strings(change.Added)
	sort.Strings(change.Changed)
	sort.Strings(change.Removed)
	sort.Strings(change.InUploads)

	return change
}

// EncodeSnapshot and DecodeSnapshot keep a look on disk, one file a line.
func EncodeSnapshot(files map[string]FileState) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var out strings.Builder
	for _, path := range paths {
		fmt.Fprintf(&out, "%s\t%d\t%s\n", path, files[path].Size, files[path].CTime)
	}
	return out.String()
}

func DecodeSnapshot(text string) map[string]FileState { return ParseSnapshot(text) }
