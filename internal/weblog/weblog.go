// Package weblog reads nginx access logs and says what they add up to: which
// site the requests went to, who sent them, what they asked for, and which of
// them look like an attack rather than a visitor.
//
// The question it is built for is the one asked when the CPU is pinned: what
// is all this traffic, and where is it going? A static file costs nginx almost
// nothing; a request that reaches PHP costs a worker for as long as the page
// takes to build. So every request is counted twice — as a request, and as a
// dynamic one or not — and the dynamic count is what points at the site
// keeping PHP busy.
package weblog

import (
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Entry is one line of an access log in nginx's default `combined` format:
//
//	1.2.3.4 - user [04/Nov/2026:10:00:00 +0000] "GET /p?q HTTP/1.1" 200 512 "ref" "agent"
type Entry struct {
	IP        string
	Time      time.Time
	Method    string
	Path      string // without the query string
	Status    int
	Bytes     int64
	UserAgent string
}

const timeLayout = "02/Jan/2006:15:04:05 -0700"

// Parse reads one combined-format line. Lines it cannot read — a different
// format, a line cut short by rotation — are reported as not ok and skipped.
func Parse(line string) (Entry, bool) {
	var entry Entry

	ip, rest, ok := strings.Cut(line, " ")
	if !ok || net.ParseIP(ip) == nil {
		return entry, false
	}
	entry.IP = ip

	open := strings.IndexByte(rest, '[')
	closing := strings.IndexByte(rest, ']')
	if open < 0 || closing < open {
		return entry, false
	}

	stamp, err := time.Parse(timeLayout, rest[open+1:closing])
	if err != nil {
		return entry, false
	}
	entry.Time = stamp

	rest = strings.TrimLeft(rest[closing+1:], " ")

	request, rest, ok := quoted(rest)
	if !ok {
		return entry, false
	}

	// "GET /path HTTP/1.1"; a malformed request is logged as whatever the
	// client sent, often a lone "-" or binary junk from a TLS probe.
	fields := strings.Fields(request)
	switch {
	case len(fields) >= 2:
		entry.Method, entry.Path = fields[0], fields[1]
	case len(fields) == 1:
		entry.Method, entry.Path = "-", fields[0]
	default:
		entry.Method, entry.Path = "-", "-"
	}

	if query := strings.IndexByte(entry.Path, '?'); query >= 0 {
		entry.Path = entry.Path[:query]
	}

	numbers := strings.Fields(rest)
	if len(numbers) < 2 {
		return entry, false
	}

	entry.Status, err = strconv.Atoi(numbers[0])
	if err != nil {
		return entry, false
	}
	entry.Bytes, _ = strconv.ParseInt(numbers[1], 10, 64)

	// Referrer, then user agent. Both optional, so a line that stops after
	// the byte count is still a request.
	rest = strings.TrimLeft(rest, " ")
	if i := strings.IndexByte(rest, '"'); i >= 0 {
		if _, after, ok := quoted(rest[i:]); ok {
			after = strings.TrimLeft(after, " ")
			if agent, _, ok := quoted(after); ok {
				entry.UserAgent = agent
			}
		}
	}

	return entry, true
}

// quoted takes a "…" off the front of text. nginx escapes a quote inside a
// field as \x22, so the next bare quote is always the end of it.
func quoted(text string) (value, rest string, ok bool) {
	if !strings.HasPrefix(text, `"`) {
		return "", text, false
	}

	end := strings.IndexByte(text[1:], '"')
	if end < 0 {
		return "", text, false
	}

	return text[1 : end+1], text[end+2:], true
}

// ------------------------------------------------------------- classifying

var staticExtensions = map[string]bool{
	".css": true, ".js": true, ".mjs": true, ".map": true, ".json": false,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true,
	".svg": true, ".ico": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".ogg": true, ".wav": true,
	".pdf": true, ".zip": true, ".gz": true, ".txt": true, ".xml": false,
}

// Static reports whether a path is a file nginx hands out by itself, without
// waking PHP or the application behind the site.
func Static(p string) bool {
	return staticExtensions[strings.ToLower(path.Ext(p))]
}

// Threat kinds, as the page names them.
const (
	ThreatWPLogin = "wp-login"
	ThreatXMLRPC  = "xmlrpc"
	ThreatLogin   = "login"
	ThreatProbe   = "probe"
	ThreatSecret  = "secret"
)

// ThreatLabels are what the page calls each kind.
var ThreatLabels = map[string]string{
	ThreatWPLogin: "WordPress login attempts",
	ThreatXMLRPC:  "XML-RPC calls",
	ThreatLogin:   "Login form posts",
	ThreatProbe:   "Vulnerability scanning",
	ThreatSecret:  "Asking for secret files",
}

// The paths that mark a request as an attack are the scanner protection's
// lists, which the owner edits on the Protection tab (security/paths.go):
// secret paths, one request for which is an attacker whatever the answer, and
// scanner paths, which count as probing when refused, missing or redirected.
// The panel hands them over with SetPaths; until it does, the defaults apply.
type pathRules struct {
	secrets []*regexp.Regexp
	probes  []*regexp.Regexp
}

var rules atomic.Pointer[pathRules]

// SetPaths is the lists in force, each pattern already a regular expression
// for a lower-case, percent-decoded path (security.PathPatternRegexp).
func SetPaths(secrets, probes []string) {
	compile := func(list []string) []*regexp.Regexp {
		out := make([]*regexp.Regexp, 0, len(list))
		for _, expression := range list {
			if re, err := regexp.Compile(expression); err == nil {
				out = append(out, re)
			}
		}
		return out
	}

	rules.Store(&pathRules{secrets: compile(secrets), probes: compile(probes)})
}

func currentRules() *pathRules {
	if r := rules.Load(); r != nil {
		return r
	}
	SetPaths(patternRegexps(DefaultSecretPaths), patternRegexps(DefaultScanPaths))
	return rules.Load()
}

func patternRegexps(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, PatternRegexp(p))
	}
	return out
}

