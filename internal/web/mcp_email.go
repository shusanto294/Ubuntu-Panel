package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The Email pages, as tools.
//
// Everything the browser can do to mail is here: adding a domain, publishing
// its records, creating and removing mailboxes, and configuring or testing the
// server itself. An assistant asked for an address at a domain the panel has
// never heard of does the whole thing — create_email_domain, then
// create_mailbox — without anybody opening the panel.

// mcpEmailDomain resolves the `domain` argument, which is an id or the domain
// itself: an assistant that has just created one holds the id, and one working
// from what a person typed holds the name.
func (a *App) mcpEmailDomain(ctx context.Context, args mcp.Args) (*store.EmailDomain, error) {
	reference := args.StringOr("domain", args.String("email_domain"))

	if reference == "" && args.Int("domain_id") > 0 {
		reference = strconv.FormatInt(args.Int("domain_id"), 10)
	}

	if reference == "" {
		return nil, errors.New("name the mail domain, by id or by name, in the `domain` argument")
	}

	if id, err := strconv.ParseInt(reference, 10, 64); err == nil {
		domain, err := a.Store.FindEmailDomain(ctx, id)
		if err != nil || domain.Status == "deleting" {
			return nil, fmt.Errorf("no mail domain with id %d", id)
		}

		return domain, nil
	}

	domain, err := a.Store.FindEmailDomainByName(ctx, strings.ToLower(reference))
	if err != nil {
		return nil, fmt.Errorf("%s is not a mail domain on this server — list_email_domains has "+
			"the ones that are, and create_email_domain adds it", reference)
	}

	return domain, nil
}

// mcpMailbox resolves the `mailbox` argument: an id, or a whole address.
func (a *App) mcpMailbox(ctx context.Context, args mcp.Args) (*store.EmailAccount, *store.EmailDomain, error) {
	reference := args.StringOr("mailbox", args.StringOr("address", args.String("email")))

	if reference == "" && args.Int("mailbox_id") > 0 {
		reference = strconv.FormatInt(args.Int("mailbox_id"), 10)
	}

	if reference == "" {
		return nil, nil, errors.New("name the mailbox, by id or by address, in the `mailbox` argument")
	}

	if id, err := strconv.ParseInt(reference, 10, 64); err == nil {
		account, err := a.Store.FindEmailAccount(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("no mailbox with id %d", id)
		}

		domain, err := a.Store.FindEmailDomain(ctx, account.EmailDomainID)
		if err != nil {
			return nil, nil, errors.New("that mailbox's domain no longer exists")
		}

		return account, domain, nil
	}

	local, domainName, found := strings.Cut(strings.ToLower(reference), "@")
	if !found {
		return nil, nil, fmt.Errorf("%q is not an address — send the whole of it, like sales@example.com",
			reference)
	}

	domain, err := a.Store.FindEmailDomainByName(ctx, domainName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not a mail domain on this server", domainName)
	}

	accounts, _ := a.Store.EmailAccounts(ctx, domain.ID)

	for _, account := range accounts {
		if account.LocalPart == local {
			return account, domain, nil
		}
	}

	return nil, nil, fmt.Errorf("there is no mailbox at %s", reference)
}

// mcpMailDNSAccount is the credential the panel would publish this domain's
// mail records with: the one whose zones hold the domain, and otherwise the
// first connected one — the same answer the create form's pre-selected list
// gives, and the same one a site gets.
func (a *App) mcpMailDNSAccount(ctx context.Context, userID int64, domain string) int64 {
	accounts, err := a.Store.DNSAccounts(ctx, userID)
	if err != nil || len(accounts) == 0 {
		return 0
	}

	for _, account := range accounts {
		if _, _, err := dns.New(account).FindZone(domain); err == nil {
			return account.ID
		}
	}

	return accounts[0].ID
}

// mailReady refuses before a row is written rather than after: a mail domain
// created on a machine with no mail server is a row whose setup task fails, and
// the reason is one install away.
func (a *App) mailReady() error {
	if !a.Store.ServiceInstalled("mail") {
		return errors.New("the mail server is not installed on this machine — install_apps with " +
			"`mail` puts Postfix, Dovecot and OpenDKIM on it, and roundcube adds the webmail")
	}

	if !a.Mail.IsConfigured() {
		return errors.New("the mail server is installed but not configured yet — " +
			"configure_mail_server does that, and it takes a minute or two")
	}

	return nil
}

