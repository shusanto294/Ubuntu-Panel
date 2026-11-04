package security

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The malware scan looks through each site's files for the three things a
// compromised PHP site almost always has, and reports them for a person to
// look at. It deletes nothing: the same line that is a web shell in one site
// is an obfuscated licence check in a commercial plugin in another, and only
// the owner can tell which.
//
//   - WordPress files that differ from wordpress.org's, or should not exist
//     (wp-cli's checksum commands — core, and every plugin from the
//     directory);
//   - PHP files under wp-content/uploads, where WordPress only ever writes
//     images and documents;
//   - PHP that matches what web shells and injected backdoors are written
//     with: eval of decoded strings, a request parameter called as a
//     function, the names of the well-known shells.

// Finding is one thing worth looking at.
type Finding struct {
	Site   string `json:"site"`
	Kind   string `json:"kind"` // core | plugin | uploads | signature
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// FindingLabels are what the page calls each kind.
var FindingLabels = map[string]string{
	"core":      "WordPress core file changed or added",
	"plugin":    "Plugin file differs from wordpress.org",
	"uploads":   "PHP file in uploads",
	"signature": "Looks like a web shell or backdoor",
}

// ScanTarget is one site to scan.
type ScanTarget struct {
	Domain    string
	Root      string // where the files are
	WordPress bool
}

// signatures are extended regular expressions over PHP source. Each is the
// shape of something malicious rather than a function name: eval and
// base64_decode are both in honest code, eval(base64_decode( almost never is.
var signatures = []string{
	`(eval|assert)[[:space:]]*\([[:space:]]*(base64_decode|gzinflate|gzuncompress|gzdecode|str_rot13|strrev|hex2bin)[[:space:]]*\(`,
	`(eval|assert|system|passthru|shell_exec|exec|popen)[[:space:]]*\([[:space:]]*\$_(POST|GET|REQUEST|COOKIE|SERVER\[.HTTP_)`,
	`\$_(POST|GET|REQUEST|COOKIE)\[[^]]{1,40}\][[:space:]]*\(`,
	`preg_replace[[:space:]]*\([[:space:]]*['"][^'"]{0,80}/e['"]`,
	`(FilesMan|WSOsetcookie|c99shell|r57shell|b374k|Uname:.{0,40}php_uname|IndoXploit|xleet)`,
	`create_function[[:space:]]*\([^)]*\$_(POST|GET|REQUEST|COOKIE)`,
}

// nice runs a command at the lowest CPU and disk priority, so a scan on a
// busy machine waits for the visitors rather than the other way round.
const nice = "sudo nice -n 19 ionice -c3 "

// ScanSite scans one site.
func ScanSite(target ScanTarget, wpCLI bool) ([]Finding, error) {
	root := strings.TrimRight(target.Root, "/")
	if root == "" || !proc.Exists(root, true) {
		return nil, fmt.Errorf("%s: %s does not exist", target.Domain, root)
	}

	findings := []Finding{}
	add := func(kind, path, detail string) {
		path = strings.TrimPrefix(strings.TrimSpace(path), root+"/")
		if path == "" {
			return
		}
		findings = append(findings, Finding{Site: target.Domain, Kind: kind, Path: path, Detail: detail})
	}

	if target.WordPress {
		uploads := root + "/wp-content/uploads"

		output := proc.Run(fmt.Sprintf(nice+"find %s -type f \\( -iname '*.php' -o -iname '*.php[0-9]' "+
			"-o -iname '*.phtml' -o -iname '*.phar' \\) 2>/dev/null | head -n 200", proc.Quote(uploads)),
			10*time.Minute).Output

		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "/") {
				add("uploads", line, "WordPress never writes PHP into uploads; something uploaded this.")
			}
		}

		if wpCLI {
			wp := "sudo -u www-data nice -n 19 wp --path=" + proc.Quote(root) + " --skip-plugins --skip-themes"

			core := proc.Run(wp+" core verify-checksums 2>&1", 5*time.Minute).Output
			for _, line := range strings.Split(core, "\n") {
				if path, ok := cutAfter(line, "File doesn't verify against checksum: "); ok {
					add("core", path, "Differs from the same version on wordpress.org.")
				} else if path, ok := cutAfter(line, "File should not exist: "); ok {
					add("core", path, "Not part of WordPress, in a WordPress directory.")
				}
			}

			plugins := proc.Run(wp+" plugin verify-checksums --all --format=json 2>/dev/null", 10*time.Minute).Output
			for _, line := range strings.Split(plugins, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "[") {
					continue
				}

				var rows []struct {
					Plugin  string `json:"plugin_name"`
					File    string `json:"file"`
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(line), &rows) != nil {
					continue
				}

				for _, row := range rows {
					add("plugin", "wp-content/plugins/"+row.Plugin+"/"+row.File, row.Message)
				}
			}
		}
	}

	expressions := make([]string, 0, len(signatures))
	for _, signature := range signatures {
		expressions = append(expressions, "-e "+proc.Quote(signature))
	}

	output := proc.Run(nice+"grep -rlIEs --include='*.php' --include='*.phtml' --include='*.inc' "+
		"--exclude-dir=node_modules --exclude-dir=.git --exclude-dir=vendor "+
		strings.Join(expressions, " ")+" "+proc.Quote(root)+" 2>/dev/null | head -n 200", 30*time.Minute).Output

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "/") {
			add("signature", line, "Contains code shaped like a web shell or injected backdoor. Review it.")
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		return findings[i].Path < findings[j].Path
	})

	return findings, nil
}

func cutAfter(line, marker string) (string, bool) {
	_, after, ok := strings.Cut(line, marker)
	return strings.TrimSpace(after), ok
}
