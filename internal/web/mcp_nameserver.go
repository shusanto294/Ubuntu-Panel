package web

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/nameserver"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// The panel's own nameserver, as tools.
//
// The records in a hosted zone need nothing here: a hosted zone is reached
// through the same credential and the same set_dns_record as Cloudflare's, so
// an assistant that can write a record at a provider can write one here
// already. What is missing without these is the step before that — deciding to
// host a zone at all, and being told what has to be registered at a registrar
// before it answers.

func (a *App) mcpNameserverStatus() *mcp.Tool {
	return &mcp.Tool{
		Name:       "nameserver_status",
		Capability: capSitesRead,
		Title:      "Read this server's own DNS status",
		ReadOnly:   true,
		Description: "Whether this machine is itself an authoritative nameserver, and what " +
			"still has to happen before a domain pointed at it resolves. Reports the DNS " +
			"server package, the nameserver hostnames, this machine's address, and what the " +
			"public DNS currently says those hostnames resolve to — which is the part that " +
			"depends on glue records registered at a registrar and cannot be done from here.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			status := nameserver.Describe(ctx, a.Store)

			glue := make([]map[string]any, 0, len(status.Glue))
			for _, check := range status.Glue {
				glue = append(glue, map[string]any{
					"hostname":    check.Name,
					"resolves":    check.Addresses,
					"points_here": check.Matches,
				})
			}

			return map[string]any{
				"installed":   status.Installed,
				"running":     status.Running,
				"answering":   status.Answering,
				"ready":       status.Ready(),
				"ip":          status.IP,
				"nameservers": status.Settings.Nameservers(),
				"hostmaster":  status.Settings.Hostmaster,
				"zone_count":  status.ZoneCount,
				"glue":        glue,
				"last_error":  nullable(status.LastError),
				"next_step":   nameserverNextStep(status),
			}, nil
		},
	}
}

// nameserverNextStep is the one thing to do next, in words.
//
// An assistant reading six booleans will work out an answer and sometimes work
// out the wrong one — recommending a zone be added to a machine with no DNS
// server on it, or telling somebody their delegation is broken when what is
// missing is a glue record. The panel knows the order these happen in, so it
// says.
func nameserverNextStep(status nameserver.Status) string {
	switch {
	case !status.Installed:
		return "Install the `dns` app, which puts BIND on this machine."

	case !status.Settings.Configured():
		return "Set the nameserver hostnames with set_nameserver_hostnames — two names " +
			"on a domain the operator owns, conventionally ns1 and ns2."

	case !status.Running:
		return "The DNS server is installed but not running; its install log says why."

	case len(status.Glue) > 0 && !status.Glue[0].Matches:
		return "Register the nameserver hostnames as glue records at the registrar of " +
			"the domain they are named on, each pointing at " + status.IP +
			". Only the operator can do this, and nothing resolves until it is done."

	case status.ZoneCount == 0:
		return "Host a zone with host_dns_zone, starting with the domain the " +
			"nameservers are named on."

	default:
		return "Set up. A domain whose registrar points it at these nameservers will " +
			"be answered for by this machine."
	}
}

func (a *App) mcpSetNameserverHostnames() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_nameserver_hostnames",
		Capability: capSystem,
		Title:      "Name this server's nameservers",
		Description: "Set the hostnames this machine will be known by as a nameserver, and " +
			"the address in every zone's SOA. Every hosted zone is rewritten. These names " +
			"have to be registered as glue records at the registrar of the domain they are " +
			"on before any domain can be delegated to them — nameserver_status reports " +
			"whether that has happened.",
		Properties: map[string]mcp.Property{
			"ns1": {Description: "The first nameserver's hostname, such as ns1.example.com."},
			"ns2": {Description: "The second. Optional here; most registries require two."},
			"hostmaster": {Description: "The address responsible for the zones, for the SOA. " +
				"Defaults to hostmaster@ the nameserver domain."},
		},
		Required: []string{"ns1"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			settings := nameserver.Settings{
				NS1:        store.NormaliseZoneName(args.String("ns1")),
				NS2:        store.NormaliseZoneName(args.StringOr("ns2", "")),
				Hostmaster: strings.ToLower(strings.TrimSpace(args.StringOr("hostmaster", ""))),
			}

			if reason := invalidHostname(settings.NS1); reason != "" {
				return nil, errors.New(reason)
			}

			if settings.NS2 != "" {
				if reason := invalidHostname(settings.NS2); reason != "" {
					return nil, errors.New(reason)
				}

				if settings.NS2 == settings.NS1 {
					return nil, errors.New("the two nameservers need different names")
				}
			}

			nameserver.SaveSettings(a.Store, settings)

			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			if _, err := a.EnsureLocalDNSAccount(ctx, user.ID); err != nil {
				return nil, err
			}

			result := map[string]any{
				"nameservers":              settings.Nameservers(),
				"ip":                       system.PublicIP(),
				"glue_records_to_register": glueInstructions(settings, system.PublicIP()),
			}

			if err := nameserver.Apply(ctx, a.Store); err != nil {
				if errors.Is(err, nameserver.ErrNotInstalled) {
					result["note"] = "Saved. The DNS server is not installed yet; " +
						"install the `dns` app and the zones are written then."

					return result, nil
				}

				return nil, err
			}

			result["note"] = "Saved and every zone rewritten."

			return result, nil
		},
	}
}

