package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The DNS page, as tools: connecting a provider, and reading and writing the
// records in its zones.
//
// A site's own records are not this — create_site publishes those and
// delete_site takes them away again, and a record written here for a domain the
// panel manages is one the next deployment will overwrite. This is for
// everything else in a zone: a mail record somebody wants adding by hand, a
// verification TXT, a subdomain pointing somewhere off this machine.

// mcpDNSAccount resolves the `dns_account` argument: an id, or the label the
// credential was given.
func (a *App) mcpDNSAccount(ctx context.Context, args mcp.Args) (*store.DNSAccount, error) {
	user := mcpUser(ctx)
	if user == nil {
		return nil, errors.New("this key is not attached to an account")
	}

	reference := args.StringOr("dns_account", args.String("account"))

	if reference == "" && args.Int("dns_account_id") > 0 {
		reference = strconv.FormatInt(args.Int("dns_account_id"), 10)
	}

	accounts, err := a.Store.DNSAccounts(ctx, user.ID)
	if err != nil {
		return nil, errors.New("the panel could not read its DNS credentials")
	}

	if reference == "" {
		if len(accounts) == 1 {
			return accounts[0], nil
		}

		return nil, errors.New("name the credential in `dns_account`, by id or by label — " +
			"list_dns_accounts has them")
	}

	if id, err := strconv.ParseInt(reference, 10, 64); err == nil {
		account, err := a.Store.FindDNSAccount(ctx, id, user.ID)
		if err != nil {
			return nil, fmt.Errorf("no DNS credential with id %d on this account", id)
		}

		return account, nil
	}

	for _, account := range accounts {
		if strings.EqualFold(account.Label, reference) ||
			strings.EqualFold(account.Provider, reference) {
			return account, nil
		}
	}

	return nil, fmt.Errorf("no DNS credential called %s — list_dns_accounts has them", reference)
}

// mcpZone finds the zone a record belongs in.
//
// The credential when one was named, and otherwise whichever connected one
// holds the hostname — which is what makes "add a TXT record for example.com"
// one call rather than three.
func (a *App) mcpZone(ctx context.Context, args mcp.Args) (*store.DNSAccount, string, string, error) {
	hostname := strings.ToLower(strings.TrimRight(args.StringOr("zone",
		args.StringOr("domain", args.String("name"))), "."))

	if hostname == "" {
		return nil, "", "", errors.New("name the zone or the record's hostname in `zone`")
	}

	named := args.String("dns_account") != "" || args.String("account") != "" ||
		args.Int("dns_account_id") > 0

	if named {
		account, err := a.mcpDNSAccount(ctx, args)
		if err != nil {
			return nil, "", "", err
		}

		zoneID, zoneName, err := dns.New(account).FindZone(hostname)
		if err != nil {
			return nil, "", "", err
		}

		return account, zoneID, zoneName, nil
	}

	user := mcpUser(ctx)
	if user == nil {
		return nil, "", "", errors.New("this key is not attached to an account")
	}

	accounts, err := a.Store.DNSAccounts(ctx, user.ID)
	if err != nil || len(accounts) == 0 {
		return nil, "", "", errors.New("no DNS provider is connected to this panel — " +
			"connect_dns_account adds one, and the panel's DNS page is the other way in")
	}

	for _, account := range accounts {
		if zoneID, zoneName, err := dns.New(account).FindZone(hostname); err == nil {
			return account, zoneID, zoneName, nil
		}
	}

	return nil, "", "", fmt.Errorf("no credential connected to this panel holds a zone for %s",
		hostname)
}

