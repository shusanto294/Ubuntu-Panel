// Package nameserver makes this machine an authoritative DNS host.
//
// Everywhere else in the panel, DNS means writing records at somebody else's
// nameserver through its API — Cloudflare's, Porkbun's — and the provider is
// the record of what exists. Here the panel *is* the nameserver: the zones live
// in its own tables, BIND answers queries from zone files generated out of
// them, and whoever owns a domain points its NS records at this machine.
//
// That is the difference between managing DNS and hosting it, and it is the
// difference between needing an account somewhere and needing two names at a
// registrar. Which is why the bulk of what this package is for is not writing
// zone files, it is Status: the operator has to be told exactly what to
// register where, and then shown whether it worked.
package nameserver

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// Where the generated configuration goes.
//
// Everything the panel writes is under its own names, and the packaged files
// are left alone apart from one `include` line appended to named.conf.local.
// A machine where somebody has also configured BIND by hand keeps working, and
// removing the panel's DNS is removing the panel's files.
const (
	// ConfigPath holds a `zone` block per hosted zone.
	ConfigPath = "/etc/bind/ubuntu-panel.conf"

	// ZoneDir holds one file per zone.
	ZoneDir = "/etc/bind/panel-zones"

	// OptionsPath is BIND's own options file, which the panel replaces: the
	// packaged default is a caching resolver for the local network, and that is
	// the opposite of what an authoritative public nameserver must be.
	OptionsPath = "/etc/bind/named.conf.options"

	// LocalPath is where the include line goes.
	LocalPath = "/etc/bind/named.conf.local"

	// IncludeLine is what is appended to LocalPath.
	IncludeLine = `include "/etc/bind/ubuntu-panel.conf";`
)

// Settings is the operator's half of the arrangement: the names the world will
// know this nameserver by, and the mailbox in every zone's SOA.
type Settings struct {
	// NS1 and NS2 are hostnames, not addresses — a delegation names
	// nameservers, and their addresses come from the glue the registrar holds.
	NS1 string `json:"ns1"`
	NS2 string `json:"ns2"`

	Hostmaster string `json:"hostmaster"`
}

// Nameservers is the pair, with anything unset dropped.
func (s Settings) Nameservers() []string {
	var list []string

	for _, name := range []string{s.NS1, s.NS2} {
		if name = store.NormaliseZoneName(name); name != "" {
			list = append(list, name)
		}
	}

	return list
}

// Configured reports whether there is enough to serve a zone: one nameserver
// name is the minimum BIND will load a zone with.
func (s Settings) Configured() bool { return len(s.Nameservers()) > 0 }

// Setting keys. Named for what they are rather than for the page they are on,
// because the mail server's hostname setting is next to them and the two get
// confused otherwise.
const (
	settingNS1        = "nameserver_ns1"
	settingNS2        = "nameserver_ns2"
	settingHostmaster = "nameserver_hostmaster"
)

// CurrentSettings reads them.
func CurrentSettings(st *store.Store) Settings {
	return Settings{
		NS1:        st.Setting(settingNS1, ""),
		NS2:        st.Setting(settingNS2, ""),
		Hostmaster: st.Setting(settingHostmaster, ""),
	}
}

// SaveSettings stores them, normalised.
func SaveSettings(st *store.Store, settings Settings) {
	save := func(key, value string) {
		if value == "" {
			st.ForgetSetting(key)
			return
		}

		st.SetSetting(key, value)
	}

	save(settingNS1, store.NormaliseZoneName(settings.NS1))
	save(settingNS2, store.NormaliseZoneName(settings.NS2))
	save(settingHostmaster, strings.ToLower(strings.TrimSpace(settings.Hostmaster)))
}

// SuggestedSettings is what the setup form is pre-filled with: ns1 and ns2 on
// whatever domain the panel is already reached at.
//
// A suggestion rather than a default. The nameservers have to be named on a
// domain the operator controls at a registrar that will take glue records, and
// the panel cannot know that the domain it is served on is one of those — but
// it very often is, and typing it again is work.
func SuggestedSettings(st *store.Store) Settings {
	domain := store.NormaliseZoneName(st.Setting("panel_domain", ""))
	if domain == "" {
		return Settings{}
	}

	// A panel reached at panel.example.com names its nameservers on
	// example.com, not on panel.example.com: the registry's glue is held
	// against the registered domain, and ns1.panel.example.com is a name
	// inside a zone rather than one the registrar knows about.
	return Settings{
		NS1:        "ns1." + registrable(domain),
		NS2:        "ns2." + registrable(domain),
		Hostmaster: "hostmaster@" + registrable(domain),
	}
}