// glueInstructions is the rows somebody has to add at a registrar, spelled out
// rather than described.
//
// An assistant asked to set this up will otherwise paraphrase, and a
// paraphrased glue record is a hostname and an address somebody types slightly
// wrong into a form they will not look at again for a week.
func glueInstructions(settings nameserver.Settings, ip string) []map[string]string {
	rows := make([]map[string]string, 0, 2)

	for _, name := range settings.Nameservers() {
		rows = append(rows, map[string]string{
			"hostname": name,
			"ipv4":     ip,
			"where": "At the registrar of the domain this hostname is on, on the screen " +
				"called glue records, host records, child nameservers, private " +
				"nameservers or \"register a nameserver\" — they are all the same thing.",
		})
	}

	return rows
}

func (a *App) mcpListHostedZones() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_hosted_zones",
		Capability: capSitesRead,
		Title:      "List the zones this server answers for",
		ReadOnly:   true,
		Description: "The DNS zones hosted on this machine itself, as opposed to the ones at " +
			"a connected provider. list_dns_zones is the other one, and asks a provider.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			zones, err := a.Store.DNSZones(ctx, user.ID)
			if err != nil {
				return nil, err
			}

			rows := make([]map[string]any, 0, len(zones))

			for _, zone := range zones {
				rows = append(rows, map[string]any{
					"id":      zone.ID,
					"name":    zone.Name,
					"records": zone.Records,
					"serial":  zone.Serial,
				})
			}

			return map[string]any{
				"zones":       rows,
				"nameservers": nameserver.CurrentSettings(a.Store).Nameservers(),
			}, nil
		},
	}
}

func (a *App) mcpHostDNSZone() *mcp.Tool {
	return &mcp.Tool{
		Name:       "host_dns_zone",
		Capability: capSystem,
		Title:      "Host a DNS zone on this server",
		Description: "Make this machine the authoritative nameserver for a domain. The zone " +
			"is created with the apex and www pointing at this server, and the panel can " +
			"then publish records for it the same way it does at any provider. The domain's " +
			"nameservers still have to be changed to this server's at the domain's own " +
			"registrar — do this first, because pointing a domain at a zone that does not " +
			"exist here yields a cached \"no such domain\".",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain itself, such as example.com — not a subdomain of it."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			name := store.NormaliseZoneName(args.String("domain"))

			if reason := invalidZoneName(name); reason != "" {
				return nil, errors.New(reason)
			}

			if _, err := a.Store.FindDNSZoneByName(ctx, name); err == nil {
				return nil, fmt.Errorf("%s is already hosted on this server", name)
			}

			settings := nameserver.CurrentSettings(a.Store)

			if !settings.Configured() {
				return nil, errors.New("the nameserver hostnames have not been set — " +
					"set_nameserver_hostnames first, because a zone has to name the " +
					"servers that answer for it")
			}

			zoneID, err := a.Store.CreateDNSZone(ctx, user.ID, name)
			if err != nil || zoneID <= 0 {
				if err == nil {
					err = errors.New("the zone was not created")
				}

				return nil, err
			}

			ip := system.PublicIP()

			wanted := nameserver.DefaultRecords(name, ip)
			wanted = append(wanted, nameserver.NameserverRecords(name, settings, ip)...)

			for _, record := range wanted {
				record.ZoneID = zoneID
				a.Store.CreateDNSZoneRecord(ctx, record)
			}

			if _, err := a.EnsureLocalDNSAccount(ctx, user.ID); err != nil {
				return nil, err
			}

			result := map[string]any{
				"id":          zoneID,
				"name":        name,
				"records":     len(wanted),
				"pointing_at": ip,
				"delegate_to": settings.Nameservers(),
				"next_step": fmt.Sprintf("At %s's registrar, change its nameservers to %s.",
					name, strings.Join(settings.Nameservers(), " and ")),
			}

			if err := nameserver.Apply(ctx, a.Store); err != nil {
				if errors.Is(err, nameserver.ErrNotInstalled) {
					result["note"] = "The zone was created. The DNS server is not " +
						"installed yet, so nothing is answering for it."

					return result, nil
				}

				return nil, err
			}

			return result, nil
		},
	}
}

func (a *App) mcpRemoveHostedZone() *mcp.Tool {
	return &mcp.Tool{
		Name:        "remove_hosted_zone",
		Capability:  capSystem,
		Title:       "Stop hosting a DNS zone",
		Destructive: true,
		Description: "Stop answering for a domain and delete its records from this panel. " +
			"Every name in the zone stops resolving within its TTL, including any mail the " +
			"domain receives — so a domain still delegated here goes dark. Point it at " +
			"another nameserver first.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The zone's domain."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			name := store.NormaliseZoneName(args.String("domain"))

			zone, err := a.Store.FindDNSZoneByName(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("%s is not hosted on this server", name)
			}

			if err := a.Store.DeleteDNSZone(ctx, zone.ID, user.ID); err != nil {
				return nil, err
			}

			if err := nameserver.Apply(ctx, a.Store); err != nil &&
				!errors.Is(err, nameserver.ErrNotInstalled) {
				return nil, err
			}

			return map[string]any{
				"removed": name,
				"note": "This server no longer claims authority for " + name +
					". Anything still pointing its nameservers here stops resolving.",
			}, nil
		},
	}
}