func (a *App) mcpConnectDNSAccount() *mcp.Tool {
	return &mcp.Tool{
		Name:       "connect_dns_account",
		Capability: capDNS,
		Title:      "Connect a DNS provider",
		Description: "Store an API credential for a DNS provider, so the panel can publish a " +
			"site's or a mail domain's records itself. The credential is tried before it is " +
			"stored: one that does not work is refused here rather than halfway through a " +
			"deployment. list_dns_accounts says which providers are connected already, and " +
			"list_dns_providers what this panel supports.",
		Properties: map[string]mcp.Property{
			"provider":   {Description: "The provider key, from list_dns_providers."},
			"label":      {Description: "A name for this credential, so a person can tell two apart."},
			"api_token":  {Description: "The API token or key."},
			"api_secret": {Description: "The secret, for the providers that issue a pair."},
			"email":      {Description: "The account email, where the provider needs one."},
		},
		Required: []string{"provider", "api_token"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			provider := strings.ToLower(args.String("provider"))

			if !dns.ProviderExists(provider) {
				return nil, fmt.Errorf("%q is not a DNS provider this panel supports — "+
					"list_dns_providers has them", provider)
			}

			token := args.String("api_token")
			secret := args.String("api_secret")

			if token == "" {
				return nil, errors.New("`api_token` is the credential itself")
			}

			if dns.NeedsSecret(provider) && secret == "" {
				return nil, errors.New(dns.ProviderLabel(provider) + " needs both a key and a secret")
			}

			account := &store.DNSAccount{
				UserID:    user.ID,
				Provider:  provider,
				Label:     args.StringOr("label", dns.ProviderLabel(provider)),
				APIToken:  token,
				APISecret: secret,
				Email:     args.String("email"),
			}

			detail, err := dns.New(account).Verify()
			if err != nil {
				return nil, fmt.Errorf("%s rejected this credential: %w",
					dns.ProviderLabel(provider), err)
			}

			id, err := a.Store.CreateDNSAccount(ctx, account)
			if err != nil || id <= 0 {
				a.Log.Error("could not store a DNS credential for an MCP call", "error", err)

				return nil, errors.New("the panel could not store the credential")
			}

			a.Store.MarkDNSAccountVerified(id, true)

			return map[string]any{
				"dns_account_id": id,
				"provider":       provider,
				"provider_label": dns.ProviderLabel(provider),
				"label":          account.Label,
				"verified":       true,
				"detail":         detail,
			}, nil
		},
	}
}

func (a *App) mcpListDNSProviders() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_dns_providers",
		Capability:  capSitesRead,
		Title:       "List supported DNS providers",
		ReadOnly:    true,
		Description: "The DNS providers this panel can publish records at, and what each needs as a credential.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			return map[string]any{
				"providers":    dns.Providers,
				"record_types": config.DNSRecordTypes,
			}, nil
		},
	}
}

