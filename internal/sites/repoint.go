package sites

import (
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Re-pointing a copied site at its own address.
//
// This file is the reason cloning a site is not just copying a directory and a
// database. An application that stores absolute URLs — and WordPress stores
// them in a dozen places, some of them inside serialised PHP — comes up on the
// new hostname still convinced it lives at the old one. What that looks like is
// not a broken site: it is a site that loads, and then redirects the visitor
// back to production the moment they click anything. Which means the person who
// made the staging copy is now editing production without knowing it.
//
// So every copy, in both directions, ends with the application being told where
// it now is, and the database being rewritten to match.

// repointArgs is one direction of a copy: a site that has just received files
// and a database, and the address they came from.
type repointArgs struct {
	// Site is the site as it now is — the destination of the copy.
	Site *store.Site

	// FromURL is the URL the copied data believes it lives at, and ToURL is
	// the one it now does. Both include the scheme, because a production site
	// on HTTPS cloned to a staging site without a certificate has to have the
	// scheme rewritten too, or every asset on the page is blocked as mixed
	// content.
	FromURL string
	ToURL   string

	// Database is the destination's own database, which the copied
	// configuration files do not know about: they still name the source's.
	Database *store.Database

	// Staging is whether the destination is the staging half of the pair.
	// It decides the handful of things that should be true of a copy and must
	// not be true of a live site — chiefly that search engines are asked to
	// stay away.
	Staging bool
}

// Re-pointing is two jobs, and they happen at opposite ends of a copy.
//
// repointConfig has to run the moment the files are in place. Until it does,
// the copy's configuration names the *source's* database — so a staging site
// with a live vhost in front of it would read and write production's data,
// which is not a staging site at all. Nothing may serve a request between the
// copy and this.
//
// repointURLs has to run last, after the certificate step, because what it
// writes into the database is a scheme as well as a hostname. Run earlier it
// would write https:// into a site whose certificate then failed to issue, and
// every asset on every page would be an https URL that nothing answers.
//
// Doing both at once is what the panel did first, and it had to pick one end
// to be wrong at.

// repointConfig makes the copy read its own database rather than the one it was
// copied from, and takes the source's cache off it.
func (s *Service) repointConfig(runner *tasks.Runner, args repointArgs) (string, error) {
	switch args.Site.Type {
	case "wordpress":
		return s.repointWordPressConfig(runner, args)

	case "laravel":
		return s.repointLaravelConfig(runner, args)
	}

	return "This site type keeps no database credentials the panel writes, so there was " +
		"nothing to re-point.", nil
}

// repointURLs rewrites every stored address to the one the site now answers on.
//
// Returns a sentence for the console. A type the panel has nothing specific to
// do for says so rather than silently doing nothing: "nothing to rewrite" and
// "the rewrite did not run" look identical in a log otherwise.
func (s *Service) repointURLs(runner *tasks.Runner, args repointArgs) (string, error) {
	switch args.Site.Type {
	case "wordpress":
		return s.repointWordPress(runner, args)

	case "laravel":
		return s.repointLaravelURL(runner, args)
	}

	if args.Site.IsProxied() {
		return s.repointEnvFiles(runner, args)
	}

	return "Nothing in this site type stores its own address, so there was nothing " +
		"to rewrite. Anything your own code hardcodes is yours to change.", nil
}

// ------------------------------------------------------------- WordPress

// repointWordPress is the whole of making a cloned WordPress believe in its new
// address.
func (s *Service) repointWordPress(runner *tasks.Runner, args repointArgs) (string, error) {
	if !proc.Which("wp") {
		return "", fmt.Errorf("WP-CLI is not installed on this machine, and a WordPress " +
			"database cannot be rewritten without it")
	}

	root := args.Site.DocumentRoot()
	wp := "sudo -u www-data wp --path=" + proc.Quote(root) + " --skip-plugins --skip-themes"

	if !proc.Ok("sudo test -f " + proc.Quote(root+"/wp-config.php")) {
		return "", fmt.Errorf("there is no wp-config.php in %s, so this is not a "+
			"WordPress installation the panel can rewrite", root)
	}

	var log strings.Builder

	// Whether this is a network, which changes what has to be rewritten.
	network := proc.Ok(fmt.Sprintf("%s config has MULTISITE --type=constant 2>/dev/null && "+
		"%s config get MULTISITE --type=constant 2>/dev/null | grep -qi '^1\\|^true'", wp, wp))

	// The URLs themselves.
	changed, err := s.searchReplaceWordPress(runner, args, wp, network)
	if err != nil {
		return "", err
	}

	log.WriteString(changed + "\n")

	// The two options, said plainly.
	//
	// The search and replace above has already done these — they are rows in
	// wp_options like any other. Setting them explicitly is the backstop for
	// the case the replacement cannot cover: a home or siteurl that was not
	// the source's own URL to begin with, which is what a site previously
	// moved by hand looks like. A WordPress whose siteurl is anything but its
	// own address redirects every request away from itself.
	if !network {
		runner.Exec(fmt.Sprintf("%s option update home %s; %s option update siteurl %s",
			wp, proc.Quote(args.ToURL), wp, proc.Quote(args.ToURL)), 2*time.Minute)

		fmt.Fprintf(&log, "home and siteurl set to %s.\n", args.ToURL)
	}

	// The constants, if the installation uses them.
	//
	// WP_HOME and WP_SITEURL in wp-config.php override the options entirely,
	// so a site that defines them ignores step 5 completely. Updated only
	// where they already exist: adding them to an installation that does not
	// use them pins the URL in a file, which is a surprise the next time
	// somebody moves the site.
	for _, constant := range []string{"WP_HOME", "WP_SITEURL"} {
		if proc.Ok(fmt.Sprintf("%s config has %s --type=constant 2>/dev/null", wp, constant)) {
			runner.Exec(fmt.Sprintf("%s config set %s %s --type=constant",
				wp, constant, proc.Quote(args.ToURL)), time.Minute)

			fmt.Fprintf(&log, "%s in wp-config.php set to %s.\n", constant, args.ToURL)
		}
	}

	if network {
		// DOMAIN_CURRENT_SITE is the network's own idea of where it is, and it
		// is a constant rather than a row, so the replacement above cannot
		// have touched it.
		runner.Exec(fmt.Sprintf("%s config set DOMAIN_CURRENT_SITE %s --type=constant",
			wp, proc.Quote(args.Site.Domain)), time.Minute)

		log.WriteString("This is a multisite network: DOMAIN_CURRENT_SITE was set to " +
			args.Site.Domain + " and every table was rewritten. Check the sites list in " +
			"the network admin — a network whose subsites are on their own domains has " +
			"records the panel cannot rewrite for you.\n")
	}

	// Search engines, on a staging copy.
	//
	// A staging site is a second, complete copy of somebody's content on a
	// hostname nobody meant to publish. Indexed, it competes with the site it
	// was copied from. This is only a request — it writes the robots meta tag
	// and robots.txt — but it is the request everybody expects a staging
	// environment to make, and the panel is the only thing in a position to
	// make it.
	if args.Staging {
		runner.Exec(fmt.Sprintf("%s option update blog_public 0 --raw", wp), time.Minute)

		log.WriteString("Search engines are asked not to index this copy " +
			"(Settings · Reading).\n")
	}

	// Everything that caches a URL.
	//
	// Permalinks are rewrite rules stored in the database and they contain the
	// site's own paths; transients hold whatever a plugin decided to remember,
	// which is routinely a full URL. Flushed last, after every rewrite above.
	runner.Exec(fmt.Sprintf(
		"%s rewrite flush --hard 2>/dev/null; %s transient delete --all 2>/dev/null; "+
			"%s cache flush 2>/dev/null; true", wp, wp, wp), 5*time.Minute)

	log.WriteString("Permalinks, transients and the object cache were flushed.")

	return log.String(), nil
}

// repointWordPressConfig points the copy at its own database and takes the
// source's cache off it.
//
// Both halves of the same problem: a copy that reads the source's database is
// production under a second hostname, and a copy that reads the source's cached
// objects is production under a second hostname for as long as the cache holds.
// Neither looks broken from the outside, which is what makes them worth doing
// before anything is allowed to serve a request.
func (s *Service) repointWordPressConfig(runner *tasks.Runner, args repointArgs) (string, error) {
	if !proc.Which("wp") {
		return "", fmt.Errorf("WP-CLI is not installed on this machine, and a copied " +
			"wp-config.php cannot be re-pointed without it")
	}

	root := args.Site.DocumentRoot()
	wp := "sudo -u www-data wp --path=" + proc.Quote(root) + " --skip-plugins --skip-themes"

	if !proc.Ok("sudo test -f " + proc.Quote(root+"/wp-config.php")) {
		return "", fmt.Errorf("there is no wp-config.php in %s, so this is not a "+
			"WordPress installation the panel can re-point", root)
	}

	if err := s.rewriteWordPressConfig(runner, args, root); err != nil {
		return "", err
	}

	note := fmt.Sprintf("wp-config.php now names the %s database, so this site reads and "+
		"writes its own data and not the one it was copied from.", args.Database.Name)

	if cache := s.resetWordPressCache(runner, args, root, wp); cache != "" {
		note += " " + cache
	}

	return note, nil
}

// rewriteWordPressConfig points wp-config.php at the destination's own
// database.
func (s *Service) rewriteWordPressConfig(runner *tasks.Runner, args repointArgs,
	root string) error {
	if args.Database == nil {
		return fmt.Errorf("this site has no database of its own, so there is nothing " +
			"to point wp-config.php at")
	}

	// --skip-plugins and --skip-themes are not there for speed. A plugin that
	// errors on load — which a plugin reading a database that is not yet the
	// right one very well might — takes WP-CLI down with it, and this is the
	// command that makes the database the right one.
	wp := "sudo -u www-data wp --path=" + proc.Quote(root) + " --skip-plugins --skip-themes"

	settings := []string{
		"config set DB_NAME " + proc.Quote(args.Database.Name),
		"config set DB_USER " + proc.Quote(args.Database.Username),
		"config set DB_PASSWORD " + proc.Quote(args.Database.Password),
		"config set DB_HOST 127.0.0.1",
	}

	for _, setting := range settings {
		if err := runner.Must(wp+" "+setting+" --type=constant", 2*time.Minute); err != nil {
			return err
		}
	}

	return nil
}

// resetWordPressCache takes the source site's cache off the copy.
func (s *Service) resetWordPressCache(runner *tasks.Runner, args repointArgs,
	root, wp string) string {
	var notes []string

	// The drop-ins. object-cache.php is what makes WordPress talk to Redis at
	// all, and advanced-cache.php the same for a page cache; both were written
	// for the source site and both are files in wp-content, so both came
	// across with the copy.
	removed := proc.Ok(fmt.Sprintf("sudo rm -f %s %s && sudo rm -rf %s",
		proc.Quote(root+"/wp-content/object-cache.php"),
		proc.Quote(root+"/wp-content/advanced-cache.php"),
		proc.Quote(root+"/wp-content/cache")))

	if removed {
		notes = append(notes, "The copied object-cache and page-cache drop-ins were removed")
	}

	// The prefix, where the installation keeps one. Rewritten rather than
	// deleted so that turning the cache back on from the panel's Cache tab
	// finds a configuration that is already this site's own.
	if proc.Ok(fmt.Sprintf("%s config has WP_REDIS_PREFIX --type=constant 2>/dev/null", wp)) {
		runner.Exec(fmt.Sprintf("%s config set WP_REDIS_PREFIX %s --type=constant",
			wp, proc.Quote(RedisPrefix(args.Site))), time.Minute)

		notes = append(notes, "WP_REDIS_PREFIX now belongs to this site rather than the "+
			"one it was copied from")
	}

	if len(notes) == 0 {
		return ""
	}

	return strings.Join(notes, ", ") + "."
}

// searchReplaceWordPress rewrites every form the old URL can take in the
// database.
//
// Six passes, not one, and the reason is encoding. The same URL is stored as
// `https://example.com` in an option, as `https:\/\/example.com` inside a
// block editor's JSON, and as `//example.com` in a theme's protocol-relative
// asset — and a replacement looking for the first finds neither of the others.
// A missed form is not a cosmetic problem: one `https://production.com` left in
// a stylesheet URL is a staging page loading production's assets, and one left
// in a redirect is a visitor sent to production.
//
// `--all-tables` because a plugin's own table is as likely to hold a URL as a
// core one, and `--precise` because the fast path is a string replacement over
// the raw column, which corrupts the length prefixes inside serialised PHP and
// produces option values WordPress silently reads as empty. Slower, and the
// difference between a clone and a clone with half its plugin settings gone.
//
// `guid` is skipped. It looks like a URL and is not one: it is a permanent
// identifier for a post, and feed readers use it to tell whether they have seen
// an item before. Rewriting it on a push to production re-delivers every post
// in the feed to every subscriber.
// wordPressReplacements is every from/to pair one move needs, in the order they
// have to run in.
//
// Pure, and its own function, because it is the part of this feature that has
// to be right and the part that cannot be checked by looking at it. A missing
// pair is a URL left pointing at the other site, and a pair in the wrong order
// is a URL rewritten twice.
func wordPressReplacements(fromURL, toURL string) [][2]string {
	fromHost := hostOf(fromURL)
	toHost := hostOf(toURL)

	// A move to the same address rewrites nothing. The hostnames being equal is
	// *not* the same question: a staging site whose certificate failed to issue
	// keeps its hostname and changes scheme, and that is a rewrite — leaving it
	// out is what puts https:// URLs on a site serving plain HTTP.
	if fromHost == "" || toHost == "" || fromURL == toURL {
		return nil
	}

	// Scheme-ful forms first, so `https://old` becomes the destination's actual
	// scheme rather than being rewritten host-only and left claiming HTTPS on a
	// site that has no certificate.
	candidates := [][2]string{
		{"https://" + fromHost, toURL},
		{"http://" + fromHost, toURL},

		// Protocol-relative, for whatever the two above did not match.
		{"//" + fromHost, "//" + toHost},

		// The same three as they appear inside a JSON string — the block
		// editor, the page builders, and every plugin that stores its settings
		// encoded. A replacement looking for `https://old` does not match
		// `https:\/\/old`, and one left behind is a staging page loading
		// production's assets.
		{`https:\/\/` + fromHost, escapeSlashes(toURL)},
		{`http:\/\/` + fromHost, escapeSlashes(toURL)},
		{`\/\/` + fromHost, `\/\/` + toHost},
	}

	// A pair that would replace a string with itself is dropped rather than
	// run. On a scheme-only move — the certificate case above — three of the
	// six are exactly that, and each one is a full scan of every table in the
	// database to achieve nothing.
	pairs := make([][2]string, 0, len(candidates))

	for _, pair := range candidates {
		if pair[0] != pair[1] {
			pairs = append(pairs, pair)
		}
	}

	return pairs
}

func (s *Service) searchReplaceWordPress(runner *tasks.Runner, args repointArgs,
	wp string, network bool) (string, error) {
	fromHost := hostOf(args.FromURL)
	toHost := hostOf(args.ToURL)

	if fromHost == "" || toHost == "" {
		return "", fmt.Errorf("could not work out the hostnames to rewrite between (%q and %q)",
			args.FromURL, args.ToURL)
	}

	flags := "--all-tables --precise --skip-columns=guid --report-changed-only --quiet"

	if network {
		flags += " --network"
	}

	var log strings.Builder

	for _, pair := range wordPressReplacements(args.FromURL, args.ToURL) {
		from, to := pair[0], pair[1]

		if from == to {
			continue
		}

		command := fmt.Sprintf("%s search-replace %s %s %s", wp,
			proc.Quote(from), proc.Quote(to), flags)

		// Must rather than Exec: a replacement that failed is a database still
		// full of the old address, and carrying on to flush the caches would
		// report a clone that worked.
		if err := runner.Must(command, 30*time.Minute); err != nil {
			return "", fmt.Errorf("rewriting %s to %s in the database failed: %w", from, to, err)
		}
	}

	fmt.Fprintf(&log, "Every %s in the database was rewritten to %s — in plain text, "+
		"protocol-relative and JSON-escaped form, and inside serialised PHP.",
		fromHost, toHost)

	return log.String(), nil
}

// escapeSlashes writes a URL the way it appears inside a JSON string.
func escapeSlashes(url string) string {
	return strings.ReplaceAll(url, "/", `\/`)
}

// hostOf is the hostname out of a URL the panel built, which always has a
// scheme and never a path.
func hostOf(url string) string {
	host := url

	for _, prefix := range []string{"https://", "http://", "//"} {
		host = strings.TrimPrefix(host, prefix)
	}

	host, _, _ = strings.Cut(host, "/")

	return strings.ToLower(strings.TrimSpace(host))
}

// --------------------------------------------------------------- Laravel

// repointLaravelConfig points the copy's .env at its own database.
func (s *Service) repointLaravelConfig(runner *tasks.Runner, args repointArgs) (string, error) {
	if args.Database == nil {
		return "This site has no database of its own, so there were no credentials " +
			"to re-point.", nil
	}

	if err := s.writeLaravelValues(runner, args, map[string]string{
		"DB_DATABASE": args.Database.Name,
		"DB_USERNAME": args.Database.Username,
		"DB_PASSWORD": args.Database.Password,
	}); err != nil {
		return "", err
	}

	return ".env now names the " + args.Database.Name + " database, so this site reads " +
		"and writes its own data and not the one it was copied from.", nil
}

// repointLaravelURL rewrites APP_URL.
//
// Laravel keeps its address in a file rather than in the database, which makes
// this the short half of the job — and the caches the long one. `config:cache`
// compiles .env into a PHP file, so an application whose configuration was
// cached before the copy goes on using the old URL no matter what .env says.
func (s *Service) repointLaravelURL(runner *tasks.Runner, args repointArgs) (string, error) {
	if err := s.writeLaravelValues(runner, args, map[string]string{
		"APP_URL": args.ToURL,
	}); err != nil {
		return "", err
	}

	return "APP_URL is " + args.ToURL + ". The configuration, route, view and " +
		"application caches were cleared and rebuilt. APP_KEY was left as it is, so " +
		"anything the source site encrypted can still be decrypted here.", nil
}

// writeLaravelValues sets keys in .env and rebuilds the caches that compiled
// the old ones in.
func (s *Service) writeLaravelValues(runner *tasks.Runner, args repointArgs,
	values map[string]string) error {
	root := args.Site.RootPath
	env := root + "/.env"

	if !proc.Ok("sudo test -f " + proc.Quote(env)) {
		// Not an error. A Laravel site deployed from a repository that keeps
		// its configuration elsewhere is unusual and legitimate, and failing
		// the copy over a file the application does not use would be the panel
		// insisting on its own idea of how Laravel is configured.
		runner.Write("There is no .env file in this site, so there was nothing to write.\n")

		return nil
	}

	contents, err := proc.ReadFile(env, true)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", env, err)
	}

	if err := proc.PutFile(env, setEnvValues(contents, values), true); err != nil {
		return err
	}

	proc.Ok(fmt.Sprintf("sudo chown www-data:www-data %[1]s && sudo chmod 640 %[1]s",
		proc.Quote(env)))

	php := args.Site.PHPVersion

	// Cleared, then rebuilt. Clearing alone leaves the application reading
	// .env on every request, which works and is slower than it was before the
	// copy; rebuilding puts it back where it was.
	runner.Exec(fmt.Sprintf(
		"cd %[1]s && sudo -u www-data php%[2]s artisan config:clear; "+
			"sudo -u www-data php%[2]s artisan route:clear; "+
			"sudo -u www-data php%[2]s artisan view:clear; "+
			"sudo -u www-data php%[2]s artisan cache:clear; "+
			"sudo -u www-data php%[2]s artisan config:cache; true",
		proc.Quote(root), php), 10*time.Minute)

	return nil
}