// publicFiles are .txt and .xml files a site is asked for by search engines,
// ad networks and feed readers whether or not it has them. Missing, they are
// not a probe.
var publicFiles = []string{
	"robots.txt", "ads.txt", "app-ads.txt", "security.txt", "humans.txt", "llms.txt", "llms-full.txt",
	"crossdomain.xml", "browserconfig.xml", "bingsiteauth.xml", "opensearch.xml", "wlwmanifest.xml",
	// Outlook asks every mail domain's website for this one.
	"autodiscover.xml",
}

// MissingScript is whether a request for an .xml or .txt file that was not
// there or was refused looks like probing: a script guessing at files a site
// might have. The ones every crawler asks for — robots.txt, sitemaps, feeds,
// ads.txt — are not, and neither is a useful bot asking. A missing .php is not
// counted: people follow old links to pages that moved.
func MissingScript(entry Entry) bool {
	if entry.Status != 403 && entry.Status != 404 {
		return false
	}

	p := decodePath(strings.ToLower(entry.Path))
	name := p[strings.LastIndexByte(p, '/')+1:]

	if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".txt") {
		return false
	}

	if FindGoodBot(entry.UserAgent) != nil || strings.Contains(p, "/.well-known/") {
		return false
	}

	for _, public := range publicFiles {
		if name == public {
			return false
		}
	}

	for _, word := range []string{"sitemap", "feed", "rss", "atom"} {
		if strings.Contains(name, word) {
			return false
		}
	}

	return true
}