func (a *App) mcpVerifyDNSAccount() *mcp.Tool {
	return &mcp.Tool{
		Name:        "verify_dns_account",
		Capability:  capDNS,
		Title:       "Check a DNS credential",
		Description: "Ask the provider whether a stored credential still works, and record the answer.",
		Properties: map[string]mcp.Property{
			"dns_account": {Description: "The credential's id, or its label."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, err := a.mcpDNSAccount(ctx, args)
			if err != nil {
				return nil, err
			}

			detail, err := dns.New(account).Verify()

			a.Store.MarkDNSAccountVerified(account.ID, err == nil)

			if err != nil {
				return nil, fmt.Errorf("%s rejected the credential: %w",
					dns.ProviderLabel(account.Provider), err)
			}

			return map[string]any{
				"dns_account_id": account.ID,
				"label":          account.Label,
				"provider_label": dns.ProviderLabel(account.Provider),
				"verified":       true,
				"detail":         detail,
			}, nil
		},
	}
}

func (a *App) mcpUpdateDNSAccount() *mcp.Tool {
	return &mcp.Tool{
		Name:        "update_dns_account",
		Capability:  capDNS,
		Title:       "Change a DNS credential",
		Description: "Replace a stored credential's token, secret, label or email — for a key that has been rotated.",
		Properties: map[string]mcp.Property{
			"dns_account": {Description: "The credential's id, or its label."},
			"label":       {Description: "A new name for it."},
			"api_token":   {Description: "The new token or key."},
			"api_secret":  {Description: "The new secret."},
			"email":       {Description: "The account email."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, err := a.mcpDNSAccount(ctx, args)
			if err != nil {
				return nil, err
			}

			account.Label = args.StringOr("label", account.Label)
			account.Email = args.StringOr("email", account.Email)

			// Blank means "keep what you have", the same as the form: the panel
			// never hands a stored credential back, so a caller cannot resend one
			// it was not given.
			if token := args.String("api_token"); token != "" {
				account.APIToken = token
			} else {
				account.APIToken = ""
			}

			if secret := args.String("api_secret"); secret != "" {
				account.APISecret = secret
			} else {
				account.APISecret = ""
			}

			if err := a.Store.UpdateDNSAccount(ctx, account); err != nil {
				a.Log.Error("could not update a DNS credential", "error", err)

				return nil, errors.New("the panel could not update the credential")
			}

			return map[string]any{
				"dns_account_id": account.ID,
				"label":          account.Label,
				"updated":        true,
			}, nil
		},
	}
}

func (a *App) mcpDeleteDNSAccount() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_dns_account",
		Capability:  capDNS,
		Title:       "Remove a DNS credential",
		Destructive: true,
		Description: "Take a provider's credential away from the panel. The records already at " +
			"the provider are left alone; what goes is the panel's ability to write more of them, " +
			"so a site using it stops having its records published or removed.",
		Properties: map[string]mcp.Property{
			"dns_account": {Description: "The credential's id, or its label."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, err := a.mcpDNSAccount(ctx, args)
			if err != nil {
				return nil, err
			}

			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			if err := a.requireConfirmation(args, "Removing the DNS credential "+account.Label+
				fmt.Sprintf(", which %d site(s) publish through", account.SitesCount)); err != nil {
				return nil, err
			}

			a.Store.ClearSiteDNSAccount(account.ID)

			if err := a.Store.DeleteDNSAccount(ctx, account.ID, user.ID); err != nil {
				return nil, errors.New("the panel could not remove the credential")
			}

			return map[string]any{
				"dns_account_id": account.ID,
				"label":          account.Label,
				"deleted":        true,
			}, nil
		},
	}
}

func (a *App) mcpListDNSZones() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_dns_zones",
		Capability:  capDNS,
		Title:       "List a credential's zones",
		ReadOnly:    true,
		Description: "The zones one connected credential can write to.",
		Properties: map[string]mcp.Property{
			"dns_account": {Description: "The credential's id, or its label."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, err := a.mcpDNSAccount(ctx, args)
			if err != nil {
				return nil, err
			}

			zones, err := dns.New(account).Zones()
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"dns_account_id": account.ID,
				"label":          account.Label,
				"zones":          zones,
				"count":          len(zones),
			}, nil
		},
	}
}

func (a *App) mcpListDNSRecords() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_dns_records",
		Capability: capDNS,
		Title:      "List a zone's records",
		ReadOnly:   true,
		Description: "Every record in the zone that holds a hostname. Send the domain in `zone`; " +
			"the panel finds which connected credential holds it, so there is nothing to look up " +
			"first.",
		Properties: map[string]mcp.Property{
			"zone":        {Description: "The zone, or any hostname inside it."},
			"dns_account": {Description: "Which credential to ask, when more than one holds the zone."},
			"type":        {Description: "Only records of this type."},
		},
		Required: []string{"zone"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, zoneID, zoneName, err := a.mcpZone(ctx, args)
			if err != nil {
				return nil, err
			}

			records, err := dns.New(account).Records(zoneID, zoneName)
			if err != nil {
				return nil, err
			}

			wanted := strings.ToUpper(args.String("type"))

			rows := make([]map[string]any, 0, len(records))

			for _, record := range records {
				if wanted != "" && record.Type != wanted {
					continue
				}

				rows = append(rows, map[string]any{
					"id":       record.ID,
					"type":     record.Type,
					"name":     record.Name,
					"content":  record.Content,
					"priority": nullablePriority(record.Priority),
					"ttl":      nullableTTL(record.TTL),
					"proxied":  record.Proxied,
				})
			}

			return map[string]any{
				"zone":           zoneName,
				"zone_id":        zoneID,
				"dns_account_id": account.ID,
				"provider_label": dns.ProviderLabel(account.Provider),
				"records":        rows,
				"count":          len(rows),
			}, nil
		},
	}
}