func (a *App) mcpListEmailDomains() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_email_domains",
		Capability: capEmail,
		Title:      "List mail domains",
		ReadOnly:   true,
		Description: "Every domain this server accepts and sends mail for, with its mailboxes, " +
			"its DKIM record, its webmail address and the IMAP and SMTP settings a mail client " +
			"needs. Also says whether the mail server itself is installed and configured.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			domains, err := a.Store.EmailDomains(ctx)
			if err != nil {
				return nil, errors.New("the panel could not read its mail domains")
			}

			list := make([]map[string]any, 0, len(domains))

			for _, domain := range domains {
				list = append(list, a.Store.EmailDomainSummary(ctx, domain, true))
			}

			return map[string]any{
				"domains":             list,
				"count":               len(list),
				"mail_installed":      a.Store.ServiceInstalled("mail"),
				"mail_configured":     a.Mail.IsConfigured(),
				"roundcube_installed": a.Store.ServiceInstalled("roundcube"),
				"mail_hostname":       a.Mail.Hostname(),
			}, nil
		},
	}
}

func (a *App) mcpGetEmailDomain() *mcp.Tool {
	return &mcp.Tool{
		Name:       "get_email_domain",
		Capability: capEmail,
		Title:      "Get a mail domain",
		ReadOnly:   true,
		Description: "One mail domain in full: its status, its mailboxes, its DKIM public key, " +
			"the records it needs published, and the client settings for the addresses on it. " +
			"This is what to poll after create_email_domain.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain's id, or the domain itself."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			domain, err := a.mcpEmailDomain(ctx, args)
			if err != nil {
				return nil, err
			}

			return a.Store.EmailDomainSummary(ctx, domain, true), nil
		},
	}
}

func (a *App) mcpCreateEmailDomain() *mcp.Tool {
	return &mcp.Tool{
		Name:       "create_email_domain",
		Capability: capEmail,
		Title:      "Add a mail domain",
		Description: "Set this server up to accept and send mail for a domain: its DKIM key, its " +
			"Postfix and Dovecot entries, the webmail host at mail.<domain> with its certificate, " +
			"and — where a DNS credential holds the domain — the MX, SPF, DKIM and DMARC records " +
			"published for it.\n\n" +
			"Asynchronous: the tool returns as soon as the work is queued, so poll " +
			"get_email_domain until its status is `active` or `failed`. Add the domain before " +
			"creating an address on it; create_mailbox is the second half.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain, like example.com."},
			"manage_dns": {Type: "boolean", Default: true,
				Description: "Publish this domain's mail records at whichever connected provider " +
					"holds it. Left on, the panel finds the credential itself."},
			"dns_account_id": {Type: "integer",
				Description: "Which DNS credential to publish with, from list_dns_accounts. Only " +
					"worth sending when the domain is in more than one of them."},
			"dkim_selector": {Description: "The DKIM selector to sign with.", Default: "mail"},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			domain := strings.ToLower(args.String("domain"))

			if !validDomain(domain) {
				return nil, errors.New("that is not a domain — send it like example.com")
			}

			selector := args.String("dkim_selector")

			if selector != "" && !validIdentifier(selector, 32) {
				return nil, errors.New("a DKIM selector is letters and digits")
			}

			if err := a.mailReady(); err != nil {
				return nil, err
			}

			if existing, err := a.Store.FindEmailDomainByName(ctx, domain); err == nil {
				return nil, fmt.Errorf("%s is already set up on this server, with id %d — "+
					"create_mailbox adds an address to it", domain, existing.ID)
			}

			var dnsAccountID int64

			if args.Bool("manage_dns", true) {
				dnsAccountID = args.Int("dns_account_id")

				if dnsAccountID > 0 {
					if _, err := a.Store.FindDNSAccount(ctx, dnsAccountID, user.ID); err != nil {
						return nil, errors.New("that is not a DNS credential on this account — " +
							"list_dns_accounts has them")
					}
				} else {
					dnsAccountID = a.mcpMailDNSAccount(ctx, user.ID, domain)
				}
			}

			id, err := a.Store.CreateEmailDomain(ctx, &store.EmailDomain{
				UserID:       user.ID,
				DNSAccountID: dnsAccountID,
				Domain:       domain,
				DKIMSelector: selector,
				ManageDNS:    dnsAccountID > 0,
			})
			if err != nil || id <= 0 {
				a.Log.Error("could not create a mail domain for an MCP call", "error", err)

				return nil, errors.New("the panel could not store the mail domain")
			}

			a.Queue.PushID(queue.MailDomainCreate, "domain", id)

			answer := map[string]any{
				"domain_id":  id,
				"domain":     domain,
				"status":     "pending",
				"manage_dns": dnsAccountID > 0,
				"queued":     true,
				"next": fmt.Sprintf("The setup is running on the server's queue. Poll "+
					"get_email_domain with domain %d until its status is active or failed; "+
					"get_task has the console if it fails. Then create_mailbox for each address.", id),
			}

			if dnsAccountID == 0 {
				answer["dns"] = map[string]any{
					"managed": false,
					"tell_the_person": "No DNS provider connected to this panel holds " + domain +
						", so its MX, SPF, DKIM and DMARC records have to be added wherever the " +
						"domain's DNS lives. get_email_domain lists exactly what they are once " +
						"the setup has finished.",
				}
			}

			return answer, nil
		},
	}
}

