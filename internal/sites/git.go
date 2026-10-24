// Package sites — git deployment.
//
// Everything that runs a git command for a site goes through this file, and the
// reason is credentials. A clone, a fetch and a reachability check all need the
// same deploy key or the same token, and three copies of "how do we
// authenticate to this remote" is three places for one of them to be wrong —
// which is a site that deploys and then cannot pull, or pulls and cannot be
// checked.
package sites

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// DeployKeyRoot is where the private halves live.
//
// Outside every site's own directory, and 0700: a key inside the site root
// would be in the repository's working tree, inside every backup of the site,
// and — for a site whose document root is the site root — served to anybody who
// guessed the filename.
const DeployKeyRoot = "/var/lib/ubuntu-panel/deploy-keys"

// The ways the panel can authenticate to a remote.
const (
	// AuthNone is a public repository. Nothing is sent.
	AuthNone = "none"

	// AuthKey is an SSH deploy key the panel generated, whose public half the
	// operator adds to the repository.
	AuthKey = "key"

	// AuthToken is a personal access token or app password, sent over HTTPS.
	AuthToken = "token"
)

// RepoAuthMethods is what the form offers, in the order it offers them.
type RepoAuthMethod struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

var RepoAuthMethods = []RepoAuthMethod{
	{AuthNone, "Public repository",
		"Anyone can clone it, so the panel sends no credentials."},
	{AuthKey, "Deploy key (recommended)",
		"The panel generates an SSH key and you add its public half to the " +
			"repository. Read-only, works on one repository and nothing else, and " +
			"revoked by deleting it there."},
	{AuthToken, "Access token",
		"A personal access token or app password, sent over HTTPS. Use this where " +
			"a deploy key is not an option — an organisation that disallows them, or " +
			"a host that does not offer them."},
}

// ValidRepoAuth reports whether this is a method the panel knows.
func ValidRepoAuth(method string) bool {
	for _, entry := range RepoAuthMethods {
		if entry.Key == method {
			return true
		}
	}

	return false
}

// RepoAuthOf is the site's method with the empty value spelled out, so a site
// created before the column existed reads as public rather than as blank.
func RepoAuthOf(site *store.Site) string {
	if ValidRepoAuth(site.RepoAuth) {
		return site.RepoAuth
	}

	return AuthNone
}

// ------------------------------------------------------------ the remote URL

// NormaliseRepository validates a repository the operator typed and returns it
// in the shape git wants.
//
// Checked here rather than left to git because a URL that is wrong fails three
// steps into a deployment, after the vhost exists and the database has been
// created — and the error git gives for a typo in a hostname is not one anybody
// reads as "you typed the hostname wrong".
//
// Four shapes are accepted and they all mean the same repository:
//
//	https://github.com/owner/repo.git
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	github.com/owner/repo          — assumed https
func NormaliseRepository(repository string) (string, error) {
	repository = strings.TrimSpace(repository)

	if repository == "" {
		return "", nil
	}

	if strings.ContainsAny(repository, " \t\n\r\"'\\;|&$`<>") {
		return "", errors.New("that does not look like a repository URL — it has " +
			"characters a URL cannot contain")
	}

	// scp-like syntax, which is what every host's "clone with SSH" button
	// gives you. Not a URL, so it is checked by shape rather than parsed.
	if strings.HasPrefix(repository, "git@") || isSCPLike(repository) {
		host, path, found := strings.Cut(strings.TrimPrefix(repository, "ssh://"), ":")
		if !found || path == "" {
			return "", errors.New("an SSH repository looks like git@github.com:owner/repo.git")
		}

		if _, hostname, ok := strings.Cut(host, "@"); !ok || hostname == "" {
			return "", errors.New("an SSH repository needs a user, as in " +
				"git@github.com:owner/repo.git")
		}

		return repository, nil
	}

	// A bare host/path, which is what somebody pastes out of a browser's
	// address bar with the scheme trimmed.
	if !strings.Contains(repository, "://") {
		repository = "https://" + repository
	}

	parsed, err := url.Parse(repository)
	if err != nil {
		return "", errors.New("that is not a URL the panel can read")
	}

	switch parsed.Scheme {
	case "https", "ssh":
	case "http":
		// Refused rather than upgraded. An http clone sends a token in
		// plaintext, and every host the panel deals with has served https for
		// a decade — so this is far more likely to be a mistake than a
		// deliberate choice.
		return "", errors.New("use https rather than http: an http clone would send " +
			"your credentials in the clear")

	case "git":
		return "", errors.New("the git:// protocol is unauthenticated and unencrypted, " +
			"and GitHub removed support for it — use the https or SSH URL instead")

	default:
		return "", fmt.Errorf("%q is not a protocol the panel clones over — use https "+
			"or SSH", parsed.Scheme)
	}

	if parsed.Host == "" {
		return "", errors.New("that URL has no hostname in it")
	}

	if strings.Trim(parsed.Path, "/") == "" {
		return "", errors.New("that URL names a host but no repository")
	}

	// Credentials already in the URL — but only over HTTPS, and this
	// distinction matters.
	//
	// In `ssh://git@github.com/owner/repo.git` the `git@` is the SSH login, not
	// a secret: every host's SSH URL has it, and stripping it produces a URL
	// that cannot authenticate at all. Over HTTPS the same position is where a
	// token goes — `https://ghp_xxx@github.com/...` is a documented way to
	// pass one — so there it is a credential whichever half it is written in,
	// and it is taken out: left in, it would be written into .git/config in
	// the clear, printed in the deployment console, and copied into every
	// backup of the site.
	if parsed.Scheme == "https" && parsed.User != nil {
		parsed.User = nil

		return parsed.String(), errCredentialsInURL
	}

	return parsed.String(), nil
}

