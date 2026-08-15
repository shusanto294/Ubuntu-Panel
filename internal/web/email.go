package web

import (
	"net/http"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// mailApps is what the Email pages need on the machine.
var mailApps = []string{"mysql", "mail", "roundcube"}

// emailIndex is the Email menu's own URL, and all it does is point.
//
// What used to be here was two lists behind a pair of tabs held in a `ref`,
// which meant the domains and the mailboxes were the same address: nothing to
// bookmark, nothing to send anybody, and nowhere for the panel to put somebody
// back after it deleted the mailbox they were looking at. They are two pages
// now, and this is the way in to the first of them.
func (a *App) emailIndex(w http.ResponseWriter, r *http.Request) {
	inertia.Redirect(w, r, "/email/domains")
}

// mailShell is what both lists need whichever one is open: the counts beside
// the tabs, whether the mail server is installed at all, and the task bar. One
// function because the two pages are one page with two bodies, and a count that
// disagrees across a tab is worse than no count.
func (a *App) mailShell(r *http.Request, domains []map[string]any) inertia.Props {
	mailboxes := 0

	for _, domain := range domains {
		if accounts, ok := domain["accounts"].([]map[string]any); ok {
			mailboxes += len(accounts)
		}
	}

	return inertia.Props{
		"counts":             map[string]any{"domains": len(domains), "mailboxes": mailboxes},
		"requirements":       a.Catalog.Requirements(r.Context(), mailApps),
		"mailConfigured":     a.Mail.IsConfigured(),
		"mailHostname":       nullable(a.Store.Setting("mail_hostname", "")),
		"roundcubeInstalled": a.Store.ServiceInstalled("roundcube"),
		"activeTask":         a.activeTask(r, "mail"),
	}
}

// emailDomainSummaries is every domain in the shape the pages read, with its
// mailboxes on it — the domains list needs them to count, and the mailboxes
// list is that same data read the other way round.
func (a *App) emailDomainSummaries(r *http.Request) ([]map[string]any, error) {
	domains, err := a.Store.EmailDomains(r.Context())
	if err != nil {
		return nil, err
	}

	summaries := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		summaries = append(summaries, a.Store.EmailDomainSummary(r.Context(), domain, true))
	}

	return summaries, nil
}

func (a *App) emailDomains(w http.ResponseWriter, r *http.Request) {
	summaries, err := a.emailDomainSummaries(r)
	if err != nil {
		a.serverError(w, r, "could not list the mail domains", err)
		return
	}

	props := a.mailShell(r, summaries)
	props["domains"] = summaries

	a.Render.Render(w, r, "Email/Domains", props)
}

// emailMailboxes is every address on the server in one list, which is the
// question the Email pages are usually open for: who has a mailbox here.
//
// Flattened on this side rather than in the page, because the page that reads
// it should not have to hold every domain to show one column of them.
func (a *App) emailMailboxes(w http.ResponseWriter, r *http.Request) {
	summaries, err := a.emailDomainSummaries(r)
	if err != nil {
		a.serverError(w, r, "could not list the mail domains", err)
		return
	}

	mailboxes := []map[string]any{}

	for _, domain := range summaries {
		accounts, ok := domain["accounts"].([]map[string]any)
		if !ok {
			continue
		}

		mailboxes = append(mailboxes, accounts...)
	}

	props := a.mailShell(r, summaries)
	props["mailboxes"] = mailboxes

	a.Render.Render(w, r, "Email/Mailboxes", props)
}

func (a *App) emailCreateDomain(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	accounts, _ := a.Store.DNSAccounts(r.Context(), user.ID)

	dnsAccounts := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		dnsAccounts = append(dnsAccounts, dnsAccountSummary(account))
	}

	a.Render.Render(w, r, "Email/CreateDomain", inertia.Props{
		"requirements":   a.Catalog.Requirements(r.Context(), []string{"mysql", "mail"}),
		"mailConfigured": a.Mail.IsConfigured(),
		"dnsAccounts":    dnsAccounts,
	})
}

