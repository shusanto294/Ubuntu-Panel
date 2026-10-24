package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/nameserver"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func (a *App) dnsIndex(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	accounts, err := a.Store.DNSAccounts(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, "could not list the DNS credentials", err)
		return
	}

	summaries := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		summaries = append(summaries, dnsAccountSummary(account))
	}

	a.Render.Render(w, r, "System/Dns", inertia.Props{
		"accounts":    summaries,
		"providers":   dns.Connectable(),
		"recordTypes": config.DNSRecordTypes,

		// The other way of doing this, summarised: rather than writing records
		// at somebody else's nameserver, be the nameserver. The page offers it
		// beside the credentials because somebody who has no account at any of
		// the providers above has not run out of options.
		"nameserver": map[string]any{
			"installed": a.Store.ServiceInstalled("dns"),
			"ready":     nameserver.CurrentSettings(a.Store).Configured(),
			"zones":     a.Store.DNSZoneCount(r.Context()),
		},
	})
}

func (a *App) dnsCreate(w http.ResponseWriter, r *http.Request) {
	a.Render.Render(w, r, "System/DnsCreate", inertia.Props{
		"providers": dns.Connectable(),
	})
}

func (a *App) dnsStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	provider := input["provider"]
	label := strings.TrimSpace(input["label"])
	token := strings.TrimSpace(input["api_token"])
	secret := strings.TrimSpace(input["api_secret"])

	if !dns.ProviderExists(provider) || dns.IsLocal(provider) {
		// The local one is excluded on purpose: this machine's own nameserver
		// is not a credential, and it is registered by setting it up rather
		// than by pasting anything.
		sess.Error("provider", "Pick a DNS host the panel supports.")
	}
	if label == "" {
		sess.Error("label", "Give this credential a name you will recognise.")
	}
	if token == "" {
		sess.Error("api_token", "Paste the credential.")
	}

	if !sess.HasErrors() && dns.NeedsSecret(provider) && secret == "" {
		sess.Error("api_secret", dns.ProviderLabel(provider)+" needs both a key and a secret.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/dns/create")
		return
	}

	account := &store.DNSAccount{
		UserID:    user.ID,
		Provider:  provider,
		Label:     label,
		APIToken:  token,
		APISecret: secret,
		Email:     strings.TrimSpace(input["email"]),
	}

	// Tried before it is stored. A token that does not work is worse than no
	// token: DNS then fails halfway through creating a site, when the vhost
	// already exists and the certificate is being issued.
	if _, err := dns.New(a.Store, account).Verify(); err != nil {
		sess.Error("api_token", dns.ProviderLabel(provider)+" rejected this credential: "+err.Error())
		inertia.Back(w, r, "/dns/create")

		return
	}

	id, err := a.Store.CreateDNSAccount(r.Context(), account)
	if err != nil || id <= 0 {
		a.Log.Error("could not store the DNS credential", "error", err)
		a.failBack(w, r, "Could not store the credential.")

		return
	}

	a.Store.MarkDNSAccountVerified(id, true)

	sess.Flash("success", dns.ProviderLabel(provider)+" connected.")

	inertia.Redirect(w, r, "/dns")
}

func (a *App) dnsUpdate(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	// Left blank means "keep the credential I already gave you"; the browser
	// never receives it back, so it cannot resubmit it.
	account.Label = stringInput(input, "label", account.Label)
	account.APIToken = strings.TrimSpace(input["api_token"])
	account.APISecret = strings.TrimSpace(input["api_secret"])
	account.Email = stringInput(input, "email", account.Email)

	if err := a.Store.UpdateDNSAccount(r.Context(), account); err != nil {
		a.Log.Error("could not update the DNS credential", "error", err)
		a.failBack(w, r, "Could not update the credential.")

		return
	}

	a.okBack(w, r, "DNS credential updated.")
}

func (a *App) dnsDestroy(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id := routeID(r, "account")

	if _, err := a.Store.FindDNSAccount(r.Context(), id, user.ID); err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	// The records already at the provider are left alone; what goes is the
	// panel's ability to write more of them.
	a.Store.ClearSiteDNSAccount(id)

	if err := a.Store.DeleteDNSAccount(r.Context(), id, user.ID); err != nil {
		a.Log.Error("could not remove the DNS credential", "error", err)
		a.failBack(w, r, "Could not remove the credential.")

		return
	}

	a.okBack(w, r, "DNS credential removed.")
}