// errCredentialsInURL is returned alongside the cleaned URL, so a caller can
// store the clean one and still tell the operator what happened.
var errCredentialsInURL = errors.New(
	"the username and password were taken out of that URL — they would have been " +
		"written into the site's .git/config in the clear and into every backup of it. " +
		"Use the access token field instead, which is stored encrypted")

// ErrCredentialsInURL reports whether this is that.
func ErrCredentialsInURL(err error) bool { return errors.Is(err, errCredentialsInURL) }

// isSCPLike reports whether this is the `host:path` form rather than a URL.
func isSCPLike(repository string) bool {
	if strings.Contains(repository, "://") {
		return false
	}

	host, path, found := strings.Cut(repository, ":")

	// A port is not a path: `example.com:8080/x` is a URL missing its scheme,
	// not scp syntax.
	return found && host != "" && path != "" && !startsWithDigit(path)
}

func startsWithDigit(text string) bool {
	return text != "" && text[0] >= '0' && text[0] <= '9'
}

// CloneURL is the URL git is actually given.
//
// For a token this is where it goes: in the URL, as the password, because that
// is the only way git sends one over HTTPS without an interactive prompt. The
// URL therefore carries a secret, which is why every caller below pipes it in
// through a credential helper or a config that `ps` cannot read — and why
// proc.Redact knows about it.
func CloneURL(site *store.Site) string {
	repository := strings.TrimSpace(site.Repository)

	if RepoAuthOf(site) != AuthToken || site.RepoToken == "" {
		return repository
	}

	parsed, err := url.Parse(repository)
	if err != nil || parsed.Scheme != "https" {
		// A token is no use against an SSH URL. Left alone so the failure is
		// "permission denied (publickey)", which is at least the truth.
		return repository
	}

	// The username matters less than it looks: GitHub accepts anything with
	// the token as the password, GitLab wants `oauth2`, and Bitbucket wants
	// the account name with an app password. `x-access-token` is what GitHub's
	// own documentation uses and what GitLab and Bitbucket both tolerate.
	parsed.User = url.UserPassword(tokenUsername(parsed.Host), site.RepoToken)

	return parsed.String()
}

// tokenUsername is the username a host wants beside a token.
func tokenUsername(host string) string {
	if strings.Contains(strings.ToLower(host), "gitlab") {
		return "oauth2"
	}

	return "x-access-token"
}

// ----------------------------------------------------------- the environment

