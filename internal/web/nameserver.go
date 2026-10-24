package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/nameserver"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// This file is the panel's own DNS hosting: the zones this machine answers for
// and the setup that has to happen outside the panel before it answers for
// anything.
//
// The second half is most of the page. Connecting Cloudflare is pasting a
// token; hosting DNS yourself is registering two nameserver names as glue
// records at a registrar, which is a screen most people have never opened and
// which every registrar calls something different. A panel that offers the
// feature and not the instructions has offered a zone editor that silently
// does nothing.

func (a *App) nameserverIndex(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	zones, err := a.Store.DNSZones(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, "could not list the hosted zones", err)
		return
	}

	status := nameserver.Describe(r.Context(), a.Store)

	rows := make([]map[string]any, 0, len(zones))
	for _, zone := range zones {
		rows = append(rows, zoneSummary(zone, status))
	}

	// The credential the zone editor and every other DNS-aware page writes
	// through. Null until the nameserver is set up, which is what the page
	// uses to decide whether to offer records at all.
	var accountID any

	if account := a.localDNSAccount(r.Context(), user.ID); account != nil {
		accountID = account.ID
	}

	a.Render.Render(w, r, "System/Nameserver", inertia.Props{
		"status":      status,
		"zones":       rows,
		"accountId":   accountID,
		"recordTypes": config.DNSRecordTypes,
		"installed":   a.Store.ServiceInstalled("dns"),
	})
}

// zoneSummary is one row of the zone list.
func zoneSummary(zone *store.DNSZone, status nameserver.Status) map[string]any {
	// Whether this zone is the one the nameservers are named inside. It is the
	// zone that has to exist for the whole arrangement to work, so the page
	// says which one it is rather than leaving the operator to notice.
	serving := false

	for _, name := range status.Settings.Nameservers() {
		if name == zone.Name || strings.HasSuffix(name, "."+zone.Name) {
			serving = true
		}
	}

	return map[string]any{
		"id":           zone.ID,
		"name":         zone.Name,
		"records":      zone.Records,
		"serial":       zone.Serial,
		"updated_at":   nullable(zone.UpdatedAt),
		"holds_the_ns": serving,
	}
}