func (a *App) dnsVerify(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	detail, err := dns.New(a.Store, account).Verify()

	a.Store.MarkDNSAccountVerified(account.ID, err == nil)

	if err != nil {
		a.failBack(w, r, dns.ProviderLabel(account.Provider)+" rejected the credential: "+err.Error())
		return
	}

	a.okBack(w, r, dns.ProviderLabel(account.Provider)+": "+detail)
}

func (a *App) dnsZones(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	zones, err := dns.New(a.Store, account).Zones()
	if err != nil {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"zones": []any{},
			"error": err.Error(),
		})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"zones": zones})
}

func (a *App) dnsRecords(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	zoneID := r.URL.Query().Get("zone_id")
	zoneName := r.URL.Query().Get("zone_name")

	if zoneID == "" || zoneName == "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"records": []any{},
			"error":   "A zone is required.",
		})

		return
	}

	records, err := dns.New(a.Store, account).Records(zoneID, zoneName)
	if err != nil {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"records": []any{},
			"error":   err.Error(),
		})

		return
	}

	// A priority or TTL of "not applicable" reads as null in the table rather
	// than as -1 or 0.
	rows := make([]map[string]any, 0, len(records))

	for _, record := range records {
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

	a.writeJSON(w, http.StatusOK, map[string]any{"records": rows})
}

func (a *App) dnsStoreRecord(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	zoneID := input["zone_id"]
	zoneName := input["zone_name"]
	kind := input["type"]
	content := strings.TrimSpace(input["content"])

	if !config.IsDNSRecordType(kind) || zoneID == "" || zoneName == "" || content == "" {
		a.failBack(w, r, "Fill in the record type, name and value.")
		return
	}

	priority := int(intInput(input, "priority", -1))
	if kind == "MX" && priority < 0 {
		priority = 10
	}

	record := dns.Record{
		Type:     kind,
		Name:     dns.Qualify(strings.TrimSpace(input["name"]), zoneName),
		Content:  content,
		Priority: priority,
		TTL:      int(intInput(input, "ttl", 0)),
		Proxied:  boolInput(input, "proxied", false) && dns.SupportsProxy(account.Provider),
	}

	result, err := dns.New(a.Store, account).Upsert(zoneID, zoneName, record)
	if err != nil {
		a.failBack(w, r, "Could not write the record: "+err.Error())
		return
	}

	// A record written over one that was already there, and any copies of it
	// that had collected at the same name, are worth a sentence: the zone now
	// says something it did not a moment ago, and the old value is gone.
	message := record.Type + " record for " + record.Name + " written."

	if !result.Created && result.Previous != "" {
		message = record.Type + " record for " + record.Name + " updated — it was " +
			result.Previous + "."
	}

	if result.Removed > 0 {
		message += fmt.Sprintf(" %d duplicate record(s) at that name removed.", result.Removed)
	}

	a.okBack(w, r, message)
}

func (a *App) dnsDestroyRecord(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	account, err := a.Store.FindDNSAccount(r.Context(), routeID(r, "account"), user.ID)
	if err != nil {
		a.notFoundError(w, r, "No such credential.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	zoneID := input["zone_id"]
	zoneName := input["zone_name"]
	recordID := input["record_id"]

	if zoneID == "" || zoneName == "" || recordID == "" {
		a.failBack(w, r, "The record could not be identified.")
		return
	}

	if err := dns.New(a.Store, account).DeleteRecord(zoneID, zoneName, recordID); err != nil {
		a.failBack(w, r, "Could not delete the record: "+err.Error())
		return
	}

	a.okBack(w, r, "Record deleted.")
}

// nullablePriority and nullableTTL write "not applicable" and "the provider's
// own automatic" as null rather than as a number that means neither. A priority
// of 0 is a real MX priority and stays one.
func nullablePriority(value int) any {
	if value < 0 {
		return nil
	}

	return value
}

func nullableTTL(value int) any {
	if value <= 0 {
		return nil
	}

	return value
}