func (a *App) mcpDeleteEmailDomain() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_email_domain",
		Capability:  capEmail,
		Title:       "Remove a mail domain",
		Destructive: true,
		Description: "Stop this server handling mail for a domain: its mailboxes, their stored " +
			"mail, its DKIM key and the records the panel published for it. This cannot be undone.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain's id, or the domain itself."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			domain, err := a.mcpEmailDomain(ctx, args)
			if err != nil {
				return nil, err
			}

			if err := a.requireConfirmation(args, "Deleting the mail domain "+domain.Domain+
				", with every mailbox on it and all their stored mail"); err != nil {
				return nil, err
			}

			a.Store.MarkEmailDomainDeleted(domain.ID)
			a.Queue.PushID(queue.MailDomainDelete, "domain", domain.ID)

			return map[string]any{
				"domain_id": domain.ID,
				"domain":    domain.Domain,
				"deleted":   true,
				"next":      "The teardown is running on the queue. The domain is already gone from the panel.",
			}, nil
		},
	}
}

func (a *App) mcpRetryEmailDomain() *mcp.Tool {
	return &mcp.Tool{
		Name:       "retry_email_domain",
		Capability: capEmail,
		Title:      "Set a mail domain up again",
		Description: "Run a mail domain's setup from the start. This is what fixes a domain whose " +
			"first attempt failed — a missing app, DNS that had not propagated, a certificate that " +
			"could not be issued — once the reason has been dealt with.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain's id, or the domain itself."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			domain, err := a.mcpEmailDomain(ctx, args)
			if err != nil {
				return nil, err
			}

			a.Store.SetEmailDomainStatus(domain.ID, "pending", "")
			a.Queue.PushID(queue.MailDomainCreate, "domain", domain.ID)

			return map[string]any{
				"domain_id": domain.ID,
				"domain":    domain.Domain,
				"queued":    true,
				"next":      "Poll get_email_domain until its status settles.",
			}, nil
		},
	}
}

func (a *App) mcpSyncEmailDNS() *mcp.Tool {
	return &mcp.Tool{
		Name:       "sync_email_dns",
		Capability: capEmail,
		Title:      "Publish a mail domain's DNS records",
		Description: "Write this domain's MX, SPF, DKIM and DMARC records at its DNS provider " +
			"again — after the key changed, or when the setup's own attempt failed. The answer " +
			"says what it wrote.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The domain's id, or the domain itself."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			domain, err := a.mcpEmailDomain(ctx, args)
			if err != nil {
				return nil, err
			}

			written, err := a.Mail.PublishDNS(ctx, domain.ID)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"domain_id": domain.ID,
				"domain":    domain.Domain,
				"written":   written,
			}, nil
		},
	}
}