// registrable is the last two labels of a name, which is the registered domain
// for the great majority of them.
//
// Deliberately not a public-suffix list. This is a suggestion in a form field
// somebody reads before saving, and getting `example.co.uk` wrong there costs
// an edit; carrying a copy of the PSL around and keeping it current costs more
// than that every year.
func registrable(domain string) string {
	labels := strings.Split(domain, ".")
	if len(labels) <= 2 {
		return domain
	}

	return strings.Join(labels[len(labels)-2:], ".")
}

// ------------------------------------------------------------------ status

// Status is the whole of what the setup page shows, and the order of its fields
// is the order the work has to happen in.
type Status struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`

	// Answering is whether BIND on this machine replies to a query for one of
	// its own zones. Installed and running and still not answering is a
	// configuration BIND loaded and then refused, which is worth separating.
	Answering bool `json:"answering"`

	Settings  Settings `json:"settings"`
	Suggested Settings `json:"suggested"`

	// IP is the address the glue records must carry.
	IP string `json:"ip"`

	// Glue is what the world currently thinks the nameserver names resolve to,
	// which is the one thing an operator cannot check from inside the machine
	// without asking a public resolver.
	Glue []GlueCheck `json:"glue"`

	ZoneCount int `json:"zone_count"`

	// LastError is BIND's own words from the last failed apply.
	LastError string `json:"last_error"`
}

// Ready reports whether the nameserver is in a state where pointing a domain
// at it would work.
func (s Status) Ready() bool {
	return s.Installed && s.Running && s.Settings.Configured()
}

// GlueCheck is one nameserver name, what it resolves to publicly, and whether
// that is this machine.
type GlueCheck struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Matches   bool     `json:"matches"`
}

// Resolves reports whether the name answers with anything at all.
func (g GlueCheck) Resolves() bool { return len(g.Addresses) > 0 }

// Installed reports whether BIND is on the machine.
func Installed() bool { return proc.Which("named") }

// Running reports whether it is up. The unit is `named` on current Ubuntu and
// `bind9` on older releases, and the two are aliases of each other — asking
// about both is cheaper than working out which release this is.
func Running() bool {
	return proc.Ok("systemctl is-active --quiet named || systemctl is-active --quiet bind9")
}

// Describe gathers the status. The public resolver lookups are the slow part,
// so they are done concurrently and only when there is something to look up.
func Describe(ctx context.Context, st *store.Store) Status {
	status := Status{
		Installed: Installed(),
		Settings:  CurrentSettings(st),
		Suggested: SuggestedSettings(st),
		IP:        system.PublicIP(),
		ZoneCount: st.DNSZoneCount(ctx),
		LastError: st.Setting("nameserver_error", ""),
	}

	if !status.Installed {
		return status
	}

	status.Running = Running()
	status.Answering = status.Running && answering(ctx, st)

	for _, name := range status.Settings.Nameservers() {
		status.Glue = append(status.Glue, checkGlue(ctx, name, status.IP))
	}

	return status
}

// answering asks the local BIND for the SOA of a zone it is supposed to hold.
//
// A nameserver that is running and does not answer for its own zone is the
// failure this catches: a zone file BIND rejected, or an `include` line that
// never made it into named.conf. Both leave the service up and every query
// about the zone refused.
func answering(ctx context.Context, st *store.Store) bool {
	zones, err := st.DNSZones(ctx, 0)
	if err != nil || len(zones) == 0 {
		// Nothing to ask about. The server is up, so there is nothing to
		// report as broken.
		return true
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 3 * time.Second}

			return dialer.DialContext(ctx, network, "127.0.0.1:53")
		},
	}

	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err = resolver.LookupNS(lookup, zones[0].Name)

	return err == nil
}

// checkGlue asks a public resolver what a nameserver name answers with.
func checkGlue(ctx context.Context, name, expected string) GlueCheck {
	check := GlueCheck{Name: name}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}

			return dialer.DialContext(ctx, network, "1.1.1.1:53")
		},
	}

	lookup, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	addresses, err := resolver.LookupHost(lookup, name)
	if err != nil {
		return check
	}

	sort.Strings(addresses)
	check.Addresses = addresses

	for _, address := range addresses {
		if address == expected && expected != "" {
			check.Matches = true
		}
	}

	return check
}

// ----------------------------------------------------------- what to write

// namedConf is the `zone` block per hosted zone.
func namedConf(zones []*store.DNSZone) string {
	var out strings.Builder

	out.WriteString("// Written by Ubuntu Panel. Edited here, lost on the next change.\n")
	out.WriteString("//\n")
	out.WriteString("// One block per zone this machine is authoritative for. The files they\n")
	out.WriteString("// name are generated from the panel's own records.\n\n")

	for _, zone := range zones {
		name := store.NormaliseZoneName(zone.Name)
		if name == "" {
			continue
		}

		fmt.Fprintf(&out, "zone \"%s\" {\n", name)
		out.WriteString("    type master;\n")
		fmt.Fprintf(&out, "    file \"%s\";\n", ZoneFilePath(name))

		// No dynamic updates and no transfers by default. A zone that will
		// hand its whole contents to anybody who asks is a list of every host
		// somebody runs, and the panel has no secondary to serve.
		out.WriteString("    allow-transfer { none; };\n")
		out.WriteString("    allow-update { none; };\n")
		out.WriteString("};\n\n")
	}

	return out.String()
}

// ZoneFilePath is where a zone's file lives.
func ZoneFilePath(zone string) string {
	return ZoneDir + "/db." + store.NormaliseZoneName(zone)
}

// options is BIND's options block, written to replace the packaged one.
//
// The packaged default is a recursive resolver for the local network. Leaving
// it in place and adding zones to it produces an open resolver on a public
// address, which is not a slightly worse configuration — it is a machine that
// will be found within days and used to reflect gigabits of traffic at
// somebody else, until the host takes it off the network. Every line below is
// there to make this an authoritative server and nothing else.
func options() string {
	return `// Written by Ubuntu Panel. Edited here, lost on the next upgrade.
