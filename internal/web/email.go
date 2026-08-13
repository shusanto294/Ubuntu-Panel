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

func (a *App) emailIndex(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	domains, err := a.Store.EmailDomains(r.Context())
	if err != nil {
		a.serverError(w, r, "could not list the mail domains", err)
		return
	}

	summaries := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		summaries = append(summaries, a.Store.EmailDomainSummary(r.Context(), domain, true))
	}

	accounts, _ := a.Store.DNSAccounts(r.Context(), user.ID)

	dnsAccounts := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		dnsAccounts = append(dnsAccounts, dnsAccountSummary(account))
	}

	a.Render.Render(w, r, "Email/Index", inertia.Props{
		"domains":            summaries,
		"requirements":       a.Catalog.Requirements(r.Context(), mailApps),
		"mailConfigured":     a.Mail.IsConfigured(),
		"mailHostname":       nullable(a.Store.Setting("mail_hostname", "")),
		"dnsAccounts":        dnsAccounts,
		"roundcubeInstalled": a.Store.ServiceInstalled("roundcube"),
		"activeTask":         a.activeTask(r, "mail"),
	})
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

	inertia.Redirect(w, r, "/email")
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

	inertia.Back(w, r, "/email")
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

	inertia.Back(w, r, "/email")
}