func (a *App) mcpCreateMailbox() *mcp.Tool {
	return &mcp.Tool{
		Name:       "create_mailbox",
		Capability: capEmail,
		Title:      "Create an email address",
		Description: "Create a mailbox: an address that receives mail, signs in to webmail, and " +
			"sends through this server with a mail client.\n\n" +
			"Send the whole address in `address`. The domain has to be set up here first — " +
			"create_email_domain does that, and list_email_domains says which are. Left without a " +
			"password, the panel generates one and hands it back in the answer; it is shown once, " +
			"so pass it on to the person.",
		Properties: map[string]mcp.Property{
			"address": {Description: "The whole address, like sales@example.com."},
			"password": {Description: "At least ten characters. Generated when left out, and " +
				"returned in the answer."},
			"quota_mb": {Type: "integer", Default: 2048,
				Description: "The mailbox's size limit in MB. 0 is unlimited."},
		},
		Required: []string{"address"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			address := strings.ToLower(args.StringOr("address", args.String("email")))

			local, domainName, found := strings.Cut(address, "@")

			// The domain may also have been named on its own, with just the local
			// part in `address` — which is what an assistant working from a
			// domain's page sends.
			if !found {
				local = address

				if named, err := a.mcpEmailDomain(ctx, args); err == nil {
					domainName = named.Domain
				}
			}

			if local == "" || domainName == "" {
				return nil, errors.New("send the whole address, like sales@example.com")
			}

			if !validLocalPart(local) {
				return nil, errors.New("the part before the @ is letters, digits, dots, dashes " +
					"and underscores")
			}

			domain, err := a.Store.FindEmailDomainByName(ctx, domainName)
			if err != nil {
				return nil, fmt.Errorf("this server does not handle mail for %s yet — "+
					"create_email_domain with domain %q sets it up, and this address can be "+
					"created as soon as that finishes", domainName, domainName)
			}

			if domain.Status == "deleting" {
				return nil, fmt.Errorf("%s is being removed from this server", domainName)
			}

			if err := a.mailReady(); err != nil {
				return nil, err
			}

			existing, _ := a.Store.EmailAccounts(ctx, domain.ID)

			for _, account := range existing {
				if account.LocalPart == local {
					return nil, fmt.Errorf("%s already exists, with id %d — "+
						"set_mailbox_password changes its password", address, account.ID)
				}
			}

			password := args.String("password")
			generated := false

			if password == "" {
				password = crypto.Password(20)
				generated = true
			} else if len(password) < 10 {
				return nil, errors.New("a mailbox password is at least ten characters")
			}

			quota := int(args.Int("quota_mb"))

			if !args.Has("quota_mb") {
				quota = 2048
			}

			if quota < 0 || quota > 1024000 {
				return nil, errors.New("a quota is between 0 (unlimited) and 1024000 MB")
			}

			id, err := a.Store.CreateEmailAccount(ctx, &store.EmailAccount{
				EmailDomainID: domain.ID,
				UserID:        user.ID,
				LocalPart:     local,
				Password:      password,
				QuotaMB:       quota,
			})
			if err != nil || id <= 0 {
				a.Log.Error("could not create a mailbox for an MCP call", "error", err)

				return nil, errors.New("the panel could not store the mailbox")
			}

			a.Queue.PushID(queue.MailAccountCreate, "account", id)

			host := domain.WebmailHost()

			return map[string]any{
				"mailbox_id":         id,
				"address":            local + "@" + domain.Domain,
				"password":           password,
				"password_generated": generated,
				"quota_mb":           quota,
				"status":             "pending",
				"queued":             true,
				"webmail_url":        "https://" + host + "/?_user=" + local + "@" + domain.Domain,
				"client_settings": map[string]any{
					"imap":     map[string]any{"host": host, "port": 993, "security": "SSL/TLS"},
					"smtp":     map[string]any{"host": host, "port": 587, "security": "STARTTLS"},
					"smtp_ssl": map[string]any{"host": host, "port": 465, "security": "SSL/TLS"},
					"username": local + "@" + domain.Domain,
				},
				"next": "The mailbox is being created on the queue; get_email_domain says when it " +
					"is active. Give the person the address, the password and the client settings — " +
					"the password is not shown again.",
			}, nil
		},
	}
}

func (a *App) mcpSetMailboxPassword() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_mailbox_password",
		Capability: capEmail,
		Title:      "Change a mailbox's password",
		Description: "Set a new password on an existing mailbox and hash it into Dovecot. " +
			"Generated when none is sent, and returned in the answer either way.",
		Properties: map[string]mcp.Property{
			"mailbox":  {Description: "The mailbox's id, or its whole address."},
			"password": {Description: "At least ten characters. Generated when left out."},
		},
		Required: []string{"mailbox"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, domain, err := a.mcpMailbox(ctx, args)
			if err != nil {
				return nil, err
			}

			password := args.String("password")
			generated := false

			if password == "" {
				password = crypto.Password(20)
				generated = true
			} else if len(password) < 10 {
				return nil, errors.New("a mailbox password is at least ten characters")
			}

			if err := a.Store.SetEmailAccountPassword(account.ID, password); err != nil {
				a.Log.Error("could not change a mailbox password", "mailbox", account.ID, "error", err)

				return nil, errors.New("the panel could not store the new password")
			}

			// The stored password is what the worker hashes into Dovecot, so the
			// mailbox is set up again with it rather than being edited in place.
			a.Store.SetEmailAccountStatus(account.ID, "pending", "")
			a.Queue.PushID(queue.MailAccountCreate, "account", account.ID)

			return map[string]any{
				"mailbox_id":         account.ID,
				"address":            account.Address(domain.Domain),
				"password":           password,
				"password_generated": generated,
				"queued":             true,
				"next": "The new password is being written into Dovecot on the queue. Give it to " +
					"the person — the panel does not show it again.",
			}, nil
		},
	}
}