// gitAs is `sudo -u www-data` with the environment a git command needs, ready
// to have a git invocation appended to it.
//
// The environment goes after sudo and not before: sudo resets the environment,
// so variables set in front of it reach sudo and not the command. They are
// accepted as `VAR=value` arguments because the panel's sudoers rule matches
// ALL, which is what lets a caller set variables sudo would otherwise strip —
// the same way the Composer and npm steps in this package already do it.
//
// GIT_TERMINAL_PROMPT=0 is the one that matters most, and it is a bug fix
// rather than a nicety. Without it git asks a private repository for a
// username, finds no terminal, and waits — so a deployment against a private
// repository with no credentials did not fail, it hung until the thirty-minute
// command timeout and then reported a timeout, which is the one error message
// that tells you nothing. With it the same deployment fails in under a second
// saying authentication failed.
func gitAs(site *store.Site) string {
	parts := []string{
		"sudo -u www-data",
		"GIT_TERMINAL_PROMPT=0",

		// Nothing the panel runs may open an editor or a pager and wait for
		// somebody, and git opens both given the chance.
		"GIT_PAGER=cat",
		"GIT_EDITOR=true",

		// www-data's home is /var/www, which it may not own; git wants
		// somewhere for its config and will complain loudly without one.
		"HOME=/tmp",
	}

	if RepoAuthOf(site) == AuthKey {
		// IdentitiesOnly so ssh offers this key and not whatever else an agent
		// or /root/.ssh happens to hold. BatchMode so it never prompts.
		// accept-new rather than `no`: a first connection to github.com must
		// not be a question nobody can answer, while a host key that *changes*
		// later is still refused — which is the half of the check that
		// actually protects anything.
		parts = append(parts, "GIT_SSH_COMMAND="+proc.Quote(fmt.Sprintf(
			"ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new "+
				"-o UserKnownHostsFile=%s -o BatchMode=yes -o ConnectTimeout=20",
			DeployKeyPath(site), KnownHostsPath())))
	}

	return strings.Join(parts, " ") + " "
}

// DeployKeyPath is where a site's private key lives.
func DeployKeyPath(site *store.Site) string {
	return fmt.Sprintf("%s/site-%d", DeployKeyRoot, site.ID)
}

// KnownHostsPath is the host-key file the panel's own git uses.
//
// Its own file rather than the panel user's ~/.ssh/known_hosts: the panel user
// has no home directory it can rely on, and a file under the panel's own
// directory is one that survives the user being recreated.
func KnownHostsPath() string { return DeployKeyRoot + "/known_hosts" }

// GenerateDeployKey makes a site's key and returns the public half.
//
// ed25519 rather than RSA: shorter, faster, accepted by GitHub, GitLab and
// Bitbucket, and with no key size to get wrong. Regenerating replaces the pair,
// which invalidates whatever is at the host — said out loud by the caller,
// because the site then cannot pull until the new one is added.
func GenerateDeployKey(site *store.Site) (string, error) {
	path := DeployKeyPath(site)

	// Readable by www-data, because that is the user git runs as, and
	// root-owned so a site cannot overwrite its own key or anybody else's.
	//
	// The directory is 0710: www-data may walk through it and may not list it.
	// That is a smaller protection than it looks and it is worth being honest
	// about — every site on this machine runs as www-data, so a site that
	// guesses the filename can read another site's key. It is also not a new
	// exposure: the same site can already read the next one's wp-config.php and
	// connect to its database, and a read-only deploy key is the weaker of the
	// two credentials. What this does buy is that the key is not in any site's
	// directory, so it is not in a backup, not in a repository's working tree,
	// and not servable by a vhost.
	command := strings.Join([]string{
		"sudo mkdir -p " + proc.Quote(DeployKeyRoot),
		"sudo chown root:www-data " + proc.Quote(DeployKeyRoot),
		"sudo chmod 710 " + proc.Quote(DeployKeyRoot),
		"sudo rm -f " + proc.Quote(path) + " " + proc.Quote(path+".pub"),
		fmt.Sprintf("sudo ssh-keygen -t ed25519 -N '' -C %s -f %s",
			proc.Quote("ubuntu-panel:"+site.Domain), proc.Quote(path)),
		"sudo chown root:www-data " + proc.Quote(path) + " " + proc.Quote(path+".pub"),
		"sudo chmod 640 " + proc.Quote(path),
		"sudo chmod 644 " + proc.Quote(path+".pub"),

		// Writable by www-data so accept-new can actually record a host key —
		// unwritable, ssh warns and carries on, and then never pins anything.
		"sudo touch " + proc.Quote(KnownHostsPath()),
		"sudo chown root:www-data " + proc.Quote(KnownHostsPath()),
		"sudo chmod 660 " + proc.Quote(KnownHostsPath()),
	}, " && ")

	if result := proc.Run(command, 2*time.Minute); !result.OK() {
		return "", fmt.Errorf("could not generate a deploy key: %s", result.Output)
	}

	public, err := proc.ReadFile(path+".pub", true)
	if err != nil {
		return "", errors.New("the deploy key was generated but its public half could " +
			"not be read back")
	}

	return strings.TrimSpace(public), nil
}

