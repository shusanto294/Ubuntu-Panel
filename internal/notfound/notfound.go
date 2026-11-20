// Package notfound notices visitors who keep asking a site for pages that do
// not exist — the signature of a scanner walking a list of /wp-login.php,
// /.env and /phpmyadmin — so the panel can have Cloudflare challenge or block
// them.
//
// It is built for Cloudflare's free plan. Cloudflare's own rate limiting
// rules cannot count responses by status there (counting expressions are a
// Business feature), so the counting happens on this server instead: nginx
// writes one line to a log of its own for each 404 and nothing for any other
// response, the panel is woken by the kernel when that file grows, and what it
// does about an address is an IP access rule — which every plan has.
//
// An idle server costs nothing: no 404, no line, no wake-up.
package notfound

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// LogPath is where nginx writes the 404s. In the panel's own log
	// directory, out of reach of the logrotate rule for /var/log/nginx, which
	// would recreate it readable only by root; the panel keeps it small itself.
	LogPath = "/var/log/ubuntu-panel/404.log"

	// MapPath defines what the log line holds and which responses get one.
	// http level, so it is in conf.d.
	MapPath = "/etc/nginx/conf.d/panel-404.conf"

	// GuardPath is in the include every site's server blocks have, so every
	// site is covered, including ones made after this was switched on.
	GuardPath = "/etc/nginx/panel-guard/404-watch.conf"

	// Window is how long a run of 404s is counted over.
	Window = 10 * time.Minute
)

// MapConfig is the http-level half: a variable that is 1 for a 404 of a page,
// and a line with the address, the hostname, the path and the user agent.
//
// The path is $request_uri — what the visitor asked for. Not $uri: WordPress
// and Laravel hand every missing page to /index.php, and $uri is the path
// after that, so every 404 was logged as "/index.php" whatever was asked for.
// nginx escapes control characters in variables, so a tab cannot be forged.
//
// A missing image, stylesheet, script or font is not counted. A page that
// links to one makes every visitor who opens it ask for it, and challenging
// them all for the site's own mistake is the opposite of the point; scanners
// ask for .php, .env, /wp-admin and .git, which are all still counted. So is
// /.well-known/, which browsers and apps probe on their own.
func MapConfig() string {
	return "# Managed by Ubuntu Panel — 404 protection.\n" +
		"map $request_uri $panel_404_asset {\n" +
		"    ~*\\.(?:css|js|mjs|map|json|jpe?g|png|gif|webp|avif|svg|ico|bmp|woff2?|ttf|otf|eot|" +
		"mp4|webm|mp3|ogg|wav|txt|xml)(?:\\?|$) 1;\n" +
		"    ~^/\\.well-known/ 1;\n" +
		"    default 0;\n" +
		"}\n" +
		"\n" +
		"map \"$status:$panel_404_asset\" $panel_404 {\n" +
		"    \"404:0\" 1;\n" +
		"    default 0;\n" +
		"}\n" +
		"\n" +
		"log_format panel_404 '$remote_addr\\t$host\\t$request_uri\\t$http_user_agent';\n"
}

// GuardConfig is the server-level half. A second access_log beside the site's
// own adds to it rather than replacing it.
func GuardConfig() string {
	return "# Managed by Ubuntu Panel — 404 protection.\n" +
		"access_log " + LogPath + " panel_404 if=$panel_404;\n"
}

// Hit is one 404.
type Hit struct {
	IP    string
	Host  string
	Path  string
	Agent string
}

// ParseLine reads one line of the log.
func ParseLine(line string) (Hit, bool) {
	parts := strings.SplitN(strings.TrimRight(line, "\r\n"), "\t", 4)
	if len(parts) < 3 {
		return Hit{}, false
	}

	hit := Hit{IP: parts[0], Host: strings.ToLower(parts[1]), Path: parts[2]}
	if len(parts) == 4 {
		hit.Agent = parts[3]
	}

	if net.ParseIP(hit.IP) == nil || hit.Host == "" || hit.Host == "_" {
		return Hit{}, false
	}

	return hit, true
}

// Key is what a rule is made against: the address itself for IPv4, and the
// /64 for IPv6, which is what one customer of an IPv6 network is given — a
// rule for the one address is walked round by changing the last few digits.
func Key(ip string) (target, value string) {
	parsed := net.ParseIP(ip)

	if parsed.To4() != nil {
		return "ip", parsed.String()
	}

	network := &net.IPNet{IP: parsed.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}

	return "ip_range", network.String()
}

// Ignored is an address no rule should ever be made against: this machine,
// the private ranges, and Cloudflare's own — which is what nginx would log for
// a proxied request if the real-address configuration were missing.
func Ignored(ip string, own []string, cloudflare func(string) bool) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsUnspecified() ||
		parsed.IsLinkLocalUnicast() {
		return true
	}

	for _, address := range own {
		if address == ip {
			return true
		}
	}

	return cloudflare(ip)
}

// Counter counts each address's 404s per hostname over the window.
type Counter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

// Add records a 404 and answers how many that address has had on that host in
// the window, this one included.
func (c *Counter) Add(key, host string, at time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.seen == nil {
		c.seen = map[string][]time.Time{}
	}

	id := key + " " + host
	cutoff := at.Add(-Window)

	kept := c.seen[id][:0]
	for _, t := range c.seen[id] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	kept = append(kept, at)
	c.seen[id] = kept

	// Forget the addresses that have gone quiet, every so often, so a scan
	// from ten thousand addresses does not stay in memory.
	if len(c.seen) > 5000 {
		for k, times := range c.seen {
			if len(times) == 0 || !times[len(times)-1].After(cutoff) {
				delete(c.seen, k)
			}
		}
	}

	return len(kept)
}

// Forget drops an address's count, once something has been done about it.
func (c *Counter) Forget(key, host string) {
	c.mu.Lock()
	delete(c.seen, key+" "+host)
	c.mu.Unlock()
}
