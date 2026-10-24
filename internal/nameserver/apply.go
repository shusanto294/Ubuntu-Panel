package nameserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// ErrNotInstalled is the nameserver not being on the machine yet. Its own error
// because it is the one failure that is not a fault: the DNS server is an app
// in the catalogue like any other, and a zone added before it is installed is a
// row waiting for a package rather than a mistake.
var ErrNotInstalled = errors.New("the DNS server is not installed on this machine")

// ErrNotConfigured is zones existing with no nameserver names to put in them.
var ErrNotConfigured = errors.New(
	"the nameserver hostnames have not been set, so no zone can be served")

// Apply writes the whole configuration from the panel's own rows and reloads
// BIND.
//
// Everything, every time: the options file, the include line, one zone block
// per zone and one file per zone. Generating the lot is both simpler to reason
// about than working out what changed and the only way a zone deleted from the
// panel actually stops being answered for — a `zone` block left behind in a
// file nobody regenerated is a name this machine still claims authority over.
func Apply(ctx context.Context, st *store.Store) error {
	err := apply(ctx, st, 0)

	remember(st, err)

	return err
}

// ApplyZone is one zone's file and a reload of that zone alone.
//
// What record edits go through. The whole-configuration write is a dozen
// commands per zone and a machine hosting fifty of them would spend a visible
// second on every record somebody adds, so a change that cannot alter the set
// of zones does not rewrite the set of zones.
func ApplyZone(ctx context.Context, st *store.Store, zoneID int64) error {
	// Unless there is no configuration to add a zone to. A zone added while
	// the package was still being installed, or a BIND reinstalled over the
	// top, leaves the panel's rows right and this machine holding no zone
	// blocks at all — and a single-zone write would reload a zone BIND has
	// never been told about.
	if !proc.Exists(ConfigPath, true) {
		zoneID = 0
	}

	err := apply(ctx, st, zoneID)

	remember(st, err)

	return err
}

// remember keeps the last failure where the status page can read it, and clears
// it on success. A reload that failed is invisible otherwise: the panel's own
// rows are right, and the machine is answering with the zone as it was before.
func remember(st *store.Store, err error) {
	if err == nil || errors.Is(err, ErrNotInstalled) {
		st.ForgetSetting("nameserver_error")
		return
	}

	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}

	st.SetSetting("nameserver_error", message)
}

func apply(ctx context.Context, st *store.Store, onlyZone int64) error {
	if !Installed() {
		return ErrNotInstalled
	}

	settings := CurrentSettings(st)
	nameservers := settings.Nameservers()

	zones, err := st.DNSZones(ctx, 0)
	if err != nil {
		return fmt.Errorf("could not read the hosted zones: %w", err)
	}

	if len(zones) > 0 && len(nameservers) == 0 {
		return ErrNotConfigured
	}

	if err := prepareDirectories(); err != nil {
		return err
	}

	// A whole-configuration write; a single-zone one leaves these alone,
	// because neither can have changed.
	if onlyZone == 0 {
		if err := proc.PutFile(OptionsPath, options(), true); err != nil {
			return err
		}

		if err := ensureInclude(); err != nil {
			return err
		}

		if err := proc.PutFile(ConfigPath, namedConf(zones), true); err != nil {
			return err
		}
	}

	written := 0

	for _, zone := range zones {
		if onlyZone != 0 && zone.ID != onlyZone {
			continue
		}

		if err := writeZone(ctx, st, zone, nameservers, settings.Hostmaster); err != nil {
			return err
		}

		written++
	}

	if onlyZone != 0 && written == 0 {
		// The zone was deleted between the edit and the write. Nothing to do
		// here; whoever deleted it rewrote the whole configuration.
		return nil
	}

	return reload(onlyZone, zones)
}

// prepareDirectories makes the place the zone files go.
//
// Under /etc/bind rather than anywhere more obvious, because AppArmor's profile
// for named permits reading /etc/bind/** and named is confined by it. A zone
// file somewhere tidier is a zone file BIND is not allowed to open.
func prepareDirectories() error {
	command := "sudo mkdir -p " + proc.Quote(ZoneDir) + " && sudo chmod 755 " +
		proc.Quote(ZoneDir)

	if result := proc.Run(command, time.Minute); !result.OK() {
		return fmt.Errorf("could not prepare %s: %s", ZoneDir, result.Output)
	}

	return nil
}

// ensureInclude appends the panel's include line to named.conf.local, once.
//
// Appended rather than written: named.conf.local is where somebody who
// configured BIND by hand on this machine put their own zones, and replacing
// it would quietly unpublish them.
func ensureInclude() error {
	existing, err := proc.ReadFile(LocalPath, true)
	if err != nil {
		// A machine where the file does not exist at all. Writing it with only
		// the include line is correct.
		existing = ""
	}

	if strings.Contains(existing, IncludeLine) {
		return nil
	}

	updated := strings.TrimRight(existing, "\n")

	if updated != "" {
		updated += "\n\n"
	}

	updated += "// The panel's zones. Added by Ubuntu Panel; the file it names is generated.\n" +
		IncludeLine + "\n"

	return proc.PutFile(LocalPath, updated, true)
}