// RemoveDeployKey takes a site's key away with the site.
func RemoveDeployKey(site *store.Site) {
	path := DeployKeyPath(site)

	proc.Ok("sudo rm -f " + proc.Quote(path) + " " + proc.Quote(path+".pub"))
}

// ----------------------------------------------------------------- the calls

// Access is what a reachability check found.
type Access struct {
	// OK is whether the remote answered and let the panel read it.
	OK bool `json:"ok"`

	// Branch is the branch that was asked about, and Found whether the remote
	// has it. A remote that answers and does not have the branch is the
	// failure nobody expects: `main` against a repository whose default is
	// still `master`.
	Branch string `json:"branch"`
	Found  bool   `json:"found"`

	// Commit is the branch's head, where it was found.
	Commit string `json:"commit"`

	// Message is what to show. Written for whoever is reading the page rather
	// than quoting git, because git's own words for "this repository is
	// private and you sent nothing" are about a repository not existing.
	Message string `json:"message"`
}

// CheckAccess asks the remote whether the panel can read it, without cloning.
//
// `git ls-remote` is a few hundred bytes and a second, and it answers every
// question the create form has: does the host exist, does the repository exist,
// are the credentials right, and is the branch there. Asking it before the site
// is created turns a deployment that fails on its fourth step — with a vhost
// and a database already made — into a sentence under the form field.
func CheckAccess(site *store.Site, branch string) Access {
	repository := strings.TrimSpace(site.Repository)

	if repository == "" {
		return Access{Message: "This site has no repository."}
	}

	if branch == "" {
		branch = site.Branch
	}

	if branch == "" {
		branch = "main"
	}

	check := Access{Branch: branch}

	// A key that was never generated. ssh reports this as "no such identity"
	// and the translation below reads it as a key the remote refused, which
	// sends somebody to the repository's settings to look for a key that is
	// not there to be found.
	if RepoAuthOf(site) == AuthKey && !proc.Exists(DeployKeyPath(site), true) {
		check.Message = "This site is set to use a deploy key and does not have one yet. " +
			"Generate it on the Git tab, then add its public half to the repository."

		return check
	}

	// --heads so the answer is branches rather than every tag in a repository
	// that has thousands, and no --exit-code: a repository with no branch of
	// this name still answered, and that is a different failure from one that
	// refused.
	result := proc.Run(gitAs(site)+"git ls-remote --heads "+
		proc.Quote(CloneURL(site)), 90*time.Second)

	if !result.OK() {
		check.Message = explainGitFailure(site, result.Output)

		return check
	}

	check.OK = true

	for _, line := range strings.Split(result.Output, "\n") {
		commit, ref, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found {
			continue
		}

		if strings.TrimSpace(ref) == "refs/heads/"+branch {
			check.Found = true
			check.Commit = shortCommit(commit)

			break
		}
	}

	if check.Found {
		check.Message = fmt.Sprintf("The repository answered and has a %s branch, at %s.",
			branch, check.Commit)

		return check
	}

	// Which branches it does have. The whole point of this check is to save
	// somebody a failed deployment, and "main does not exist" without saying
	// what does is half an answer.
	available := []string{}

	for _, line := range strings.Split(result.Output, "\n") {
		if _, ref, found := strings.Cut(strings.TrimSpace(line), "\t"); found {
			if name := strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"); name != ref {
				available = append(available, name)
			}
		}
	}

	if len(available) > 6 {
		available = append(available[:6], "…")
	}

	check.Message = fmt.Sprintf("The repository answered, but it has no %s branch.", branch)

	if len(available) > 0 {
		check.Message += " It has: " + strings.Join(available, ", ") + "."
	}

	return check
}