func (a *App) emailStoreDomain(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	domain := strings.ToLower(strings.TrimSpace(input["domain"]))
	selector := strings.TrimSpace(input["dkim_selector"])
	manageDNS := boolInput(input, "manage_dns", false)

	if !validDomain(domain) {
		sess.Error("domain", "Enter a domain, like example.com.")
	}

	if selector != "" && !validIdentifier(selector, 32) {
		sess.Error("dkim_selector", "Use only letters and digits.")
	}

	var dnsAccountID int64

	if manageDNS {
		dnsAccountID = intInput(input, "dns_account_id", 0)

		if dnsAccountID <= 0 {
			sess.Error("dns_account_id", "Pick a DNS credential the panel can write with.")
		} else if _, err := a.Store.FindDNSAccount(r.Context(), dnsAccountID, user.ID); err != nil {
			sess.Error("dns_account_id", "Pick a DNS credential the panel can write with.")
		}
	}

	if !sess.HasErrors() && !a.Mail.IsConfigured() {
		sess.Error("domain", "The mail server is not installed yet — install it from the Apps page.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/email/domains/create")
		return
	}

	if _, err := a.Store.FindEmailDomainByName(r.Context(), domain); err == nil {
		sess.Error("domain", "That domain is already set up on this server.")
		inertia.Back(w, r, "/email/domains/create")

		return
	}

	id, err := a.Store.CreateEmailDomain(r.Context(), &store.EmailDomain{
		UserID:       user.ID,
		DNSAccountID: dnsAccountID,
		Domain:       domain,
		DKIMSelector: selector,
		ManageDNS:    manageDNS,
	})
	if err != nil || id <= 0 {
		a.Log.Error("could not create the mail domain", "error", err)
		a.failBack(w, r, "Could not create the mail domain.")

		return
	}

	a.Queue.PushID(queue.MailDomainCreate, "domain", id)

	inertia.Redirect(w, r, "/email/domains/"+itoa(id))
}

func (a *App) emailShowDomain(w http.ResponseWriter, r *http.Request) {
	domain, err := a.Store.FindEmailDomain(r.Context(), routeID(r, "domain"))
	if err != nil || domain.Status == "deleting" {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	a.Render.Render(w, r, "Email/Show", inertia.Props{
		"domain":             a.Store.EmailDomainSummary(r.Context(), domain, true),
		"roundcubeInstalled": a.Store.ServiceInstalled("roundcube"),
		"activeTask":         a.activeTask(r, "mail"),
	})
}

func (a *App) emailDestroyDomain(w http.ResponseWriter, r *http.Request) {
	domain, err := a.Store.FindEmailDomain(r.Context(), routeID(r, "domain"))
	if err != nil {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	// Gone from the panel now; taken apart on the machine afterwards — the same
	// bargain a deleted site gets, for the same reason. Removing a mail domain
	// means DKIM keys, records at a DNS provider and every mailbox's stored
	// mail, and none of that is worth watching a row for.
	a.Store.MarkEmailDomainDeleted(domain.ID)

	a.Queue.PushID(queue.MailDomainDelete, "domain", domain.ID)

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "Mail domain deleted.")
	}

	inertia.Redirect(w, r, "/email/domains")
}

// emailRepair configures — or repairs — the mail server itself.
//
// The schema, the vmail store, the Postfix maps and the Dovecot configuration
// are all written by one task, and any of them can be missing on a machine
// where the packages went down but the configuration did not, or where MariaDB
// was reinstalled underneath. Every statement in it is idempotent, so this is
// the same task whether it has run before or not.
func (a *App) emailRepair(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	if !a.Store.ServiceInstalled("mail") {
		a.failBack(w, r, "The mail server is not installed yet — install it from Apps first.")
		return
	}

	a.Queue.PushID(queue.MailConfigure, "user", user.ID)

	inertia.Back(w, r, "/email")
}

// emailTest sends a message through the server the way a mail client would, and
// writes every reply to the log.
func (a *App) emailTest(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	if !a.Store.ServiceInstalled("mail") {
		a.failBack(w, r, "The mail server is not installed yet — install it from Apps first.")
		return
	}

	a.Queue.PushID(queue.MailTest, "user", user.ID)

	inertia.Back(w, r, "/email")
}