func (a *App) mcpRepairMailbox() *mcp.Tool {
	return &mcp.Tool{
		Name:       "repair_mailbox",
		Capability: capEmail,
		Title:      "Repair a mailbox",
		Description: "Set a mailbox up again with the password the panel already holds: re-hash it " +
			"into Dovecot, remake its directory, and check that it can sign in. This is the answer " +
			"to \"the password is right and the login is refused\".",
		Properties: map[string]mcp.Property{
			"mailbox": {Description: "The mailbox's id, or its whole address."},
		},
		Required: []string{"mailbox"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, domain, err := a.mcpMailbox(ctx, args)
			if err != nil {
				return nil, err
			}

			a.Store.SetEmailAccountStatus(account.ID, "pending", "")
			a.Queue.PushID(queue.MailAccountCreate, "account", account.ID)

			return map[string]any{
				"mailbox_id": account.ID,
				"address":    account.Address(domain.Domain),
				"queued":     true,
			}, nil
		},
	}
}

func (a *App) mcpDeleteMailbox() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_mailbox",
		Capability:  capEmail,
		Title:       "Delete a mailbox",
		Destructive: true,
		Description: "Remove an address and — unless told otherwise — everything it has ever " +
			"received. This cannot be undone.",
		Properties: map[string]mcp.Property{
			"mailbox": {Description: "The mailbox's id, or its whole address."},
			"delete_mail": {Type: "boolean", Default: true,
				Description: "Remove the stored mail as well as the address."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Required: []string{"mailbox"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			account, domain, err := a.mcpMailbox(ctx, args)
			if err != nil {
				return nil, err
			}

			address := account.Address(domain.Domain)

			if err := a.requireConfirmation(args, "Deleting the mailbox "+address); err != nil {
				return nil, err
			}

			a.Store.MarkEmailAccountDeleted(account.ID)

			a.Queue.Push(queue.MailAccountDelete, map[string]any{
				"account": account.ID,
				"mail":    args.Bool("delete_mail", true),
			})

			return map[string]any{
				"mailbox_id": account.ID,
				"address":    address,
				"deleted":    true,
			}, nil
		},
	}
}

func (a *App) mcpConfigureMailServer() *mcp.Tool {
	return &mcp.Tool{
		Name:       "configure_mail_server",
		Capability: capEmail,
		Title:      "Configure or repair the mail server",
		Description: "Write Postfix, Dovecot and OpenDKIM's configuration, create the mail schema " +
			"and the vmail store, and restart the daemons. Every step is idempotent, so this is " +
			"both the first-time setup and the repair. Asynchronous — get_task has the console.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			if !a.Store.ServiceInstalled("mail") {
				return nil, errors.New("the mail server is not installed on this machine — " +
					"install_apps with `mail` puts it there first")
			}

			a.Queue.PushID(queue.MailConfigure, "user", user.ID)

			return map[string]any{
				"queued": true,
				"next":   "The configuration is running on the queue; list_tasks has it, and get_task the console.",
			}, nil
		},
	}
}

func (a *App) mcpTestMailDelivery() *mcp.Tool {
	return &mcp.Tool{
		Name:       "test_mail_delivery",
		Capability: capEmail,
		Title:      "Send a test message",
		Description: "Send a message through this server the way a mail client would, and write " +
			"every reply from the conversation to the task log. This is what says whether " +
			"authentication, delivery and DKIM signing actually work.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			if !a.Store.ServiceInstalled("mail") {
				return nil, errors.New("the mail server is not installed on this machine")
			}

			a.Queue.PushID(queue.MailTest, "user", user.ID)

			return map[string]any{
				"queued": true,
				"next":   "The test is running on the queue; get_task has the whole SMTP conversation.",
			}, nil
		},
	}
}
