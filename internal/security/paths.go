package security

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// The paths the scanner protection bans for, as the owner edits them on the
// Protection tab. Two lists:
//
//   - secret paths: one request is a ban, whatever the answer. Files that hold
//     passwords and keys, which no visitor, search engine or application ever
//     asks a web server for.
//   - scanner paths: counted when refused, not there, or redirected, and banned
//     after the jail's number of them. Files only something looking for a
//     weakness asks for, but that a real site might have.
//
// A pattern is plain text matched anywhere in a request's path — never the
// query string — ignoring case:
//
//	/.env          any path containing "/.env": /.env, /app/.env.bak
//	*              any run of characters: /wp-content/*/debug.log
//	$ at the end   the path ends there: .sql$
//
// A dot or a slash also matches its percent-encoded form (%2e, %2f), which is
// how scanners slip past rules that look for the plain one.

// DefaultSecretPaths and DefaultScanPaths are what the lists start as. They
// live in weblog, which flags the same requests on the Logs page and cannot
// import this package.
var (
	DefaultSecretPaths = weblog.DefaultSecretPaths
	DefaultScanPaths   = weblog.DefaultScanPaths
)

// Secrets is the secret list in force: the owner's, or the default.
func (p Protection) Secrets() []string {
	if p.SecretPaths == nil {
		return DefaultSecretPaths
	}
	return p.SecretPaths
}

// Scans is the scanner list in force: the owner's, or the default.
func (p Protection) Scans() []string {
	if p.ScanPaths == nil {
		return DefaultScanPaths
	}
	return p.ScanPaths
}

// ordinaryPaths are what real visitors and crawlers ask every site for. A
// pattern that matches one would ban them, and is refused.
var ordinaryPaths = []string{
	"/", "/index.php", "/index.html", "/wp-login.php", "/wp-admin/", "/wp-admin/admin-ajax.php",
	"/wp-json/wp/v2/posts", "/wp-content/themes/theme/style.css", "/wp-content/uploads/2026/01/photo.jpg",
	"/wp-includes/js/jquery/jquery.min.js", "/wp-cron.php", "/robots.txt", "/sitemap.xml",
	"/sitemap_index.xml", "/favicon.ico", "/apple-touch-icon.png", "/feed/", "/login", "/about/",
	"/blog/a-post/", "/shop/", "/cart/", "/checkout/", "/my-account/", "/api/users", "/assets/app.js",
	"/static/css/main.css", "/.well-known/acme-challenge/token", "/.well-known/security.txt",
	"/ads.txt", "/llms.txt", "/manifest.json", "/service-worker.js", "/search",
}

// pathPatternChars is what a pattern may hold: letters, digits and the
// punctuation a URL path uses. No spaces, quotes, ?, # or %, which either
// cannot be in a path or would mean something else in the files written.
var pathPatternChars = regexp.MustCompile(`^[A-Za-z0-9._~\-/*!+,;=:@&()\[\]{}'$]+$`)

// ValidatePathPattern is why a pattern cannot be used, or "".
func ValidatePathPattern(pattern string) string {
	if len(pattern) > 200 {
		return fmt.Sprintf("%.40q… is too long — keep a pattern under 200 characters.", pattern)
	}

	if !pathPatternChars.MatchString(pattern) {
		return fmt.Sprintf("%q has a character a path pattern cannot use (spaces, quotes, ?, #, %% and \\ are not allowed).", pattern)
	}

	if i := strings.IndexByte(pattern, '$'); i >= 0 && i != len(pattern)-1 {
		return fmt.Sprintf("%q: $ only goes at the very end, meaning the path ends there.", pattern)
	}

	literal := strings.NewReplacer("*", "", "$", "").Replace(pattern)
	if len(literal) < 3 {
		return fmt.Sprintf("%q is too broad — give at least three characters besides * and $.", pattern)
	}

	re := regexp.MustCompile(PathPatternRegexp(pattern))
	for _, path := range ordinaryPaths {
		if re.MatchString(path) {
			return fmt.Sprintf("%q matches %s, which every visitor or search engine asks for — it would ban them.", pattern, path)
		}
	}

	return ""
}

// CleanPathPatterns splits the text a form sends — one pattern a line — into
// patterns, without blanks or repeats, and says what is wrong with any.
func CleanPathPatterns(text string) ([]string, string) {
	patterns := []string{}
	seen := map[string]bool{}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[strings.ToLower(line)] {
			continue
		}

		if problem := ValidatePathPattern(line); problem != "" {
			return nil, problem
		}

		seen[strings.ToLower(line)] = true
		patterns = append(patterns, line)
	}

	if len(patterns) > 500 {
		return nil, "Keep the list under 500 patterns."
	}

	return patterns, ""
}

// SamePatterns is whether two lists hold the same patterns, in any order.
func SamePatterns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	set := map[string]bool{}
	for _, p := range a {
		set[strings.ToLower(p)] = true
	}
	for _, p := range b {
		if !set[strings.ToLower(p)] {
			return false
		}
	}

	return true
}

// PathPatternRegexp is a pattern as a Go regular expression for a path that
// is already lower case and percent-decoded, as weblog matches them.
func PathPatternRegexp(pattern string) string { return weblog.PatternRegexp(pattern) }

// fail2banPattern is a pattern as part of a fail2ban regular expression over
// the raw request line, from where the pattern starts in the path up to
// " HTTP": dots and slashes also match their percent-encoded forms, case is
// ignored, and % is written twice for the ConfigParser fail2ban reads its
// files with. The rest of the path, or with $ none of it, then the query
// string, are part of it — no lookahead, which Go's tests cannot run.
func fail2banPattern(pattern string) string {
	var re strings.Builder

	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")

	for _, r := range pattern {
		switch {
		case r == '.':
			re.WriteString(scanDot)
		case r == '/':
			re.WriteString(scanSlash)
		case r == '*':
			re.WriteString(`[^\s?]*`)
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			re.WriteString("[" + strings.ToLower(string(r)) + strings.ToUpper(string(r)) + "]")
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}

	if !anchored {
		re.WriteString(`[^\s?]*`)
	}
	re.WriteString(`(?:\?\S*)? HTTP`)

	return re.String()
}

// fail2banPatterns is the alternation of a list, for a failregex: the request
// line from the method to " HTTP", or "" for an empty list.
func fail2banPatterns(patterns []string) string {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if ValidatePathPattern(p) == "" {
			parts = append(parts, fail2banPattern(p))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return `(?:GET|POST|HEAD|PUT|OPTIONS) [^\s?]*(?:` + strings.Join(parts, "|") + `)`
}