// nameserverZoneCheck asks the public DNS what a zone's delegation currently
// says, which is the one question the panel cannot answer from inside itself.
//
// Its own endpoint rather than part of the page payload: it is a query to a
// resolver out on the internet, and a panel hosting thirty zones would spend
// thirty of them on every page load.
func (a *App) nameserverZoneCheck(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	zone, err := a.Store.FindDNSZone(r.Context(), routeID(r, "zone"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such zone.")
		return
	}

	check := nameserver.CheckDelegation(r.Context(), zone.Name,
		nameserver.CurrentSettings(a.Store))

	a.writeJSON(w, http.StatusOK, map[string]any{
		"delegated":   check.Delegated,
		"nameservers": check.Nameservers,
		"error":       nullable(check.Error),
	})
}

// nameserverSettings saves the two hostnames the world will know this
// nameserver by, and makes the panel's own DNS available everywhere a provider
// is.
func (a *App) nameserverSettings(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	settings := nameserver.Settings{
		NS1:        store.NormaliseZoneName(input["ns1"]),
		NS2:        store.NormaliseZoneName(input["ns2"]),
		Hostmaster: strings.ToLower(strings.TrimSpace(input["hostmaster"])),
	}

	if settings.NS1 == "" {
		sess.Error("ns1", "The first nameserver's hostname is what domains get pointed at.")
	} else if reason := invalidHostname(settings.NS1); reason != "" {
		sess.Error("ns1", reason)
	}

	// The second is optional and strongly wanted. Optional because a registry
	// that accepts one will serve a working domain, and wanted because the
	// ones that do not accept one will refuse the delegation outright — so
	// this is a validation error only when it is filled in badly, and a
	// sentence on the page otherwise.
	if settings.NS2 != "" {
		if reason := invalidHostname(settings.NS2); reason != "" {
			sess.Error("ns2", reason)
		} else if settings.NS2 == settings.NS1 {
			sess.Error("ns2", "The two nameservers need different names.")
		}
	}

	if settings.Hostmaster != "" && !strings.Contains(settings.Hostmaster, "@") {
		sess.Error("hostmaster", "This is an email address — it goes into every zone's SOA.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/dns/nameserver")
		return
	}

	nameserver.SaveSettings(a.Store, settings)

	// The credential row that makes this machine selectable wherever a DNS
	// provider is. Created here rather than at boot because this is the moment
	// it becomes true: before the hostnames are set, a site pointed at the
	// panel's own DNS would publish records nothing could find.
	if _, err := a.EnsureLocalDNSAccount(r.Context(), user.ID); err != nil {
		a.Log.Error("could not register the panel's own DNS as a provider", "error", err)
	}

	// Every zone, not one: the nameserver names are in every zone's SOA and in
	// every zone's apex NS records, so changing them changes every file.
	if err := nameserver.Apply(r.Context(), a.Store); err != nil {
		if errors.Is(err, nameserver.ErrNotInstalled) {
			a.okBack(w, r, "Saved. Install the DNS server from the Apps page and the "+
				"zones are written as soon as it is there.")

			return
		}

		a.failBack(w, r, "Saved, but the nameserver would not take it: "+err.Error())

		return
	}

	a.okBack(w, r, "Nameserver settings saved and every zone rewritten.")
}

// nameserverZoneStore starts hosting a zone.
func (a *App) nameserverZoneStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := store.NormaliseZoneName(input["name"])

	if name == "" {
		sess.Error("name", "Which domain?")
	} else if reason := invalidZoneName(name); reason != "" {
		sess.Error("name", reason)
	} else if _, err := a.Store.FindDNSZoneByName(r.Context(), name); err == nil {
		sess.Error("name", name+" is already hosted on this server.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/dns/nameserver")
		return
	}

	settings := nameserver.CurrentSettings(a.Store)

	if !settings.Configured() {
		a.failBack(w, r, "Set the nameserver hostnames first — a zone has to name the "+
			"servers that answer for it.")

		return
	}

	zoneID, err := a.Store.CreateDNSZone(r.Context(), user.ID, name)
	if err != nil || zoneID <= 0 {
		a.Log.Error("could not create the zone", "zone", name, "error", err)
		a.failBack(w, r, "Could not create the zone.")

		return
	}

	ip := system.PublicIP()

	// A zone that answers for something the moment it is created. The apex and
	// www point here, and — when this is the zone the nameservers themselves
	// live in — so do ns1 and ns2, which is the pair of records that stops a
	// resolver being sent back to a name nothing can find.
	wanted := nameserver.DefaultRecords(name, ip)
	wanted = append(wanted, nameserver.NameserverRecords(name, settings, ip)...)

	for _, record := range wanted {
		record.ZoneID = zoneID
		a.Store.CreateDNSZoneRecord(r.Context(), record)
	}

	if _, err := a.EnsureLocalDNSAccount(r.Context(), user.ID); err != nil {
		a.Log.Error("could not register the panel's own DNS as a provider", "error", err)
	}

	// The whole configuration, because the set of zones has changed and BIND
	// has to be told about one it has never heard of.
	if err := nameserver.Apply(r.Context(), a.Store); err != nil {
		if errors.Is(err, nameserver.ErrNotInstalled) {
			a.okBack(w, r, name+" added. Install the DNS server from the Apps page and "+
				"this machine starts answering for it.")

			return
		}

		a.failBack(w, r, name+" was added but the nameserver would not load it: "+err.Error())

		return
	}

	message := name + " is now served by this machine."

	if len(wanted) > 0 {
		message = fmt.Sprintf("%s %d record(s) were created, pointing at %s.", message,
			len(wanted), ip)
	}

	message += " Point the domain's nameservers at " +
		strings.Join(settings.Nameservers(), " and ") + " at its registrar."

	a.okBack(w, r, message)
}

