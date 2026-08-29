package web

import (
	"context"
	"errors"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// siteRequest is a site somebody asked the panel to create, from the create
// form or from an assistant's tool call.
//
// One shape and one place that turns it into a row, because the two ways in
// have to produce the same site. They did not: the form defaults DNS
// management on wherever a credential exists, and the tool — written second,
// with its own copy of the same decisions — left it off. What that produced was
// a WordPress site with no records published, which resolves nowhere, so its
// certificate could not be issued either. Both symptoms, one cause: a second
// copy of a set of defaults.
//
// The optional booleans are pointers so that "not sent" and "sent as false" are
// different answers. A form always sends its checkboxes; a tool call sends what
// the assistant thought to mention, and everything it did not mention has to
// come out as the panel's own default rather than as zero.
type siteRequest struct {
	Type    string
	Domain  string
	Aliases []string

	// WWW is whether an apex domain also answers on its www host. Unset means
	// yes, which is what somebody asking for example.com meant.
	WWW *bool

	PHPVersion   string
	WebDirectory string
	Repository   string
	Branch       string
	StartCommand string
	BuildCommand string

	SSL *bool

	ManageDNS    *bool
	DNSAccountID int64
	DNSType      string
	DNSContent   string
	DNSProxied   *bool

	AdminUser     string
	AdminEmail    string
	AdminPassword string
	Title         string
}

// siteProblem is a refusal with the form field it belongs to, so the create
// page can put it under the right input and a tool call can read it as a
// sentence.
type siteProblem struct {
	Field   string
	Message string
}

func (p siteProblem) Error() string { return p.Message }

// problem is the shorthand for returning one.
func problem(field, message string) error {
	return siteProblem{Field: field, Message: message}
}

// prepareSite validates a request and fills in every default the panel has.
//
// This is the whole of what "create a site the way this panel creates sites"
// means. Nothing above it decides anything: the form reads a request off a
// POST body and the tool reads one off its arguments, and both hand it here.
func (a *App) prepareSite(ctx context.Context, user *store.User, in siteRequest) (*store.Site, error) {
	if user == nil {
		return nil, problem("", "there is no account to create this site for")
	}

	kind := strings.ToLower(strings.TrimSpace(in.Type))
	domain := strings.ToLower(strings.TrimSpace(in.Domain))

	meta := config.FindSiteType(kind)

	if meta == nil {
		return nil, problem("type", "That is not a site type the panel can deploy.")
	}

	if !validDomain(domain) {
		return nil, problem("domain", "Enter a domain, like example.com.")
	}

	php := strings.TrimSpace(in.PHPVersion)
	if php == "" {
		php = a.Store.PHPVersion()
	}

	if !config.IsPHPVersion(php) {
		return nil, problem("php_version", "That is not a PHP version the panel installs.")
	}

	dnsType := strings.ToUpper(strings.TrimSpace(in.DNSType))
	if dnsType == "" {
		dnsType = "A"
	}

	if !config.IsDNSRecordType(dnsType) {
		return nil, problem("dns_type", "That is not a record type this form offers.")
	}

	if _, err := a.Store.FindSiteByDomain(ctx, domain); err == nil {
		return nil, problem("domain", "A site for this domain already exists.")
	}

	// Asked of the machine rather than the row: a stale `failed` from a
	// half-finished install would refuse a site this server can host perfectly
	// well.
	a.Catalog.Detect([]string{"node", "mysql", "wpcli", "php"})

	if missing := a.missingRequirement(kind); missing != "" {
		return nil, problem("type", missing)
	}

	manageDNS, dnsAccountID, err := a.resolveDNS(ctx, user, domain, in)
	if err != nil {
		return nil, err
	}

	// An explicit web directory wins; otherwise the type's own default.
	webDirectory := strings.Trim(orDefault(strings.TrimSpace(in.WebDirectory), meta.WebDirectory), "/")

	if webDirectory != "" {
		webDirectory = "/" + webDirectory
	}

	aliases := siteAliases(domain, in.Aliases, boolOr(in.WWW, true))

	site := &store.Site{
		UserID:       user.ID,
		DNSAccountID: dnsAccountID,
		Domain:       domain,
		Type:         kind,
		Aliases:      aliases,
		RootPath:     strings.TrimRight(a.Config.SitesRoot, "/") + "/" + domain,
		WebDirectory: webDirectory,
		PHPVersion:   php,
		NodeVersion:  a.Store.NodeVersion(),
		SSL:          boolOr(in.SSL, true),
		Repository:   strings.TrimSpace(in.Repository),
		Branch:       orDefault(strings.TrimSpace(in.Branch), "main"),
		ManageDNS:    manageDNS,
		DNSType:      dnsType,
		DNSContent:   orDefault(strings.TrimSpace(in.DNSContent), system.PublicIP()),
		DNSProxied:   boolOr(in.DNSProxied, true),
	}

	if meta.Managed && site.Repository != "" {
		return nil, problem("repository",
			meta.Label+" is installed by the panel itself, so it takes no repository.")
	}

	if meta.Proxied {
		site.AppPort = a.Store.NextFreePort(ctx)
		site.StartCommand = orDefault(strings.TrimSpace(in.StartCommand),
			orDefault(meta.Start, "npm run start"))
		site.BuildCommand = orDefault(strings.TrimSpace(in.BuildCommand), meta.Build)
	}

	// The application's own administrator, for the types where the panel
	// creates one: same username, and a password it generates.
	if meta.AdminAccount {
		site.WPTitle = orDefault(strings.TrimSpace(in.Title), store.DefaultWPTitle)
		site.WPAdminEmail = orDefault(strings.TrimSpace(in.AdminEmail), user.Email)
		site.WPAdminUser = orDefault(strings.TrimSpace(in.AdminUser), "admin")
		site.WPAdminPassword = strings.TrimSpace(in.AdminPassword)
	}

	return site, nil
}

// siteAliases is every extra hostname a site answers on: the ones asked for,
// and — for a domain at its root — its www host.
//
// The www is added rather than waited for because leaving it out is not a
// smaller site, it is a broken one. A person who puts example.com on this
// server has not asked for a site that 404s the moment somebody types the www
// in front of it, and the panel had no second chance to add it: aliases were
// settable at creation and nowhere else, so the vhost was written without the
// www and stayed that way. Every hostname here goes into the nginx
// server_name, the certbot request and the DNS records the panel publishes, so
// one alias in one place is the whole of the fix.
//
// An alias that is not a hostname is dropped rather than written: it would go
// straight into a server_name, and nginx refuses the whole configuration —
// every site on the machine — over one bad name in one file.
func siteAliases(domain string, requested []string, www bool) []string {
	aliases := []string{}
	seen := map[string]bool{domain: true}

	add := func(alias string) {
		alias = strings.ToLower(strings.TrimSpace(alias))

		if !validDomain(alias) || seen[alias] {
			return
		}

		seen[alias] = true
		aliases = append(aliases, alias)
	}

	for _, alias := range requested {
		add(alias)
	}

	if www && isApexDomain(domain) {
		add("www." + domain)
	}

	return aliases
}

// resolveDNS decides whether the panel publishes this site's records, and with
// which credential.
//
// The create form's own defaults, which is the point of this being in one
// place: DNS management is on wherever the account has a credential connected,
// and the credential is the one holding the domain — the form pre-selects the
// first, and a person picking from a list of two picks the right one. The panel
// asks the providers instead.
//
// A domain no connected credential holds is the case that matters, because it
// is the ordinary one: the panel is asked for test.example.com and example.com
// lives at a registrar nobody connected. The site is created either way — the
// machine's half of it is perfectly buildable — but with management off, so the
// deployment does not stop on a record it was never going to be able to write,
// and the site page and create_site both say which record to add and where.
// The panel used to hand that case the first credential in the list instead,
// which turned it into a deployment that failed on its second step with
// "No zone on this Cloudflare account covers …", before nginx, before
// WordPress, leaving a site that was only ever going to say NXDOMAIN.
//
// A provider that could not be answered at all is the other case, and the
// opposite call: management stays on, because in a minute the answer may be
// yes and the deployment's own DNS step will ask again.
func (a *App) resolveDNS(ctx context.Context, user *store.User, domain string,
	in siteRequest) (bool, int64, error) {
	accounts, err := a.Store.DNSAccounts(ctx, user.ID)
	if err != nil {
		return false, 0, errors.New("the panel could not read its DNS credentials")
	}

	// Off, either because it was asked for or because there is nothing to
	// publish with.
	if !boolOr(in.ManageDNS, len(accounts) > 0) || len(accounts) == 0 {
		return false, 0, nil
	}

	if in.DNSAccountID > 0 {
		account, err := a.Store.FindDNSAccount(ctx, in.DNSAccountID, user.ID)
		if err != nil {
			return false, 0, problem("dns_account_id",
				"Pick a DNS credential the panel can write with.")
		}

		// A chosen credential that plainly does not hold the domain is the same
		// answer as no credential at all: the panel publishes nothing and says
		// which record to add. Not a refusal — the create form pre-selects this
		// credential rather than the person choosing it, so refusing would be a
		// dead end in front of somebody whose domain is simply hosted somewhere
		// else, and whose site is otherwise perfectly buildable.
		if _, _, err := dns.New(account).FindZone(domain); errors.Is(err, dns.ErrNoZone) {
			return false, 0, nil
		}

		return true, in.DNSAccountID, nil
	}

	var unreachable int64

	for _, account := range accounts {
		_, _, err := dns.New(account).FindZone(domain)

		switch {
		case err == nil:
			return true, account.ID, nil

		case !errors.Is(err, dns.ErrNoZone) && unreachable == 0:
			unreachable = account.ID
		}
	}

	// Every provider answered, and none of them holds this domain. Nothing to
	// publish with, so nothing is promised.
	if unreachable == 0 {
		return false, 0, nil
	}

	// One of them did not answer at all, and it is the one that might have said
	// yes. The site keeps management on and the deployment's DNS step asks it
	// again when it gets there.
	return true, unreachable, nil
}

// boolOr reads an optional flag.
func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}

	return *value
}

// boolPointer is how a caller that has a real answer passes one.
func boolPointer(value bool) *bool { return &value }