// setEnvValues rewrites the keys it is given and leaves every other line alone.
//
// Line by line rather than regenerating the file: a .env is hand-edited, often
// commented, and frequently holds API keys the panel knows nothing about.
// Rewriting the whole thing from what the panel can see would silently drop all
// of that.
func setEnvValues(contents string, values map[string]string) string {
	lines := strings.Split(contents, "\n")
	written := map[string]bool{}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, _, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)

		value, wanted := values[key]
		if !wanted {
			continue
		}

		lines[i] = key + "=" + quoteEnvValue(value)
		written[key] = true
	}

	// A key the file did not have is appended. A Laravel application with no
	// APP_URL line is one that defaults to localhost, which is not the answer
	// on either half of a staging pair.
	for key, value := range values {
		if !written[key] {
			lines = append(lines, key+"="+quoteEnvValue(value))
		}
	}

	return strings.Join(lines, "\n")
}

// quoteEnvValue quotes a value that needs it. A generated database password
// contains characters a shell would read and so would Laravel's own parser.
func quoteEnvValue(value string) string {
	if value == "" {
		return `""`
	}

	if !strings.ContainsAny(value, " \t\"'$#=&|<>()!*?[]{}`\\") {
		return value
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(value) + `"`
}

// ------------------------------------------------------------- the rest

// repointEnvFiles swaps the hostname in a proxied application's environment
// files.
//
// Best effort, and said as such in the console. A Node or Next.js application
// keeps its configuration wherever its author decided to, and the panel is not
// going to find a URL built out of three variables at runtime. What it can do
// is the case that covers most of them — a hostname written into a .env — and
// then say plainly that the rest is the operator's to check.
//
// The port is not touched here. A proxied staging site is given a free port of
// its own when its row is created, and the systemd unit is written from that.
func (s *Service) repointEnvFiles(runner *tasks.Runner, args repointArgs) (string, error) {
	fromHost := hostOf(args.FromURL)
	toHost := hostOf(args.ToURL)

	if fromHost == "" || toHost == "" || fromHost == toHost {
		return "No hostname to rewrite.", nil
	}

	root := args.Site.RootPath
	changed := []string{}

	for _, name := range []string{".env", ".env.local", ".env.production", ".env.production.local"} {
		path := root + "/" + name

		if !proc.Ok("sudo test -f " + proc.Quote(path)) {
			continue
		}

		contents, err := proc.ReadFile(path, true)
		if err != nil {
			continue
		}

		if !strings.Contains(contents, fromHost) {
			continue
		}

		if err := proc.PutFile(path, strings.ReplaceAll(contents, fromHost, toHost), true); err != nil {
			continue
		}

		proc.Ok("sudo chown www-data:www-data " + proc.Quote(path))

		changed = append(changed, name)
	}

	// A framework that builds its client bundle is the case worth naming
	// separately. Nuxt bakes NUXT_PUBLIC_* and Next.js bakes NEXT_PUBLIC_* into
	// the files a browser downloads, at build time — so a copy of a *built*
	// application carries the source's address inside assets no rewrite of a
	// .env file reaches. Rebuilding is the fix, and the panel says so rather
	// than reporting a copy that is only mostly re-pointed.
	rebuild := ""

	if args.Site.BuildCommand != "" {
		rebuild = " This site is built rather than run from source, and anything the " +
			"build compiled in — a NUXT_PUBLIC_ or NEXT_PUBLIC_ variable, a value " +
			"read at build time — is still the one it was built with. Redeploy this " +
			"site to rebuild it against its own address."
	}

	if len(changed) == 0 {
		return "No environment file in this site mentioned " + fromHost +
			". If the application has its address configured anywhere else, that is " +
			"yours to change." + rebuild, nil
	}

	return fmt.Sprintf("%s mentioned %s and now say %s. Anything else the application "+
		"has its address in — a value assembled at runtime — is yours to check.%s",
		strings.Join(changed, ", "), fromHost, toHost, rebuild), nil
}