func (a *App) mcpSetDNSRecord() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_dns_record",
		Capability: capDNS,
		Title:      "Write a DNS record",
		Description: "Create a record in a zone, or replace the one already at that name and " +
			"type. The panel finds the credential holding the zone itself.\n\n" +
			"Not for a site's own records: create_site publishes those and delete_site removes " +
			"them, and a record written here for a domain the panel manages is overwritten by the " +
			"next deployment. sync_dns is what republishes a site's, sync_email_dns a mail " +
			"domain's.",
		Properties: map[string]mcp.Property{
			"name":    {Description: "The record's hostname, like www.example.com or example.com itself."},
			"type":    {Description: "The record type.", Enum: config.DNSRecordTypes, Default: "A"},
			"content": {Description: "What the record points at — an address, a hostname, the text of a TXT."},
			"priority": {Type: "integer",
				Description: "For MX and SRV. Defaults to 10 for an MX."},
			"ttl":         {Type: "integer", Description: "Seconds. Left out, the provider's own default."},
			"proxied":     {Type: "boolean", Default: false, Description: "For Cloudflare: serve this name through their proxy."},
			"zone":        {Description: "The zone, when it is not obvious from the name."},
			"dns_account": {Description: "Which credential to write with, when more than one holds the zone."},
		},
		Required: []string{"name", "content"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			kind := strings.ToUpper(args.StringOr("type", "A"))

			if !config.IsDNSRecordType(kind) {
				return nil, fmt.Errorf("%q is not a record type this panel writes", kind)
			}

			content := args.String("content")
			if content == "" {
				return nil, errors.New("`content` is what the record points at")
			}

			account, zoneID, zoneName, err := a.mcpZone(ctx, args)
			if err != nil {
				return nil, err
			}

			priority := -1

			if args.Has("priority") {
				priority = int(args.Int("priority"))
			} else if kind == "MX" {
				priority = 10
			}

			record := dns.Record{
				Type:     kind,
				Name:     dns.Qualify(args.String("name"), zoneName),
				Content:  content,
				Priority: priority,
				TTL:      int(args.Int("ttl")),
				Proxied:  args.Bool("proxied", false) && dns.SupportsProxy(account.Provider),
			}

			id, err := dns.New(account).UpsertRecord(zoneID, zoneName, record)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"record_id":      id,
				"zone":           zoneName,
				"type":           record.Type,
				"name":           record.Name,
				"content":        record.Content,
				"proxied":        record.Proxied,
				"dns_account_id": account.ID,
				"written":        true,
			}, nil
		},
	}
}

func (a *App) mcpDeleteDNSRecord() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_dns_record",
		Capability:  capDNS,
		Title:       "Delete a DNS record",
		Destructive: true,
		Description: "Remove one record from a zone. Name it by `record_id` from list_dns_records, " +
			"or by its name and type.",
		Properties: map[string]mcp.Property{
			"record_id":   {Description: "The record's id, from list_dns_records."},
			"name":        {Description: "The record's hostname, when no id is sent."},
			"type":        {Description: "The record type, when no id is sent."},
			"zone":        {Description: "The zone, when it is not obvious from the name."},
			"dns_account": {Description: "Which credential holds the zone."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told which record goes and has said yes."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, zoneID, zoneName, err := a.mcpZone(ctx, args)
			if err != nil {
				return nil, err
			}

			client := dns.New(account)

			recordID := args.String("record_id")
			label := recordID

			if recordID == "" {
				kind := strings.ToUpper(args.String("type"))

				if kind == "" {
					return nil, errors.New("send `record_id`, or the record's `name` and `type`")
				}

				name := dns.Qualify(args.String("name"), zoneName)

				recordID, err = client.FindRecordID(zoneID, zoneName, dns.Record{
					Type: kind, Name: name, Content: args.String("content"),
				})
				if err != nil {
					return nil, err
				}

				label = kind + " " + name
			}

			if err := a.requireConfirmation(args,
				"Deleting the DNS record "+label+" in "+zoneName); err != nil {
				return nil, err
			}

			if err := client.DeleteRecord(zoneID, zoneName, recordID); err != nil {
				return nil, err
			}

			return map[string]any{
				"zone":      zoneName,
				"record_id": recordID,
				"deleted":   true,
			}, nil
		},
	}
}