// emailRetryDomain runs the domain's setup again.
//
// A mail domain that failed had no way back: the row exists, so adding it again
// is refused, and the only route through was to delete it and retype
// everything. Most failures here are conditions that have since been fixed.
func (a *App) emailRetryDomain(w http.ResponseWriter, r *http.Request) {
	domain, err := a.Store.FindEmailDomain(r.Context(), routeID(r, "domain"))
	if err != nil {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	a.Store.SetEmailDomainStatus(domain.ID, "pending", "")
	a.Queue.PushID(queue.MailDomainCreate, "domain", domain.ID)

	inertia.Back(w, r, "/email/domains/"+itoa(domain.ID))
}

func (a *App) emailSyncDNS(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "domain")

	if _, err := a.Store.FindEmailDomain(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	if _, err := a.Mail.PublishDNS(r.Context(), id); err != nil {
		a.failBack(w, r, "DNS publish failed: "+err.Error())
		return
	}

	a.okBack(w, r, "Mail DNS records published.")
}

func (a *App) emailCreateAccount(w http.ResponseWriter, r *http.Request) {
	domain, err := a.Store.FindEmailDomain(r.Context(), routeID(r, "domain"))
	if err != nil || domain.Status == "deleting" {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	a.Render.Render(w, r, "Email/CreateAccount", inertia.Props{
		"domain": a.Store.EmailDomainSummary(r.Context(), domain, true),
	})
}

// emailNewMailbox is the same form with the domain still to be chosen.
//
// A mailbox belongs to a domain, but somebody adding one has an address in mind
// — sales@something — rather than a domain to open first. This is the way in
// from the Email page; the per-domain form is still there, and both post to the
// same place.
func (a *App) emailNewMailbox(w http.ResponseWriter, r *http.Request) {
	domains, err := a.Store.EmailDomains(r.Context())
	if err != nil {
		a.serverError(w, r, "could not list the mail domains", err)
		return
	}

	summaries := make([]map[string]any, 0, len(domains))

	for _, domain := range domains {
		// A domain being torn down is not somewhere to put a new address.
		if domain.Status == "deleting" {
			continue
		}

		summaries = append(summaries, a.Store.EmailDomainSummary(r.Context(), domain, false))
	}

	a.Render.Render(w, r, "Email/CreateMailbox", inertia.Props{
		"domains": summaries,
	})
}

func (a *App) emailStoreAccount(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	domain, err := a.Store.FindEmailDomain(r.Context(), routeID(r, "domain"))
	if err != nil {
		a.notFoundError(w, r, "No such mail domain.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	local := strings.ToLower(strings.TrimSpace(input["local_part"]))
	password := input["password"]
	quota := intInput(input, "quota_mb", 2048)

	if !validLocalPart(local) {
		sess.Error("local_part", "Use letters, digits, dots, dashes and underscores.")
	}

	if len(password) < 10 {
		sess.Error("password", "Use at least 10 characters.")
	}

	// 0 is unlimited — Dovecot's `*:bytes=0` means exactly that.
	if quota < 0 || quota > 1024000 {
		sess.Error("quota_mb", "Enter a quota between 0 (unlimited) and 1024000 MB.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/email/domains/"+itoa(domain.ID)+"/accounts/create")
		return
	}

	existing, _ := a.Store.EmailAccounts(r.Context(), domain.ID)

	for _, account := range existing {
		if account.LocalPart == local {
			sess.Error("local_part", "That address already exists on this domain.")
			inertia.Back(w, r, "/email/domains/"+itoa(domain.ID)+"/accounts/create")

			return
		}
	}

	id, err := a.Store.CreateEmailAccount(r.Context(), &store.EmailAccount{
		EmailDomainID: domain.ID,
		UserID:        user.ID,
		LocalPart:     local,
		Password:      password,
		QuotaMB:       int(quota),
	})
	if err != nil || id <= 0 {
		a.Log.Error("could not create the mailbox", "error", err)
		a.failBack(w, r, "Could not create the mailbox.")

		return
	}

	a.Queue.PushID(queue.MailAccountCreate, "account", id)

	inertia.Redirect(w, r, "/email/domains/"+itoa(domain.ID))
}

// emailShowAccount is one mailbox's own page.
//
// It exists because "Manage" beside a mailbox used to open the domain it
// belongs to — a page listing every address on it, none of which could be
// changed. What somebody clicking a mailbox wants is that mailbox: who it
// belongs to, how much it may hold, and a new password for the person who has
// forgotten theirs — set, never shown.
func (a *App) emailShowAccount(w http.ResponseWriter, r *http.Request) {
	account, err := a.Store.FindEmailAccount(r.Context(), routeID(r, "account"))
	if err != nil || account.Status == "deleting" {
		a.notFoundError(w, r, "No such mailbox.")
		return
	}

	domain, err := a.Store.FindEmailDomain(r.Context(), account.EmailDomainID)
	if err != nil {
		a.notFoundError(w, r, "That mailbox's domain no longer exists.")
		return
	}

	// The stored password is deliberately not in these props. The panel keeps
	// one so a mailbox can be set up again without anybody retyping it, but a
	// page that prints it is a password on a shared screen and in the payload of
	// every refresh. Forgetting one is answered by setting a new one.
	a.Render.Render(w, r, "Email/Mailbox", inertia.Props{
		"mailbox":            store.EmailAccountSummary(account, domain),
		"domain":             a.Store.EmailDomainSummary(r.Context(), domain, false),
		"roundcubeInstalled": a.Store.ServiceInstalled("roundcube"),
		"activeTask":         a.activeTask(r, "mail"),
	})
}

// emailUpdateAccount saves what that page can change.
//
// One endpoint for the three fields, because they are one form: a change to any
// of them is written to the row and then applied by the same provisioning job
// that created the mailbox — the quota into the mail server's table, the
// password hashed into Dovecot, the name into the webmail's identity.
func (a *App) emailUpdateAccount(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	account, err := a.Store.FindEmailAccount(r.Context(), routeID(r, "account"))
	if err != nil || account.Status == "deleting" {
		a.notFoundError(w, r, "No such mailbox.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := strings.TrimSpace(input["name"])
	password := input["password"]
	quota := intInput(input, "quota_mb", int64(account.QuotaMB))

	if len(name) > 120 {
		sess.Error("name", "Keep the name under 120 characters.")
	}

	// Blank means "leave it alone", so the form can be saved without retyping a
	// password nobody wanted to change.
	if password != "" && len(password) < 10 {
		sess.Error("password", "Use at least 10 characters.")
	}

	if quota < 0 || quota > 1024000 {
		sess.Error("quota_mb", "Enter a quota between 0 (unlimited) and 1024000 MB.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/email/mailboxes/"+itoa(account.ID))
		return
	}

	if err := a.Store.SetEmailAccountDetails(account.ID, name, int(quota)); err != nil {
		a.Log.Error("could not update the mailbox", "mailbox", account.ID, "error", err)
		a.failBack(w, r, "Could not save this mailbox.")

		return
	}

	changed := "Saved."

	if password != "" {
		if err := a.Store.SetEmailAccountPassword(account.ID, password); err != nil {
			a.Log.Error("could not change the mailbox password", "mailbox", account.ID, "error", err)
			a.failBack(w, r, "Could not store the new password.")

			return
		}

		changed = "Saved. The new password works as soon as the mailbox finishes setting itself " +
			"up again — give it to whoever uses the address."
	}

	a.Store.SetEmailAccountStatus(account.ID, "pending", "")
	a.Queue.PushID(queue.MailAccountCreate, "account", account.ID)

	sess.Flash("success", changed)

	inertia.Back(w, r, "/email/mailboxes/"+itoa(account.ID))
}

// emailRepairAccount sets the mailbox up again — re-hashes the password into
// Dovecot's table, remakes its directory, and asks Dovecot whether it can log
// in.
//
// The stored password is the panel's own encrypted copy, so this needs nothing
// typed again. It is the answer to "the password is right and the login is
// refused", which is a question about the server rather than about the mailbox.
func (a *App) emailRepairAccount(w http.ResponseWriter, r *http.Request) {
	account, err := a.Store.FindEmailAccount(r.Context(), routeID(r, "account"))
	if err != nil {
		a.notFoundError(w, r, "No such mailbox.")
		return
	}

	a.Store.SetEmailAccountStatus(account.ID, "pending", "")
	a.Queue.PushID(queue.MailAccountCreate, "account", account.ID)

	inertia.Back(w, r, "/email/mailboxes")
}

func (a *App) emailDestroyAccount(w http.ResponseWriter, r *http.Request) {
	account, err := a.Store.FindEmailAccount(r.Context(), routeID(r, "account"))
	if err != nil {
		a.notFoundError(w, r, "No such mailbox.")
		return
	}

	input, _ := a.input(r)

	// Off the list now; the stored mail goes with the worker.
	a.Store.MarkEmailAccountDeleted(account.ID)

	a.Queue.Push(queue.MailAccountDelete, map[string]any{
		"account": account.ID,
		"mail":    boolInput(input, "delete_mail", true),
	})

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "Mailbox deleted.")
	}

	// Redirect rather than Back: this is nearly always the mailbox's own page
	// asking, and back is the address that has just stopped existing. The list
	// it was on is where somebody who has deleted one wants to be.
	inertia.Redirect(w, r, "/email/mailboxes")
}