// decodePath undoes the percent-encoding scanners use to slip past patterns
// that look for a plain dot or slash: /%2eenv, /%2f%2eaws%2fcredentials.
func decodePath(path string) string {
	return strings.NewReplacer("%2e", ".", "%2f", "/", "%5c", "/", `\`, "/").Replace(path)
}

// IsSecret is whether a request asked for a secret file.
func IsSecret(path string) bool {
	p := decodePath(strings.ToLower(path))

	for _, re := range currentRules().secrets {
		if re.MatchString(p) {
			return true
		}
	}

	return false
}

// probes are paths nobody visits except something looking for a weakness — a
// forgotten admin tool, a known exploitable appliance — beyond the scanner
// list the owner edits. Each is specific enough that a real page does not
// contain it: no "/shell" that would catch /shellfish, no /admin.php or
// /install.php that a real application might redirect to its login, no
// /autodiscover, which Outlook asks every mail domain's website for.
var probes = []string{
	"/phpmyadmin", "/pma/", "/myadmin/", "/adminer.php",
	"/webshell", "/jenkins/", "/owa/auth", "/remote/login", "/geoserver/",
	"/debug/default", "/.well-known/security.txt.php", "/wp-content/plugins/wp-file-manager/",
	"/xmlrpc.php.", "/cdn-cgi/trace.php", "/backup.sql", "/dump.sql", "/database.sql",
}

// Threat classifies a request as one of the kinds above, or "" for one that
// looks like an ordinary visitor.
//
// A request for one of the probe paths only counts when it was refused or
// not there. Some of them are real on this machine — the panel serves
// phpMyAdmin at /phpmyadmin — and the owner using one is answered with a
// page, where a scanner guessing at it gets a 404.
func Threat(entry Entry) string {
	p := strings.ToLower(entry.Path)

	if IsSecret(p) {
		return ThreatSecret
	}

	switch {
	case p == "/wp-login.php" && entry.Method == "POST":
		return ThreatWPLogin
	case p == "/xmlrpc.php":
		return ThreatXMLRPC
	case strings.Contains(p, "../") || strings.Contains(p, "%2e%2e") ||
		strings.Contains(p, "union%20select") || strings.Contains(p, "<script"):
		return ThreatProbe
	}

	// 3xx as well: a scanner sweeping http:// is answered with a redirect to
	// https:// it never follows. Not 304, which is a browser checking a copy
	// of a file it already has.
	if entry.Status == 0 || entry.Status >= 300 && entry.Status != 304 {
		p = decodePath(p)
		for _, probe := range probes {
			if strings.Contains(p, probe) {
				return ThreatProbe
			}
		}
		for _, re := range currentRules().probes {
			if re.MatchString(p) {
				return ThreatProbe
			}
		}
	}

	if MissingScript(entry) {
		return ThreatProbe
	}

	if entry.Method == "POST" {
		for _, login := range []string{"/login", "/signin", "/sign-in", "/user/login",
			"/administrator", "/admin/login", "/auth/login", "/wp-admin/admin-post.php"} {
			if p == login || strings.HasSuffix(p, login) {
				return ThreatLogin
			}
		}
	}

	return ""
}

// botMarkers are words a crawler or script puts in its user agent.
var botMarkers = []string{"bot", "crawl", "spider", "slurp", "curl", "wget", "python",
	"go-http", "java/", "okhttp", "libwww", "scrapy", "httpclient", "headless",
	"zgrab", "masscan", "nmap", "nikto", "sqlmap", "wpscan", "axios", "node-fetch"}

// Bot reports whether a user agent says it is not a person's browser. An
// empty one counts: every browser sends one.
func Bot(agent string) bool {
	if agent == "" || agent == "-" {
		return true
	}

	lower := strings.ToLower(agent)
	for _, marker := range botMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	return false
}

// --------------------------------------------------------------- adding up

// Analyzer adds entries up. Not safe for concurrent use: give each goroutine
// its own and Merge them, or lock around Add.
type Analyzer struct {
	From, To time.Time
	buckets  int
	width    time.Duration

	// Trusted are addresses whose requests are never read as an attack: the
	// owner's, the never-ban list's, the server's own.
	Trusted map[string]bool

	// Proxied are the sites whose visitors arrive through Cloudflare. What an
	// address does there that only looks like an attack — many requests a
	// minute, many pages not found — is left to Cloudflare, which sees every
	// site it fronts and has far better ways to tell a visitor from a bot.
	Proxied map[string]bool

	sites    map[string]*siteTotals
	ips      map[string]*ipTotals
	paths    map[string]*pathTotals
	agents   map[string]int
	threats  map[string]*threatTotals
	timeline []Bucket
	statuses map[string]int

	total, dynamic, static, bytes int
	scanned, skipped              int
}

type siteTotals struct {
	requests, dynamic, bytes int
	status                   [6]int
	threats                  int
	ips                      map[string]bool
	minutes                  map[int64]int
}

type ipTotals struct {
	requests, dynamic, notFound, errors int
	proxied                             int
	sites                               map[string]int
	paths                               map[string]int
	threats                             map[string]int
	agent                               string
	minutes                             map[int64]int
	last                                time.Time
}

type pathTotals struct {
	site, path string
	requests   int
	notFound   int
	errors     int
	dynamic    bool
	threat     string
	ips        map[string]bool
}

type threatTotals struct {
	requests int
	ips      map[string]int
	sites    map[string]int
}

// Bucket is one slice of the window on the timeline.
type Bucket struct {
	At       int64 `json:"at"` // Unix seconds at its start
	Requests int   `json:"requests"`
	Dynamic  int   `json:"dynamic"`
	Threats  int   `json:"threats"`
}

// New is an analyzer that counts what falls between from and to, in buckets
// slices for the timeline.
func New(from, to time.Time, buckets int) *Analyzer {
	if buckets < 1 {
		buckets = 1
	}

	a := &Analyzer{
		From: from, To: to, buckets: buckets,
		width:    to.Sub(from) / time.Duration(buckets),
		sites:    map[string]*siteTotals{},
		ips:      map[string]*ipTotals{},
		paths:    map[string]*pathTotals{},
		agents:   map[string]int{},
		threats:  map[string]*threatTotals{},
		statuses: map[string]int{},
		timeline: make([]Bucket, buckets),
	}

	if a.width <= 0 {
		a.width = time.Minute
	}

	for i := range a.timeline {
		a.timeline[i].At = from.Add(time.Duration(i) * a.width).Unix()
	}

	return a
}

// Line parses and adds one log line for a site.
func (a *Analyzer) Line(site, line string) {
	a.scanned++

	entry, ok := Parse(line)
	if !ok {
		a.skipped++
		return
	}

	a.Add(site, entry)
}

// Add counts one request to a site, if it falls inside the window.
func (a *Analyzer) Add(site string, entry Entry) {
	if entry.Time.Before(a.From) || entry.Time.After(a.To) {
		return
	}

	dynamic := !Static(entry.Path) && entry.Status != 304
	threat := Threat(entry)
	if a.Trusted[entry.IP] {
		threat = ""
	}
	class := entry.Status / 100
	if class < 1 || class > 5 {
		class = 0
	}
	minute := entry.Time.Unix() / 60

	a.total++
	a.bytes += int(entry.Bytes)
	if dynamic {
		a.dynamic++
	} else {
		a.static++
	}
	a.statuses[strconv.Itoa(class)+"xx"]++

	// The window's last instant belongs to its last bucket.
	if i := min(int(entry.Time.Sub(a.From)/a.width), len(a.timeline)-1); i >= 0 {
		a.timeline[i].Requests++
		if dynamic {
			a.timeline[i].Dynamic++
		}
		if threat != "" {
			a.timeline[i].Threats++
		}
	}

	s := a.sites[site]
	if s == nil {
		s = &siteTotals{ips: map[string]bool{}, minutes: map[int64]int{}}
		a.sites[site] = s
	}
	s.requests++
	s.bytes += int(entry.Bytes)
	s.status[class]++
	s.ips[entry.IP] = true
	s.minutes[minute]++
	if dynamic {
		s.dynamic++
	}
	if threat != "" {
		s.threats++
	}

	ip := a.ips[entry.IP]
	if ip == nil && len(a.ips) >= maxAddresses {
		// Past the cap an address is counted in the totals but not followed
		// on its own: a month of a botnet is millions of addresses, and each
		// one followed is a few maps of memory.
		ip = a.overflow()
	} else if ip == nil {
		ip = &ipTotals{sites: map[string]int{}, paths: map[string]int{},
			threats: map[string]int{}, minutes: map[int64]int{}}
		a.ips[entry.IP] = ip
	}
	ip.requests++
	if a.Proxied[site] {
		ip.proxied++
	}
	ip.sites[site]++
	ip.minutes[minute]++
	if len(ip.paths) < 500 || ip.paths[entry.Path] > 0 {
		ip.paths[entry.Path]++
	}
	if dynamic {
		ip.dynamic++
	}
	if entry.Status == 404 {
		ip.notFound++
	}
	if entry.Status >= 400 {
		ip.errors++
	}
	if threat != "" {
		ip.threats[threat]++
	}
	if entry.Time.After(ip.last) {
		ip.last = entry.Time
		ip.agent = entry.UserAgent
	}

	// Paths are capped so a scanner trying a hundred thousand distinct URLs
	// cannot make the report as large as the log; past the cap only paths
	// already seen are counted.
	key := site + " " + entry.Path
	p := a.paths[key]
	if p == nil && len(a.paths) < 20000 {
		p = &pathTotals{site: site, path: entry.Path, dynamic: dynamic, ips: map[string]bool{}}
		a.paths[key] = p
	}
	if p != nil {
		p.requests++
		if threat != "" {
			p.threat = threat
		}
		if entry.Status == 404 {
			p.notFound++
		}
		if entry.Status >= 500 {
			p.errors++
		}
		if len(p.ips) < 1000 {
			p.ips[entry.IP] = true
		}
	}

	if len(a.agents) < 5000 || a.agents[entry.UserAgent] > 0 {
		a.agents[entry.UserAgent]++
	}

	if threat != "" {
		t := a.threats[threat]
		if t == nil {
			t = &threatTotals{ips: map[string]int{}, sites: map[string]int{}}
			a.threats[threat] = t
		}
		t.requests++
		t.ips[entry.IP]++
		t.sites[site]++
	}
}

// maxAddresses is how many addresses are followed one by one.
const maxAddresses = 50000

// overflowAddress is what the addresses past the cap are counted as.
const overflowAddress = "(other addresses)"

func (a *Analyzer) overflow() *ipTotals {
	ip := a.ips[overflowAddress]
	if ip == nil {
		ip = &ipTotals{sites: map[string]int{}, paths: map[string]int{},
			threats: map[string]int{}, minutes: map[int64]int{}}
		a.ips[overflowAddress] = ip
	}
	return ip
}

// ------------------------------------------------------------------ report

// Report is what the page shows.
type Report struct {
	From     int64 `json:"from"`
	To       int64 `json:"to"`
	Requests int   `json:"requests"`
	Dynamic  int   `json:"dynamic"`
	Static   int   `json:"static"`
	Bytes    int   `json:"bytes"`
	IPs      int   `json:"ips"`
	Scanned  int   `json:"scanned"`
	Skipped  int   `json:"skipped"`

	Statuses map[string]int `json:"statuses"`
	Timeline []Bucket       `json:"timeline"`
	Sites    []SiteStat     `json:"sites"`
	TopIPs   []IPStat       `json:"top_ips"`
	Paths    []PathStat     `json:"paths"`
	Threats  []ThreatStat   `json:"threats"`
	Agents   []AgentStat    `json:"agents"`
}

type SiteStat struct {
	Domain     string  `json:"domain"`
	Requests   int     `json:"requests"`
	Dynamic    int     `json:"dynamic"`
	Share      float64 `json:"share"`         // of all requests, percent
	PHPShare   float64 `json:"dynamic_share"` // of all dynamic requests, percent
	Bytes      int     `json:"bytes"`
	IPs        int     `json:"ips"`
	Success    int     `json:"success"` // 2xx and 3xx
	NotFound   int     `json:"client_errors"`
	Errors     int     `json:"server_errors"`
	Threats    int     `json:"threats"`
	PeakMinute int     `json:"peak_per_minute"`
}

type IPStat struct {
	IP       string `json:"ip"`
	Requests int    `json:"requests"`
	Dynamic  int    `json:"dynamic"`
	NotFound int    `json:"not_found"`
	Errors   int    `json:"errors"`

	// Proxied is how many of the requests went to a site behind Cloudflare.
	Proxied    int            `json:"proxied"`
	PeakMinute int            `json:"peak_per_minute"`
	Sites      []Count        `json:"sites"`
	Paths      []Count        `json:"paths"`
	Threats    map[string]int `json:"threats"`
	Agent      string         `json:"agent"`
	Bot        bool           `json:"bot"`

	// GoodBot is the useful bot the user agent says this is — a search
	// engine, an AI crawler, a link preview, an uptime monitor — or nil.
	GoodBot *GoodBot `json:"good_bot,omitempty"`
	Last    int64    `json:"last"`

	// Verdict is the one-line reading of this address: "" for nothing
	// remarkable, or why it stands out.
	Verdict string `json:"verdict"`
	Danger  bool   `json:"danger"`

	// Attack is what kind of attack a dangerous address is making — "login"
	// (brute force), "scan" (vulnerable files, or crawling for missing ones)
	// or "flood" — which decides the protection that bans it.
	Attack string `json:"attack,omitempty"`

	// Trusted says why this address is never flagged or banned: "you", "the
	// never-ban list", "signed in to the panel", "this server". Set by the
	// page, which knows who is looking.
	Trusted string `json:"trusted,omitempty"`
}

type PathStat struct {
	Site     string `json:"site"`
	Path     string `json:"path"`
	Requests int    `json:"requests"`
	IPs      int    `json:"ips"`
	NotFound int    `json:"not_found"`
	Errors   int    `json:"errors"`
	Dynamic  bool   `json:"dynamic"`
	Threat   string `json:"threat"`
}

type ThreatStat struct {
	Kind     string  `json:"kind"`
	Label    string  `json:"label"`
	Requests int     `json:"requests"`
	IPs      int     `json:"ips"`
	TopIPs   []Count `json:"top_ips"`
	Sites    []Count `json:"sites"`
}

type AgentStat struct {
	Agent    string `json:"agent"`
	Requests int    `json:"requests"`
	Bot      bool   `json:"bot"`
}

// Count is a name and how many times it came up.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func top(counts map[string]int, n int) []Count {
	list := make([]Count, 0, len(counts))
	for name, count := range counts {
		list = append(list, Count{name, count})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Name < list[j].Name
	})

	if len(list) > n {
		list = list[:n]
	}

	return list
}

func peak(minutes map[int64]int) int {
	highest := 0
	for _, count := range minutes {
		highest = max(highest, count)
	}
	return highest
}

func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(int(float64(part)/float64(whole)*1000+0.5)) / 10
}

// Report is the totals, with lists cut to what a page can show.
func (a *Analyzer) Report() Report {
	report := Report{
		From: a.From.Unix(), To: a.To.Unix(),
		Requests: a.total, Dynamic: a.dynamic, Static: a.static, Bytes: a.bytes,
		IPs: len(a.ips), Scanned: a.scanned, Skipped: a.skipped,
		Statuses: a.statuses, Timeline: a.timeline,
		Sites: []SiteStat{}, TopIPs: []IPStat{}, Paths: []PathStat{},
		Threats: []ThreatStat{}, Agents: []AgentStat{},
	}

	for domain, s := range a.sites {
		report.Sites = append(report.Sites, SiteStat{
			Domain: domain, Requests: s.requests, Dynamic: s.dynamic,
			Share: percent(s.requests, a.total), PHPShare: percent(s.dynamic, a.dynamic),
			Bytes: s.bytes, IPs: len(s.ips),
			Success: s.status[2] + s.status[3], NotFound: s.status[4], Errors: s.status[5],
			Threats: s.threats, PeakMinute: peak(s.minutes),
		})
	}
	sort.Slice(report.Sites, func(i, j int) bool {
		if report.Sites[i].Dynamic != report.Sites[j].Dynamic {
			return report.Sites[i].Dynamic > report.Sites[j].Dynamic
		}
		return report.Sites[i].Requests > report.Sites[j].Requests
	})

	minutes := max(1, int(a.To.Sub(a.From)/time.Minute))

	for address, ip := range a.ips {
		stat := IPStat{
			IP: address, Requests: ip.requests, Dynamic: ip.dynamic,
			NotFound: ip.notFound, Errors: ip.errors, Proxied: ip.proxied, PeakMinute: peak(ip.minutes),
			Sites: top(ip.sites, 3), Paths: top(ip.paths, 5), Threats: ip.threats,
			Agent: ip.agent, Bot: Bot(ip.agent), GoodBot: FindGoodBot(ip.agent), Last: ip.last.Unix(),
		}
		if address != overflowAddress && !a.Trusted[address] {
			stat.Verdict, stat.Danger = verdict(stat, minutes)
			if stat.Danger {
				stat.Attack = attackKind(stat)
			}
		}
		report.TopIPs = append(report.TopIPs, stat)
	}
	SortIPs(report.TopIPs)
	if len(report.TopIPs) > 200 {
		report.TopIPs = report.TopIPs[:200]
	}

	for _, p := range a.paths {
		report.Paths = append(report.Paths, PathStat{
			Site: p.site, Path: p.path, Requests: p.requests, IPs: len(p.ips),
			NotFound: p.notFound, Errors: p.errors, Dynamic: p.dynamic,
			Threat: p.threat,
		})
	}
	sort.Slice(report.Paths, func(i, j int) bool {
		if report.Paths[i].Requests != report.Paths[j].Requests {
			return report.Paths[i].Requests > report.Paths[j].Requests
		}
		return report.Paths[i].Site+report.Paths[i].Path < report.Paths[j].Site+report.Paths[j].Path
	})
	if len(report.Paths) > 200 {
		report.Paths = report.Paths[:200]
	}

	for kind, t := range a.threats {
		report.Threats = append(report.Threats, ThreatStat{
			Kind: kind, Label: ThreatLabels[kind], Requests: t.requests, IPs: len(t.ips),
			TopIPs: top(t.ips, 50), Sites: top(t.sites, 10),
		})
	}
	sort.Slice(report.Threats, func(i, j int) bool {
		return report.Threats[i].Requests > report.Threats[j].Requests
	})

	for _, c := range top(a.agents, 100) {
		report.Agents = append(report.Agents, AgentStat{Agent: c.Name, Requests: c.Count, Bot: Bot(c.Name)})
	}

	return report
}

// SortIPs puts the addresses worth acting on first, then the busiest.
func SortIPs(list []IPStat) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Danger != list[j].Danger {
			return list[i].Danger
		}
		if list[i].Requests != list[j].Requests {
			return list[i].Requests > list[j].Requests
		}
		return list[i].IP < list[j].IP
	})
}

// verdict reads one address: whether it is doing something worth banning it
// for, and what. The thresholds are deliberately high — a busy visitor on a
// slow connection reloading a page is not an attack.
// attackKind is which of the protections a dangerous address's verdict
// belongs to, in the order verdict tries its rules.
func attackKind(ip IPStat) string {
	switch {
	case ip.Threats[ThreatWPLogin] >= 10, ip.Threats[ThreatXMLRPC] >= 10, ip.Threats[ThreatLogin] >= 10:
		return "login"
	case ip.Threats[ThreatSecret] >= 1, ip.Threats[ThreatProbe] >= 5:
		return "scan"
	case ip.NotFound >= 50 && ip.NotFound*2 >= ip.Requests:
		return "scan"
	default:
		return "flood"
	}
}

// Reassess judges an address again without its user agent's claim to be a
// useful bot: one whose DNS says it is not the search engine it calls itself.
// minutes is the length of the window the counts are over.
func Reassess(ip IPStat, minutes int) IPStat {
	ip.GoodBot = nil
	ip.Verdict, ip.Danger = verdict(ip, max(1, minutes))
	ip.Attack = ""
	if ip.Danger {
		ip.Attack = attackKind(ip)
	}
	return ip
}

func verdict(ip IPStat, minutes int) (string, bool) {
	wp, rpc, login, probe := ip.Threats[ThreatWPLogin], ip.Threats[ThreatXMLRPC],
		ip.Threats[ThreatLogin], ip.Threats[ThreatProbe]
	secret := ip.Threats[ThreatSecret]

	switch {
	case wp >= 10:
		return strconv.Itoa(wp) + " WordPress login attempts — brute force", true
	case rpc >= 10:
		return strconv.Itoa(rpc) + " XML-RPC calls — brute force or pingback abuse", true
	case login >= 10:
		return strconv.Itoa(login) + " login form posts — brute force", true
	case secret >= 1:
		return strconv.Itoa(secret) + " requests for secret files (.env, .git, keys) — an attacker", true

	// A useful bot crawls, follows dead links — including the odd paths other
	// sites link to — and comes back often: none of the rules below is held
	// against it. The ones above still are — no search engine guesses
	// passwords or asks for .env.
	case ip.GoodBot != nil:
		return "", false

	case probe >= 5:
		return strconv.Itoa(probe) + " requests for known-vulnerable files — a scanner", true
	}

	// The rules above are what no visitor does. The ones below are guesses
	// from volume — a busy visitor on a slow page, an office behind one
	// address, a crawler nobody listed — and for an address that came mostly
	// through Cloudflare they are Cloudflare's call: said, never banned.
	if reason, danger := volumeVerdict(ip, minutes); reason != "" {
		if danger && ip.Proxied > 0 && ip.Proxied*2 >= ip.Requests {
			return reason + " — through Cloudflare, so left to Cloudflare", false
		}
		return reason, danger
	}

	if wp+rpc+login+probe+secret > 0 {
		return "Some suspicious requests", false
	}

	return "", false
}

// volumeVerdict is what verdict says from how much an address asked for.
func volumeVerdict(ip IPStat, minutes int) (string, bool) {
	switch {
	case ip.NotFound >= 50 && ip.NotFound*2 >= ip.Requests:
		return strconv.Itoa(ip.NotFound) + " not-found requests — crawling for files that are not there", true
	case ip.PeakMinute >= 120 && ip.Dynamic*2 >= ip.Requests:
		return strconv.Itoa(ip.PeakMinute) + " requests in one minute, most of them to PHP — a flood", true
	case ip.PeakMinute >= 300:
		return strconv.Itoa(ip.PeakMinute) + " requests in one minute", true
	case ip.Bot && ip.Dynamic >= 30*minutes:
		return "A bot, crawling dynamic pages heavily", false
	}

	return "", false
}