// explainGitFailure turns git's own words into the problem.
//
// git is answering a different question from the one the operator asked. "Could
// not read from remote repository" is what a private repository says to a
// client with no key, and reads as the repository being broken; "Repository not
// found" is what GitHub says to a *valid* token without access to it, and reads
// as a typo. Each of these is a thing somebody can go and fix, and the raw
// output is kept on the end because it is occasionally the real answer.
func explainGitFailure(site *store.Site, output string) string {
	lower := strings.ToLower(output)
	method := RepoAuthOf(site)

	var explanation string

	switch {
	case strings.Contains(lower, "could not resolve host"),
		strings.Contains(lower, "name or service not known"):
		explanation = "That hostname does not resolve from this server. Check the URL, " +
			"and that this machine has working DNS."

	case strings.Contains(lower, "permission denied (publickey)"),
		strings.Contains(lower, "no such identity"):
		explanation = "The remote refused the deploy key. Add the public key below to " +
			"the repository — on GitHub that is Settings · Deploy keys · Add deploy key " +
			"— and make sure it is on the repository itself rather than on an account."

	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "invalid username or password"),
		strings.Contains(lower, "terminal prompts disabled"):
		switch method {
		case AuthNone:
			explanation = "The repository is private and the panel sent no credentials. " +
				"Pick a deploy key or an access token below."

		case AuthToken:
			explanation = "The remote refused the access token. Check it has not expired " +
				"and that it carries read access to this repository's contents."

		default:
			explanation = "The remote asked for credentials the panel does not have."
		}

	case strings.Contains(lower, "repository not found"),
		strings.Contains(lower, "not found"), strings.Contains(lower, "does not exist"):
		if method == AuthNone {
			explanation = "The repository does not exist, or it is private — a private " +
				"repository is reported as not found to anybody who cannot see it. " +
				"Check the URL, and pick a deploy key or a token below if it is private."
		} else {
			explanation = "The remote says there is no such repository. Either the URL " +
				"is wrong, or the credential is valid but has no access to this " +
				"repository — hosts report both the same way."
		}

	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"),
		strings.Contains(lower, "connection refused"):
		explanation = "The connection to the remote timed out. Port 22 outbound is what " +
			"an SSH clone needs, and some hosts block it — GitHub answers SSH on 443 " +
			"at ssh.github.com if yours does."

	default:
		explanation = "The remote could not be read."
	}

	if trimmed := firstGitLine(output); trimmed != "" {
		explanation += " git said: " + trimmed
	}

	return explanation
}

// firstGitLine is the most useful line of git's output, which is rarely the
// first: ssh and git both lead with progress and advice.
func firstGitLine(output string) string {
	var best string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "Warning: Permanently added") {
			continue
		}

		line = strings.TrimPrefix(line, "fatal: ")
		line = strings.TrimPrefix(line, "ERROR: ")
		line = strings.TrimPrefix(line, "remote: ")

		if line == "" {
			continue
		}

		if best == "" {
			best = line
		}

		// A line naming the actual problem beats the generic one git ends with.
		if strings.Contains(strings.ToLower(line), "denied") ||
			strings.Contains(strings.ToLower(line), "not found") ||
			strings.Contains(strings.ToLower(line), "authentication") {
			best = line

			break
		}
	}

	if len(best) > 200 {
		best = best[:200]
	}

	return best
}

// RemoteHead is the commit a branch is at on the remote, or empty.
//
// What the polling pull asks before it does anything. A fetch of a repository
// that has not changed is a clone's worth of work and a service restart for
// nothing, which on a five-minute interval is a site that restarts twelve times
// an hour.
func RemoteHead(site *store.Site, branch string) string {
	if branch == "" {
		branch = site.Branch
	}

	if branch == "" {
		branch = "main"
	}

	result := proc.Run(gitAs(site)+"git ls-remote "+proc.Quote(CloneURL(site))+" "+
		proc.Quote("refs/heads/"+branch), 60*time.Second)

	if !result.OK() {
		return ""
	}

	commit, _, _ := strings.Cut(strings.TrimSpace(result.Output), "\t")

	return shortCommit(commit)
}

// LocalHead is the commit the working tree is at, short form.
func LocalHead(site *store.Site) string {
	result := proc.Run(fmt.Sprintf("cd %s && sudo -u www-data git rev-parse --short HEAD",
		proc.Quote(site.RootPath)), 30*time.Second)

	if !result.OK() {
		return ""
	}

	return strings.TrimSpace(result.Output)
}

// LocalSubject is the message of the commit the site is serving.
func LocalSubject(site *store.Site) string {
	result := proc.Run(fmt.Sprintf(
		"cd %s && sudo -u www-data git log -1 --pretty=%%s", proc.Quote(site.RootPath)),
		30*time.Second)

	if !result.OK() {
		return ""
	}

	subject := strings.TrimSpace(result.Output)

	if len(subject) > 160 {
		subject = subject[:160] + "…"
	}

	return subject
}