//
// An authoritative nameserver, not a resolver. See recursion, below.

options {
    directory "/var/cache/bind";

    // Answer for the zones this machine holds, and for nothing else.
    //
    // A nameserver that resolves arbitrary names for arbitrary clients is an
    // open resolver: a short query produces a long answer, the source address
    // of a UDP query cannot be verified, and that arithmetic is what DNS
    // amplification attacks are made of. The machine would be scanned, found,
    // and used to attack somebody else.
    recursion no;
    allow-recursion { none; };
    allow-query { any; };
    allow-query-cache { none; };

    // A cap on how hard one client can make this server work, so a flood of
    // queries for one zone cannot be turned into a flood of answers at a
    // forged source address.
    rate-limit {
        responses-per-second 20;
        referrals-per-second 10;
        nodata-per-second 10;
        nxdomains-per-second 10;
        errors-per-second 10;
        slip 2;
    };

    // Nothing here needs to know which BIND this is.
    version none;
    hostname none;
    server-id none;

    listen-on { any; };
    listen-on-v6 { any; };

    dnssec-validation no;
};

logging {
    // To syslog rather than to a file of its own, so journalctl -u named has
    // it and there is no directory to create, to own, or to be refused.
    //
    // A log channel BIND cannot open is not a missing log — it is a
    // configuration BIND rejects, and a nameserver that will not start. The
    // file channel this used to be depended on AppArmor's named profile
    // permitting a path, which is not a thing to bet a machine's DNS on.
    category default { default_syslog; default_debug; };

    // Every query, which is every visitor to every site on the machine, at a
    // few hundred bytes each. Off.
    category queries { null; };
};
`
}

// ------------------------------------------------------------- delegation

// Delegation is what the public DNS currently says about who serves a zone.
type Delegation struct {
	// Nameservers is what the parent zone hands out, in whatever order it
	// came back.
	Nameservers []string `json:"nameservers"`

	// Delegated is whether every name this panel is configured with appears in
	// that answer. Partially delegated is not delegated: a resolver picks a
	// nameserver at random, so a domain listing one of ours and one of
	// somebody else's resolves correctly about half the time, which is the
	// hardest kind of DNS problem to be told about.
	Delegated bool `json:"delegated"`

	Error string `json:"error"`
}

// CheckDelegation asks a public resolver who serves a zone.
//
// This is the question that tells an operator whether the work outside the
// panel actually happened. Everything else on the setup page is a fact about
// this machine; a delegation is a fact about a registrar, and the only way to
// learn it is to ask somebody else.
func CheckDelegation(ctx context.Context, zone string, settings Settings) Delegation {
	zone = store.NormaliseZoneName(zone)

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}

			return dialer.DialContext(ctx, network, "1.1.1.1:53")
		},
	}

	lookup, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	answers, err := resolver.LookupNS(lookup, zone)
	if err != nil {
		return Delegation{Error: "Nothing answers for " + zone + " yet. " +
			"A delegation just changed at the registrar can take a few hours to " +
			"be visible everywhere."}
	}

	delegation := Delegation{}

	published := map[string]bool{}

	for _, answer := range answers {
		name := store.NormaliseZoneName(answer.Host)

		delegation.Nameservers = append(delegation.Nameservers, name)
		published[name] = true
	}

	sort.Strings(delegation.Nameservers)

	wanted := settings.Nameservers()
	if len(wanted) == 0 {
		return delegation
	}

	delegation.Delegated = true

	for _, name := range wanted {
		if !published[name] {
			delegation.Delegated = false
		}
	}

	return delegation
}