// writeZone bumps the zone's serial, renders it and writes it — then has BIND's
// own checker read it back before anything is reloaded.
//
// The check is the point. A zone file BIND will not parse does not fail at the
// reload in a way anybody sees: `rndc reload` succeeds, BIND keeps serving the
// previous contents of that zone, and the only trace is a line in its log. So
// the file is validated where the operator is standing, and a record that would
// have produced an unloadable zone is a visible error on the form rather than a
// change that appeared to work and did nothing.
func writeZone(ctx context.Context, st *store.Store, zone *store.DNSZone,
	nameservers []string, hostmaster string) error {
	records, err := st.DNSZoneRecords(ctx, zone.ID)
	if err != nil {
		return fmt.Errorf("could not read the records for %s: %w", zone.Name, err)
	}

	zone.Serial = st.BumpDNSZoneSerial(ctx, zone.ID)

	path := ZoneFilePath(zone.Name)

	if err := proc.PutFile(path, Render(zone, records, nameservers, hostmaster), true); err != nil {
		return err
	}

	check := proc.Run("sudo named-checkzone "+proc.Quote(store.NormaliseZoneName(zone.Name))+
		" "+proc.Quote(path), time.Minute)

	if !check.OK() {
		return fmt.Errorf("%s is not a zone BIND will load: %s", zone.Name,
			firstLines(check.Output, 6))
	}

	return nil
}

// reload tells the running BIND to pick the change up.
//
// Two different commands for two different changes. A zone whose file was
// rewritten needs that zone re-read; a configuration whose set of zones
// changed needs BIND to notice a zone it had never heard of, which `reload`
// alone does not do.
func reload(onlyZone int64, zones []*store.DNSZone) error {
	if !Running() {
		if result := proc.Run("sudo systemctl enable --now named 2>/dev/null || "+
			"sudo systemctl enable --now bind9", 2*time.Minute); !result.OK() {
			return fmt.Errorf("the DNS server would not start: %s", firstLines(result.Output, 8))
		}

		return nil
	}

	if check := proc.Run("sudo named-checkconf", time.Minute); !check.OK() {
		return fmt.Errorf("BIND rejected the configuration: %s", firstLines(check.Output, 6))
	}

	command := "sudo rndc reconfig && sudo rndc reload"

	if onlyZone != 0 {
		for _, zone := range zones {
			if zone.ID == onlyZone {
				command = "sudo rndc reload " +
					proc.Quote(store.NormaliseZoneName(zone.Name))

				break
			}
		}
	}

	result := proc.Run(command, 2*time.Minute)
	if result.OK() {
		return nil
	}

	// rndc needs its key and a listening control channel, and a machine where
	// either is missing is one where the reload has to be the service's own.
	// Reloading rather than restarting first, because a restart drops every
	// query in flight.
	if fallback := proc.Run("sudo systemctl reload named 2>/dev/null || "+
		"sudo systemctl reload bind9 2>/dev/null || "+
		"sudo systemctl restart named 2>/dev/null || "+
		"sudo systemctl restart bind9", 2*time.Minute); fallback.OK() {
		return nil
	}

	return fmt.Errorf("the DNS server would not take the new configuration: %s",
		firstLines(result.Output, 8))
}

// firstLines trims a command's output to something that fits in a flash
// message. BIND's complaints put the useful line first.
func firstLines(output string, count int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) > count {
		lines = lines[:count]
	}

	return strings.Join(lines, " · ")
}

// DefaultRecords is what a new zone is created holding.
//
// A zone with nothing in it but its SOA and NS records is a domain that
// resolves to nothing — which is a working zone, and indistinguishable to
// whoever added it from a zone that did not work. The apex and `www` pointing
// at this machine is what they almost certainly came to do, and the records are
// ordinary rows they can change or delete.
func DefaultRecords(zone string, ip string) []*store.DNSZoneRecord {
	zone = store.NormaliseZoneName(zone)

	if ip == "" {
		return nil
	}

	return []*store.DNSZoneRecord{
		{Type: "A", Name: zone, Content: ip, Priority: -1},
		{Type: "A", Name: "www." + zone, Content: ip, Priority: -1},
	}
}

// NameserverRecords are the A records a zone needs when it is the zone the
// nameservers themselves are named in.
//
// Adding example.com here while the nameservers are ns1.example.com and
// ns2.example.com means this zone has to answer for those names: the glue at
// the registry is what gets a resolver to this machine the first time, and
// after that the resolver asks this machine for the addresses and believes
// what it says. A zone that delegates to names it does not hold A records for
// is the "lame delegation" every DNS checker reports and some registries
// refuse outright.
func NameserverRecords(zone string, settings Settings, ip string) []*store.DNSZoneRecord {
	zone = store.NormaliseZoneName(zone)

	if ip == "" {
		return nil
	}

	var records []*store.DNSZoneRecord

	for _, name := range settings.Nameservers() {
		if name == zone || strings.HasSuffix(name, "."+zone) {
			records = append(records, &store.DNSZoneRecord{
				Type: "A", Name: name, Content: ip, Priority: -1,
			})
		}
	}

	return records
}