// shortCommit is the first seven characters, which is what everybody quotes.
func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)

	if len(commit) > 7 {
		return commit[:7]
	}

	return commit
}

// ------------------------------------------------------------- the commands

// CloneCommand is the shell for a fresh clone into the site's root.
//
// `root` arrives already shell-quoted, as it is everywhere else in this
// package: the callers build it once with proc.Quote and pass it to every step.
//
// The remote URL is rewritten to the clean one straight afterwards. A clone
// over HTTPS with a token writes the whole URL — token included — into
// .git/config, where it stays: in the working tree, in every backup of the
// site, and readable by every other site on the machine. Scrubbing it there and
// passing it per command instead costs one extra line.
func CloneCommand(site *store.Site, root, branch string) string {
	clone := fmt.Sprintf(
		"sudo rm -rf %[1]s && sudo mkdir -p %[1]s && sudo chown www-data:www-data %[1]s && "+
			"%[2]sgit clone --depth 1 --branch %[3]s %[4]s %[1]s",
		root, gitAs(site), proc.Quote(branch), proc.Quote(CloneURL(site)))

	if CloneURL(site) == strings.TrimSpace(site.Repository) {
		return clone
	}

	return clone + fmt.Sprintf(" && cd %s && sudo -u www-data git remote set-url origin %s",
		root, proc.Quote(strings.TrimSpace(site.Repository)))
}

// FetchCommand is the shell for bringing an existing checkout up to date.
//
// Fetched from an explicit URL with an explicit refspec rather than from the
// configured remote, which solves three things at once.
//
// The URL, because it is where the credential goes — passed per command, it
// never reaches .git/config. The refspec, because a `--depth 1 --branch main`
// clone configures itself to fetch only main: changing the branch in the panel
// afterwards gave a fetch that succeeded and a reset that failed on a ref
// nothing had ever fetched. And together, because a site whose credentials
// changed would otherwise go on using whatever URL the original clone
// recorded — a key that had since been revoked.
func FetchCommand(site *store.Site, root, branch string) string {
	return fmt.Sprintf(
		"cd %[1]s && "+
			"%[2]sgit fetch --depth 1 %[3]s %[4]s && "+
			"sudo -u www-data git reset --hard %[5]s",
		root, gitAs(site),
		proc.Quote(CloneURL(site)),
		proc.Quote(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)),
		proc.Quote("refs/remotes/origin/"+branch))
}

// --------------------------------------------------------- automatic pulling

// PullSchedule is an interval the panel will check a repository on.
type PullSchedule struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// PullSchedules is what the form offers.
//
// A menu rather than a cron expression, the same choice the backup schedule
// makes: five space-separated fields is a language, and somebody who wants
// their site to follow a branch should not have to learn it.
//
// Nothing shorter than five minutes. The check itself is cheap — one
// `ls-remote`, a few hundred bytes — but a repository that has moved means a
// fetch, an npm install, a rebuild and a service restart, and a minute's
// interval on a busy branch is a site that is restarting more often than it is
// serving.
var PullSchedules = []PullSchedule{
	{"off", "Off"},
	{"5m", "Every 5 minutes"},
	{"15m", "Every 15 minutes"},
	{"hourly", "Hourly"},
	{"daily", "Daily"},
}

var pullIntervals = map[string]time.Duration{
	"5m":     5 * time.Minute,
	"15m":    15 * time.Minute,
	"hourly": time.Hour,
	"daily":  24 * time.Hour,
}

// ValidPullSchedule reports whether this is one of them. "off" counts: it is a
// choice somebody can make.
func ValidPullSchedule(schedule string) bool {
	if schedule == "" || schedule == "off" {
		return true
	}

	_, ok := pullIntervals[schedule]

	return ok
}

// NextPullAt is when a site on this schedule should next be checked, or zero
// for one that is not on a schedule — which is what DuePulls reads as "never
// ask about this site".
func NextPullAt(schedule string, from time.Time) int64 {
	every, ok := pullIntervals[schedule]
	if !ok {
		return 0
	}

	return from.Add(every).Unix()
}

// PullScheduleLabel is the interval's name, for a sentence.
func PullScheduleLabel(schedule string) string {
	for _, entry := range PullSchedules {
		if entry.Key == schedule {
			return entry.Label
		}
	}

	return schedule
}