// nameserverZoneDestroy stops hosting one.
func (a *App) nameserverZoneDestroy(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	zone, err := a.Store.FindDNSZone(r.Context(), routeID(r, "zone"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such zone.")
		return
	}

	if err := a.Store.DeleteDNSZone(r.Context(), zone.ID, user.ID); err != nil {
		a.Log.Error("could not remove the zone", "zone", zone.Name, "error", err)
		a.failBack(w, r, "Could not remove the zone.")

		return
	}

	// The zone file is left on disk and the `zone` block is not: a machine that
	// no longer claims authority over the name is the part that matters, and
	// the file is a copy of what the zone said, which is occasionally the only
	// copy anybody has.
	if err := nameserver.Apply(r.Context(), a.Store); err != nil &&
		!errors.Is(err, nameserver.ErrNotInstalled) {
		a.failBack(w, r, zone.Name+" was removed but the nameserver would not reload: "+
			err.Error())

		return
	}

	a.Store.ForgetSetting("nameserver_error")

	a.okBack(w, r, zone.Name+" is no longer served by this machine. Anything still "+
		"pointing its nameservers here stops resolving.")
}

// nameserverResync writes every zone again and reloads.
//
// Here because the generated files and the panel's rows can drift for reasons
// that have nothing to do with the panel: a reload that failed while the
// machine was out of disk, a zone file somebody edited by hand, a BIND
// reinstalled over the top. One button that makes the machine say what the
// panel says is worth more than working out which of those happened.
func (a *App) nameserverResync(w http.ResponseWriter, r *http.Request) {
	if err := nameserver.Apply(r.Context(), a.Store); err != nil {
		switch {
		case errors.Is(err, nameserver.ErrNotInstalled):
			a.failBack(w, r, "The DNS server is not installed on this machine. "+
				"Install it from the Apps page.")

		case errors.Is(err, nameserver.ErrNotConfigured):
			a.failBack(w, r, "Set the nameserver hostnames first.")

		default:
			a.failBack(w, r, "The nameserver would not take the configuration: "+err.Error())
		}

		return
	}

	count := a.Store.DNSZoneCount(r.Context())

	a.okBack(w, r, fmt.Sprintf("%d zone(s) written and reloaded.", count))
}

// ----------------------------------------------------- the local credential

// EnsureLocalDNSAccount makes sure the panel's own nameserver appears wherever
// a DNS provider does.
//
// It is a row in dns_accounts with no credential in it, which looks odd until
// you see what it buys: creating a site, syncing a mail domain's SPF and DKIM,
// the zone editor and every MCP tool all take a credential id and go through
// one Client. Hosting the zone here is then one more provider rather than a
// second path through all of that — and the panel's own DNS gets the site
// integration, the mail records and the assistant's tools for nothing.
func (a *App) EnsureLocalDNSAccount(ctx context.Context, userID int64) (*store.DNSAccount, error) {
	if existing := a.localDNSAccount(ctx, userID); existing != nil {
		return existing, nil
	}

	account := &store.DNSAccount{
		UserID:   userID,
		Provider: dns.LocalProvider,
		Label:    "This server",

		// There is no credential. The column is not nullable and a marker
		// reads better in the table than an empty string, which would look
		// like a token that failed to save.
		APIToken: "local",
	}

	id, err := a.Store.CreateDNSAccount(ctx, account)
	if err != nil || id <= 0 {
		if err == nil {
			err = errors.New("the credential row was not created")
		}

		return nil, err
	}

	a.Store.MarkDNSAccountVerified(id, true)

	account.ID = id

	return account, nil
}

// localDNSAccount is the row, or nil.
func (a *App) localDNSAccount(ctx context.Context, userID int64) *store.DNSAccount {
	accounts, err := a.Store.DNSAccounts(ctx, userID)
	if err != nil {
		return nil
	}

	for _, account := range accounts {
		if dns.IsLocal(account.Provider) {
			return account
		}
	}

	return nil
}

// EnsureNameserverConfigured rewrites the generated configuration at startup.
//
// A panel that has just been upgraded may write a zone file differently from
// the one that wrote the files on disk, and a machine that was rebooted may
// have had BIND start before the panel did. Neither is a failure worth a log
// line on a machine that is not hosting DNS at all, which is why nothing here
// is reported unless there is a zone to report it about.
func (a *App) EnsureNameserverConfigured(ctx context.Context) {
	if !nameserver.Installed() || a.Store.DNSZoneCount(ctx) == 0 {
		return
	}

	if err := nameserver.Apply(ctx, a.Store); err != nil {
		a.Log.Error("could not write the nameserver configuration", "error", err)
	}
}

// ------------------------------------------------------------- validation

// invalidZoneName is why a name cannot be a zone, or empty.
func invalidZoneName(name string) string {
	if reason := invalidHostname(name); reason != "" {
		return reason
	}

	// A zone needs a dot: `com` is a top-level domain and `localhost` is not
	// a thing anybody delegates. Both are typing mistakes rather than zones,
	// and BIND would happily serve either while nothing on earth asked it to.
	if !strings.Contains(name, ".") {
		return "That is not a domain — it needs at least one dot, like example.com."
	}

	return ""
}

// invalidHostname is why a name is not a hostname, or empty.
//
// Checked here rather than left to BIND because the name goes into a generated
// file and then into a command line. Nothing but letters, digits, hyphens,
// dots and underscores gets through — underscores because `_dmarc` and
// `_acme-challenge` are names the panel itself publishes.
func invalidHostname(name string) string {
	if len(name) > 253 {
		return "That is longer than a domain name can be."
	}

	labels := strings.Split(name, ".")

	for _, label := range labels {
		if label == "" {
			return "There is an empty label in there — two dots in a row, or a leading dot."
		}

		if len(label) > 63 {
			return "One of the parts between the dots is longer than 63 characters."
		}

		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "A part of a domain name cannot start or end with a hyphen."
		}

		for _, character := range label {
			switch {
			case character >= 'a' && character <= 'z',
				character >= '0' && character <= '9',
				character == '-', character == '_':
			default:
				return "Only letters, digits, hyphens and dots are allowed. " +
					"An internationalised domain goes in as its punycode form, " +
					"which starts xn--."
			}
		}
	}

	return ""
}
