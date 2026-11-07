package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/migrate"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Moving sites and mail domains between two panels.
//
// The receiving panel issues a migration key (Migrate → Receive). The sending
// panel is given that key and the receiving panel's address, and from then on
// every message between them is sealed with it (see the migrate package): the
// key never crosses the wire, so the transfer is private and tamper-proof even
// to a panel reached over plain HTTP.
//
// A move, from the sending side:
//
//  1. ask the new server what it has, and refuse what it could not take;
//  2. pack the site (files and database) or mail domain (stored mail), and
//     send it with its settings, the passwords its configuration needs, and
//     the DNS credential that manages it;
//  3. wait while the new server builds it — the same site, serving, but not
//     yet named in DNS;
//  4. point the DNS records at the new server, from here, with this panel's
//     credential;
//  5. have the new server get its certificates, now the names resolve to it;
//  6. for mail, copy what arrived here while the DNS changed over;
//  7. remove it from this server, if that was asked for.

const (
	migrationKeyIDSetting     = "migration_key_id"
	migrationKeySecretSetting = "migration_key_secret"
	migrationKeyAtSetting     = "migration_key_created_at"
	migrationPeerSetting      = "migration_peer_address"
	migrationPeerKeySetting   = "migration_peer_key"
	migrationPeerHelloSetting = "migration_peer_hello"

	// MigrationRoot holds the packages on both sides while a move is going.
	migrationRoot = "/var/backups/ubuntu-panel/migrations"
)

// The queue's names for the four kinds of migration work.
const (
	jobMigrateOut     = "migrate.out"
	jobMigrateIn      = "migrate.in"
	jobMigrateFinish  = "migrate.in.finish"
	jobMigrateCatchUp = "migrate.in.catch-up"
)

// migrationSeen refuses a sealed request the second time.
var migrationSeen = &migrate.Seen{}

// ------------------------------------------------------------------ the page

func (a *App) migrateIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// The key lets whoever holds it put a site on this server, so a
	// read-only account is told one exists and never shown it.
	key := a.migrationKey()
	receive := map[string]any{"key": nil, "exists": key != nil, "created_at": nil}
	if key != nil {
		receive["created_at"] = a.Store.Setting(migrationKeyAtSetting, "")
		if user := userFrom(ctx); user != nil && user.IsAdmin() {
			receive["key"] = key.String()
		}
	}

	var hello *migrate.Hello
	a.settingJSON(migrationPeerHelloSetting, &hello)

	sites := []map[string]any{}
	if list, err := a.Store.Sites(ctx); err == nil {
		for _, site := range list {
			sites = append(sites, map[string]any{
				"id": site.ID, "domain": site.Domain, "type": site.Type, "status": site.Status,
				"dns": site.ManageDNS && site.DNSAccountID > 0, "staging": site.IsStaging(),
			})
		}
	}

	domains := []map[string]any{}
	if list, err := a.Store.EmailDomains(ctx); err == nil {
		for _, domain := range list {
			accounts, _ := a.Store.EmailAccounts(ctx, domain.ID)
			domains = append(domains, map[string]any{
				"id": domain.ID, "domain": domain.Domain, "status": domain.Status,
				"mailboxes": len(accounts), "dns": domain.ManageDNS && domain.DNSAccountID > 0,
			})
		}
	}

	migrations, _ := a.Store.Migrations(ctx, 50)

	a.Render.Render(w, r, "System/Migrate", inertia.Props{
		"receive": receive,
		"peer": map[string]any{
			"address": a.Store.Setting(migrationPeerSetting, ""),
			"hello":   hello,
		},
		"thisServer":  map[string]any{"ip": system.PublicIP()},
		"sites":       sites,
		"mailDomains": domains,
		"migrations":  migrations,
	})
}

func (a *App) migrateBack(w http.ResponseWriter, r *http.Request, message string, failed bool) {
	if sess := session.FromContext(r.Context()); sess != nil {
		if failed {
			sess.Flash("error", message)
		} else {
			sess.Flash("success", message)
		}
	}

	inertia.Back(w, r, "/migrate")
}

// migrateKey issues a new migration key, which replaces the one before.
func (a *App) migrateKey(w http.ResponseWriter, r *http.Request) {
	key, err := migrate.NewKey()
	if err != nil {
		a.migrateBack(w, r, err.Error(), true)
		return
	}

	a.Store.SetSecretSetting(migrationKeySecretSetting, string(key.Secret))
	a.Store.SetSetting(migrationKeyIDSetting, key.ID)
	a.Store.SetSetting(migrationKeyAtSetting, time.Now().UTC().Format(time.RFC3339))

	a.Log.Info("migration key issued", "ip", session.ClientIP(r))
	a.migrateBack(w, r, "A new migration key is ready. Paste it, with this panel's address, into the panel "+
		"you are moving from. The key before it no longer works.", false)
}

// migrateRevoke stops this panel accepting anything.
func (a *App) migrateRevoke(w http.ResponseWriter, r *http.Request) {
	a.Store.ForgetSetting(migrationKeyIDSetting)
	a.Store.ForgetSetting(migrationKeySecretSetting)
	a.Store.ForgetSetting(migrationKeyAtSetting)

	a.Log.Info("migration key revoked", "ip", session.ClientIP(r))
	a.migrateBack(w, r, "The migration key is revoked. Nothing can be moved to this server until a new one is issued.", false)
}

func (a *App) migrationKey() *migrate.Key {
	id := a.Store.Setting(migrationKeyIDSetting, "")
	secret := a.Store.SecretSetting(migrationKeySecretSetting)
	if id == "" || len(secret) != 32 {
		return nil
	}

	return &migrate.Key{ID: id, Secret: []byte(secret)}
}

// migrateConnect checks the other panel answers with the key given, and
// keeps both for the moves that follow.
func (a *App) migrateConnect(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	key, err := migrate.ParseKey(input["key"])
	if err != nil {
		sess.Error("key", err.Error())
		inertia.Back(w, r, "/migrate")
		return
	}

	client, err := migrate.NewClient(input["address"], key)
	if err != nil {
		sess.Error("address", err.Error())
		inertia.Back(w, r, "/migrate")
		return
	}

	if a.migrationKey() != nil && a.migrationKey().ID == key.ID {
		sess.Error("key", "That is this panel's own key. Use the key the other panel shows under Receive.")
		inertia.Back(w, r, "/migrate")
		return
	}

	var hello migrate.Hello
	if err := client.Call(migrate.PathHello, struct{}{}, &hello); err != nil {
		sess.Error("address", err.Error())
		inertia.Back(w, r, "/migrate")
		return
	}

	a.Store.SetSetting(migrationPeerSetting, client.BaseURL)
	a.Store.SetSecretSetting(migrationPeerKeySetting, key.String())
	a.saveJSON(migrationPeerHelloSetting, hello)

	a.Log.Info("connected to another panel for migration", "peer", client.BaseURL, "ip", session.ClientIP(r))
	a.migrateBack(w, r, fmt.Sprintf("Connected to %s (Ubuntu Panel %s, %s).", client.BaseURL, hello.Version, hello.PublicIP), false)
}

func (a *App) migrateDisconnect(w http.ResponseWriter, r *http.Request) {
	a.Store.ForgetSetting(migrationPeerSetting)
	a.Store.ForgetSetting(migrationPeerKeySetting)
	a.Store.ForgetSetting(migrationPeerHelloSetting)

	a.migrateBack(w, r, "Disconnected. The other panel's key is no longer kept here.", false)
}

func (a *App) peerClient() (*migrate.Client, error) {
	address := a.Store.Setting(migrationPeerSetting, "")
	key, err := migrate.ParseKey(a.Store.SecretSetting(migrationPeerKeySetting))
	if address == "" || err != nil {
		return nil, errors.New("connect to the other panel first")
	}

	return migrate.NewClient(address, key)
}

// migrationOptions are what the person chose when starting a move.
type migrationOptions struct {
	RemoveAfter bool   `json:"remove_after"`
	DestIP      string `json:"dest_ip,omitempty"`
}

// migrateStart queues a move for each site and mail domain picked.
func (a *App) migrateStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	client, err := a.peerClient()
	if err != nil {
		a.migrateBack(w, r, err.Error(), true)
		return
	}

	var siteIDs, mailIDs []int64
	json.Unmarshal([]byte(input["sites"]), &siteIDs)
	json.Unmarshal([]byte(input["mail"]), &mailIDs)

	if len(siteIDs)+len(mailIDs) == 0 {
		a.migrateBack(w, r, "Pick at least one site or mail domain to move.", true)
		return
	}

	options, _ := json.Marshal(migrationOptions{RemoveAfter: boolInput(input, "remove_after", false)})
	queued := []string{}

	queue := func(kind, name string, localID int64) {
		if active := a.Store.ActiveMigration(ctx, kind, name); active != nil {
			return
		}

		id, err := a.Store.CreateMigration(ctx, &store.Migration{
			Direction: "out", Kind: kind, Name: name, Peer: client.BaseURL,
			LocalID: localID, Options: string(options), Status: "queued",
		})
		if err != nil {
			return
		}

		a.Queue.PushID(jobMigrateOut, "migration", id)
		queued = append(queued, name)
	}

	for _, id := range siteIDs {
		if site, err := a.Store.FindSite(ctx, id); err == nil {
			queue("site", site.Domain, site.ID)
		}
	}

	for _, id := range mailIDs {
		if domain, err := a.Store.FindEmailDomain(ctx, id); err == nil {
			queue("mail", domain.Domain, domain.ID)
		}
	}

	if len(queued) == 0 {
		a.migrateBack(w, r, "Those are already being moved.", true)
		return
	}

	a.Log.Info("migration started", "to", client.BaseURL, "what", strings.Join(queued, " "), "ip", session.ClientIP(r))
	a.migrateBack(w, r, "Moving "+strings.Join(queued, ", ")+" to "+client.BaseURL+". Follow each one below.", false)
}

// migrateCatchUp copies mail that reached this server after a mail domain
// moved — for a DNS cache somewhere that kept sending it here.
func (a *App) migrateCatchUp(w http.ResponseWriter, r *http.Request) {
	m, err := a.Store.FindMigration(r.Context(), routeID(r, "migration"))
	if err != nil || m.Direction != "out" || m.Kind != "mail" || m.Status != "done" {
		a.migrateBack(w, r, "Only a mail domain that has finished moving has new mail to copy.", true)
		return
	}

	a.Store.SetMigrationStatus(m.ID, "queued", "Copying the mail that arrived since.")
	a.Queue.Push(jobMigrateOut, map[string]any{"migration": m.ID, "catch_up_only": true})

	a.migrateBack(w, r, "Copying the mail that has reached this server since the move.", false)
}

// ------------------------------------------------------------------ jobs

func (a *App) registerMigrationJobs() {
	a.Queue.Handle(jobMigrateOut, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Migration   int64 `json:"migration"`
			CatchUpOnly bool  `json:"catch_up_only"`
		}
		if err := job.Decode(&payload); err != nil {
			return err
		}

		return a.runMigrationOut(ctx, payload.Migration, payload.CatchUpOnly)
	})

	a.Queue.Handle(jobMigrateIn, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Migration int64 `json:"migration"`
		}
		if err := job.Decode(&payload); err != nil {
			return err
		}

		return a.runMigrationIn(ctx, payload.Migration)
	})

	a.Queue.Handle(jobMigrateFinish, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Migration int64 `json:"migration"`
		}
		if err := job.Decode(&payload); err != nil {
			return err
		}

		return a.runMigrationFinish(ctx, payload.Migration)
	})

	a.Queue.Handle(jobMigrateCatchUp, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Migration int64 `json:"migration"`
		}
		if err := job.Decode(&payload); err != nil {
			return err
		}

		return a.runMigrationCatchUp(ctx, payload.Migration)
	})
}

// ownerID is who a site or domain that arrived belongs to: the first account.
func (a *App) ownerID(ctx context.Context) int64 {
	if users, err := a.Store.Users(ctx); err == nil {
		for _, user := range users {
			if user.IsAdmin() {
				return user.ID
			}
		}
	}

	return 1
}

// ----------------------------------------------------------------- sending

// runMigrationOut is the whole move, from this side, as one task.
func (a *App) runMigrationOut(ctx context.Context, migrationID int64, catchUpOnly bool) error {
	m, err := a.Store.FindMigration(ctx, migrationID)
	if err != nil {
		return err
	}

	var options migrationOptions
	json.Unmarshal([]byte(m.Options), &options)

	client, err := a.peerClient()
	if err != nil {
		a.Store.SetMigrationStatus(m.ID, "failed", err.Error())
		return err
	}
	if client.BaseURL != m.Peer {
		err := errors.New("this panel is now connected to " + client.BaseURL + ", not " + m.Peer + " — connect to it again to finish")
		a.Store.SetMigrationStatus(m.ID, "failed", err.Error())
		return err
	}

	directory := fmt.Sprintf("%s/out-%d", migrationRoot, m.ID)

	var site *store.Site
	var domain *store.EmailDomain
	siteID := int64(0)

	switch {
	case catchUpOnly:
		// Only the domain's name and its stored mail are needed, and the
		// domain may have been removed here since.
	case m.Kind == "site":
		if site, err = a.Store.FindSite(ctx, m.LocalID); err != nil {
			a.Store.SetMigrationStatus(m.ID, "failed", "The site is no longer on this server.")
			return err
		}
		siteID = site.ID
	default:
		if domain, err = a.Store.FindEmailDomain(ctx, m.LocalID); err != nil {
			a.Store.SetMigrationStatus(m.ID, "failed", "The mail domain is no longer on this server.")
			return err
		}
	}

	// The panel reads and writes the packages as its own user, so it has to
	// be able to pass through the directory that holds them.
	proc.Run(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chmod 711 %[1]s", proc.Quote(migrationRoot)), time.Minute)

	kind := "site"
	if m.Kind == "mail" {
		kind = "mail"
	}

	taskID, err := a.Store.CreateTask(ctx, kind, "migrate.out", a.ownerID(ctx), siteID, "Moving "+m.Name+" to "+m.Peer)
	if err != nil {
		return err
	}

	a.Store.SetMigrationTask(m.ID, taskID)
	a.Store.SetMigrationStatus(m.ID, "running", "")

	list := &tasks.List{}
	var hello migrate.Hello
	transfer := m.RemoteID

	// wait polls the other panel until the transfer reaches want.
	wait := func(runner *tasks.Runner, want string, limit time.Duration) (string, error) {
		deadline := time.Now().Add(limit)
		last := ""

		for time.Now().Before(deadline) {
			var status migrate.Status
			if err := client.Call(migrate.PathStatus, migrate.Transfer{Transfer: transfer}, &status); err != nil {
				runner.Writef("Asking the new server failed (%v); asking again.\n", err)
			} else {
				if status.Message != "" && status.Message != last {
					runner.Writef("%s\n", status.Message)
					last = status.Message
				}
				switch status.Status {
				case want:
					return "The new server is done.", nil
				case "failed":
					return "", fmt.Errorf("the new server could not finish: %s — its Migrate page has the whole log", status.Message)
				}
			}

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}

		return "", errors.New("the new server took too long; its Migrate page shows where it got to")
	}

	if catchUpOnly {
		list.AddFn("Copy the mail that arrived since", func(runner *tasks.Runner) (string, error) {
			return a.sendCatchUp(ctx, runner, client, transfer, m.Name, directory, wait)
		})
	} else {
		list.AddFn("Check the new server", func(runner *tasks.Runner) (string, error) {
			if err := client.Call(migrate.PathHello, struct{}{}, &hello); err != nil {
				return "", err
			}

			options.DestIP = hello.PublicIP
			encoded, _ := json.Marshal(options)
			a.Store.DB().Exec(`UPDATE migrations SET options = ? WHERE id = ?`, string(encoded), m.ID)

			if problem := a.refuseMigration(ctx, m, &hello, site); problem != "" {
				return "", errors.New(problem)
			}

			return fmt.Sprintf("%s runs Ubuntu Panel %s at %s and can take %s.", m.Peer, hello.Version, hello.PublicIP, m.Name), nil
		})

		if site != nil {
			a.Sites.PackageForMigration(ctx, list, site, directory)
		} else {
			a.Mail.PackageForMigration(list, domain, directory)
		}

		list.AddFn("Send the settings", func(runner *tasks.Runner) (string, error) {
			begin := migrate.Begin{Kind: m.Kind, SourceIP: system.PublicIP()}

			accountID := int64(0)
			if site != nil {
				manifest, err := a.Sites.MigrationManifest(ctx, site)
				if err != nil {
					return "", err
				}
				begin.Site = manifest
				if site.ManageDNS {
					accountID = site.DNSAccountID
				}
			} else {
				manifest, err := a.Mail.MigrationManifest(ctx, domain)
				if err != nil {
					return "", err
				}
				begin.Mail = manifest
				if domain.ManageDNS {
					accountID = domain.DNSAccountID
				}
			}

			// The credential that manages its DNS, so the new server can go
			// on managing it. Not this panel's own nameserver: that one is
			// here, and stays here.
			if accountID > 0 {
				if account, err := a.Store.FindDNSAccount(ctx, accountID, 0); err == nil && !dns.IsLocal(account.Provider) {
					begin.DNSAccount = &migrate.DNSAccount{
						Provider: account.Provider, Label: account.Label, APIToken: account.APIToken,
						APISecret: account.APISecret, Email: account.Email, AccountID: account.AccountID,
					}
				}
			}

			var answer migrate.Transfer
			if err := client.Call(migrate.PathBegin, begin, &answer); err != nil {
				return "", err
			}

			transfer = answer.Transfer
			a.Store.SetMigrationRemote(m.ID, transfer)

			return "The new server has made room for " + m.Name + ".", nil
		})

		list.AddFn("Send the files", func(runner *tasks.Runner) (string, error) {
			hashes := map[string]string{}

			for name := range migrate.Files {
				path := directory + "/" + name
				if name == "catch-up.tar.gz" {
					continue
				}
				if _, err := os.Stat(path); err != nil {
					continue
				}

				lastShown := int64(-1)
				hash, err := client.Upload(transfer, name, path, func(sent, total int64) {
					// Every 10%: the console is not a progress bar.
					if total > 0 && (sent*10/total) != lastShown {
						lastShown = sent * 10 / total
						runner.Writef("%s: %s of %s\n", name, humanBytes(sent), humanBytes(total))
					}
				})
				if err != nil {
					return "", err
				}
				hashes[name] = hash
			}

			if err := client.Call(migrate.PathFinish, migrate.Finish{Transfer: transfer, Files: hashes}, nil); err != nil {
				return "", err
			}

			return "Everything arrived, and the new server checked it is what was sent.", nil
		})

		list.AddFn("Wait while the new server sets it up", func(runner *tasks.Runner) (string, error) {
			return wait(runner, "imported", 3*time.Hour)
		})

		list.AddFn("Point the DNS at the new server", func(runner *tasks.Runner) (string, error) {
			return a.switchDNS(ctx, m, site, domain, hello.PublicIP)
		})

		list.AddFn("Finish on the new server", func(runner *tasks.Runner) (string, error) {
			if err := client.Call(migrate.PathFinalize, migrate.Transfer{Transfer: transfer}, nil); err != nil {
				return "", err
			}

			want := "done"
			if m.Kind == "mail" {
				want = "finished"
			}

			return wait(runner, want, time.Hour)
		})

		if domain != nil {
			list.AddFn("Copy the mail that arrived during the move", func(runner *tasks.Runner) (string, error) {
				return a.sendCatchUp(ctx, runner, client, transfer, m.Name, directory, wait)
			})
		}

		if options.RemoveAfter {
			list.AddFn("Remove it from this server", func(runner *tasks.Runner) (string, error) {
				return a.removeMoved(ctx, m, site, domain)
			}).Optional()
		}

		// A site's package is not needed once it has arrived. A mail
		// domain's marker is what the next catch-up copies from, so its
		// directory stays, without the big archive.
		list.AddFn("Tidy up", func(runner *tasks.Runner) (string, error) {
			if domain != nil {
				os.Remove(directory + "/mail.tar.gz")
				return "Kept the time of the last copy, for the next catch-up.", nil
			}
			proc.Ok("sudo rm -rf " + proc.Quote(directory))
			return "The package is removed from this server.", nil
		}).Optional()
	}

	if !tasks.New(a.Store, taskID).Run(list) {
		message := "The move did not finish."
		if task, err := a.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
			message = task.Message
		}

		if !catchUpOnly && domain == nil {
			proc.Ok("sudo rm -rf " + proc.Quote(directory))
		}

		// What the new server made room for, if it got no further than
		// receiving, is taken away again there. Anything it started building
		// is left for its owner to look at.
		if !catchUpOnly && transfer != "" {
			client.Call(migrate.PathCancel, migrate.Transfer{Transfer: transfer}, nil)
		}

		a.Store.SetMigrationStatus(m.ID, "failed", message)

		return errors.New(message)
	}

	a.Store.SetMigrationStatus(m.ID, "done", m.Name+" is on "+m.Peer+" now.")

	return nil
}

// refuseMigration is why the new server cannot take this, or "".
func (a *App) refuseMigration(ctx context.Context, m *store.Migration, hello *migrate.Hello, site *store.Site) string {
	for _, name := range hello.Sites {
		if m.Kind == "site" && strings.EqualFold(name, m.Name) {
			return "The new server already has a site called " + m.Name + ". Delete it there first."
		}
	}

	for _, name := range hello.MailDomains {
		if m.Kind == "mail" && strings.EqualFold(name, m.Name) {
			return "The new server already has the mail domain " + m.Name + ". Delete it there first."
		}
	}

	if m.Kind == "mail" && !hello.MailConfigured {
		return "The new server has no mail server set up. Install Mail on its Apps page first."
	}

	if site == nil {
		return ""
	}

	if !hello.Services["nginx"] {
		return "The new server has no nginx. Install it on its Apps page first."
	}

	if database, _, _ := a.Sites.MigrationDatabase(ctx, site); database != nil && !hello.Services[database.Engine] {
		return fmt.Sprintf("%s's database is %s, which the new server does not have. Install it on its Apps page first.",
			site.Domain, config.EngineLabel(database.Engine))
	}

	if site.IsProxied() && !hello.Services["node"] {
		return "The new server has no Node.js. Install it on its Apps page first."
	}

	if hello.SitesRoot != "" && !strings.HasPrefix(site.RootPath, strings.TrimRight(hello.SitesRoot, "/")+"/") {
		return fmt.Sprintf("%s lives in %s, outside the new server's sites directory (%s).",
			site.Domain, site.RootPath, hello.SitesRoot)
	}

	return ""
}

// switchDNS points the records at the new server, with this panel's DNS
// credential, and stops this panel managing them: from now on the new
// server does.
func (a *App) switchDNS(ctx context.Context, m *store.Migration, site *store.Site, domain *store.EmailDomain, destIP string) (string, error) {
	if destIP == "" {
		return "", errors.New("the new server did not say what its address is")
	}

	if site != nil {
		if !site.ManageDNS || site.DNSAccountID <= 0 {
			return fmt.Sprintf("This panel does not manage %s's DNS, so it could not change it. Point the A "+
				"record of %s at %s at your DNS provider now; the new server gets its certificate once it does.",
				site.Domain, strings.Join(site.Hostnames(), ", "), destIP), nil
		}

		moved := *site
		moved.DNSContent = destIP
		moved.DNSType = "A"

		output, err := dns.SyncForSite(ctx, a.Store, &moved)
		if err != nil {
			return "", fmt.Errorf("the DNS provider refused: %w", err)
		}

		a.Store.DetachSiteDNS(site.ID)
		system.ForgetResolution(site.Domain)

		return output + "This server no longer manages these records; the new server does.", nil
	}

	if !domain.ManageDNS || domain.DNSAccountID <= 0 {
		return fmt.Sprintf("This panel does not manage %s's DNS. Point mail.%s's A record, and the IP in its SPF "+
			"record, at %s at your DNS provider now.", domain.Domain, domain.Domain, destIP), nil
	}

	output, err := dns.SyncForMail(ctx, a.Store, domain, destIP)
	if err != nil {
		return "", fmt.Errorf("the DNS provider refused: %w", err)
	}

	a.Store.DetachMailDNS(domain.ID)

	return output + "This server no longer manages these records; the new server does.", nil
}

// sendCatchUp copies mail that arrived here since the last copy.
func (a *App) sendCatchUp(ctx context.Context, runner *tasks.Runner, client *migrate.Client, transfer, domain, directory string,
	wait func(*tasks.Runner, string, time.Duration) (string, error)) (string, error) {
	if transfer == "" {
		return "", errors.New("the new server's transfer for this domain is unknown")
	}

	said, err := a.Mail.PackageCatchUp(runner, domain, directory)
	if err != nil {
		return "", err
	}
	runner.Writef("%s\n", said)

	hash, err := client.Upload(transfer, "catch-up.tar.gz", directory+"/catch-up.tar.gz", nil)
	if err != nil {
		return "", err
	}
	os.Remove(directory + "/catch-up.tar.gz")

	if err := client.Call(migrate.PathCatchUp, migrate.Finish{Transfer: transfer, Files: map[string]string{"catch-up.tar.gz": hash}}, nil); err != nil {
		return "", err
	}

	return wait(runner, "done", time.Hour)
}

// removeMoved deletes the old copy, leaving the DNS records alone: they are
// the new server's now.
func (a *App) removeMoved(ctx context.Context, m *store.Migration, site *store.Site, domain *store.EmailDomain) (string, error) {
	if site != nil {
		a.Store.DetachSiteDNS(site.ID)
		if err := a.Sites.Destroy(ctx, site.ID, true); err != nil {
			return "", err
		}
		return site.Domain + " is removed from this server: its files, database, vhost and backups.", nil
	}

	a.Store.DetachMailDNS(domain.ID)
	a.Store.MarkEmailDomainDeleted(domain.ID)
	if err := a.Mail.DeleteDomain(ctx, domain.ID); err != nil {
		return "", err
	}

	return domain.Domain + " is removed from this server, mailboxes and stored mail included.", nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return strconv.FormatInt(n, 10) + " B"
}

// --------------------------------------------------------------- receiving

// migrationAPI wraps a receiving endpoint: the request is opened with this
// panel's migration key, and the answer sealed with it. Anything not sealed
// with the current key is refused before it is read further.
func (a *App) migrationAPI(handle func(ctx context.Context, payload []byte) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := a.migrationKey()
		if key == nil || !migrate.SameKey(r.Header.Get(migrate.KeyHeader), key.ID) {
			http.Error(w, "unknown migration key", http.StatusUnauthorized)
			return
		}

		purpose := r.URL.RequestURI()

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, migrate.ChunkSize+64*1024))
		if err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}

		payload, err := key.Open("request "+purpose, body, migrationSeen)
		if err != nil {
			a.Log.Warn("migration request refused", "path", r.URL.Path, "ip", session.ClientIP(r), "error", err)
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		answer, err := handle(r.Context(), payload)
		if err != nil {
			answer = map[string]string{"error": err.Error()}
		}
		if answer == nil {
			answer = map[string]bool{"ok": true}
		}

		encoded, _ := json.Marshal(answer)
		sealed, err := key.Seal("response "+purpose, encoded)
		if err != nil {
			http.Error(w, "could not seal the answer", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(sealed)
	}
}

func (a *App) migrateHello(ctx context.Context, _ []byte) (any, error) {
	hello := migrate.Hello{
		Panel: config.Name, Version: config.Version, PublicIP: system.PublicIP(),
		Services: map[string]bool{}, MailConfigured: a.Mail.IsConfigured(), SitesRoot: a.Config.SitesRoot,
		Sites: []string{}, MailDomains: []string{},
	}

	for _, service := range []string{"nginx", "php", "mysql", "postgres", "mongodb", "redis", "node", "mail"} {
		hello.Services[service] = a.Store.ServiceInstalled(service)
	}

	if list, err := a.Store.Sites(ctx); err == nil {
		for _, site := range list {
			hello.Sites = append(hello.Sites, site.Domain)
		}
	}
	if list, err := a.Store.EmailDomains(ctx); err == nil {
		for _, domain := range list {
			hello.MailDomains = append(hello.MailDomains, domain.Domain)
		}
	}

	return hello, nil
}

// incomingDir is where an incoming transfer's files are written.
func incomingDir(transfer string) string { return migrationRoot + "/in-" + transfer }

func (a *App) migrateBegin(ctx context.Context, payload []byte) (any, error) {
	var begin migrate.Begin
	if err := json.Unmarshal(payload, &begin); err != nil {
		return nil, err
	}

	owner := a.ownerID(ctx)
	transfer := migrate.NewTransferID()

	accountID := int64(0)
	if begin.DNSAccount != nil && dns.ProviderExists(begin.DNSAccount.Provider) && !dns.IsLocal(begin.DNSAccount.Provider) {
		accountID = a.adoptDNSAccount(ctx, owner, begin.DNSAccount)
	}

	directory := incomingDir(transfer)
	if result := proc.Run(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chmod 711 %[2]s && "+
		"sudo chown \"$(id -un)\" %[1]s && sudo chmod 700 %[1]s",
		proc.Quote(directory), proc.Quote(migrationRoot)), time.Minute); !result.OK() {
		return nil, fmt.Errorf("could not make room for the transfer: %s", result.Output)
	}

	var localID int64
	var name string
	var err error

	switch {
	case begin.Kind == "site" && begin.Site != nil:
		name = strings.ToLower(begin.Site.Domain)
		localID, err = a.receiveSite(ctx, owner, accountID, begin.Site)

		// Scheduled once the site is built; until then they would run
		// against files that are not there yet.
		if err == nil && len(begin.Site.Cron) > 0 {
			encoded, _ := json.Marshal(begin.Site.Cron)
			os.WriteFile(directory+"/cron.json", encoded, 0o600)
		}
	case begin.Kind == "mail" && begin.Mail != nil:
		name = strings.ToLower(begin.Mail.Domain)
		localID, err = a.receiveMail(ctx, owner, accountID, begin.Mail, directory)
	default:
		err = errors.New("nothing to receive")
	}

	if err != nil {
		proc.Ok("sudo rm -rf " + proc.Quote(directory))
		return nil, err
	}

	if _, err := a.Store.CreateMigration(ctx, &store.Migration{
		Direction: "in", Kind: begin.Kind, Name: name, Peer: begin.SourceIP, RemoteID: transfer,
		Status: "receiving", Message: "Receiving the files.", LocalID: localID,
	}); err != nil {
		return nil, err
	}

	a.Log.Info("receiving a migration", "kind", begin.Kind, "name", name, "from", begin.SourceIP)

	return migrate.Transfer{Transfer: transfer}, nil
}

// adoptDNSAccount finds this panel's copy of a DNS credential, or keeps the
// one that arrived.
func (a *App) adoptDNSAccount(ctx context.Context, owner int64, incoming *migrate.DNSAccount) int64 {
	if accounts, err := a.Store.DNSAccounts(ctx, owner); err == nil {
		for _, account := range accounts {
			if account.Provider == incoming.Provider && account.APIToken == incoming.APIToken &&
				account.APISecret == incoming.APISecret && account.Email == incoming.Email {
				return account.ID
			}
		}
	}

	label := incoming.Label
	if label == "" {
		label = dns.ProviderLabel(incoming.Provider)
	}

	id, err := a.Store.CreateDNSAccount(ctx, &store.DNSAccount{
		UserID: owner, Provider: incoming.Provider, Label: label + " (moved here)",
		APIToken: incoming.APIToken, APISecret: incoming.APISecret, Email: incoming.Email, AccountID: incoming.AccountID,
	})
	if err != nil {
		return 0
	}

	return id
}

// receiveSite makes the row for a site that is arriving, and its database's.
func (a *App) receiveSite(ctx context.Context, owner, accountID int64, incoming *migrate.Site) (int64, error) {
	domain := strings.ToLower(strings.TrimSpace(incoming.Domain))

	if !validDomain(domain) {
		return 0, errors.New("that is not a domain")
	}
	if _, err := a.Store.FindSiteByDomain(ctx, domain); err == nil {
		return 0, errors.New("this server already has a site called " + domain)
	}
	if a.Store.ActiveMigration(ctx, "site", domain) != nil {
		return 0, errors.New(domain + " is already being received")
	}

	root := filepath.Clean(incoming.RootPath)
	sitesRoot := strings.TrimRight(a.Config.SitesRoot, "/")
	if !strings.HasPrefix(root, sitesRoot+"/") || root != incoming.RootPath || strings.Contains(root, "..") {
		return 0, fmt.Errorf("%s is not inside this server's sites directory (%s)", incoming.RootPath, sitesRoot)
	}

	if incoming.Type == "" || strings.ContainsAny(incoming.Type+incoming.PHPVersion+incoming.NodeVersion, " /;'\"$`") {
		return 0, errors.New("the site's settings are not ones this panel accepts")
	}

	if !a.Store.ServiceInstalled("nginx") {
		return 0, errors.New("this server has no nginx; install it on the Apps page first")
	}

	databaseID := int64(0)
	if db := incoming.Database; db != nil {
		if !a.Store.ServiceInstalled(db.Engine) {
			return 0, fmt.Errorf("%s is not installed here; install it on the Apps page first", config.EngineLabel(db.Engine))
		}
		if a.Store.DatabaseExists(ctx, db.Engine, db.Name) {
			return 0, fmt.Errorf("this server already has a database called %s", db.Name)
		}

		id, err := a.Store.CreateDatabase(ctx, &store.Database{
			UserID: owner, Engine: db.Engine, Name: db.Name, Username: db.Username,
			Password: db.Password, Charset: db.Charset, ManagedBySite: true,
		})
		if err != nil {
			return 0, err
		}
		databaseID = id
	}

	port := incoming.AppPort
	if port > 0 {
		if sites, err := a.Store.Sites(ctx); err == nil {
			for _, other := range sites {
				if other.AppPort == port {
					port = a.Store.NextFreePort(ctx)
					break
				}
			}
		}
	}

	site := &store.Site{
		UserID: owner, DNSAccountID: accountID, DatabaseID: databaseID, Domain: domain, Type: incoming.Type,
		Aliases: incoming.Aliases, RootPath: root, WebDirectory: incoming.WebDirectory,
		PHPVersion: incoming.PHPVersion, NodeVersion: incoming.NodeVersion, SSL: incoming.SSL, AppPort: port,
		StartCommand: incoming.StartCommand, BuildCommand: incoming.BuildCommand,
		Repository: incoming.Repository, Branch: incoming.Branch, RepoAuth: incoming.RepoAuth, RepoToken: incoming.RepoToken,
		WPAdminUser: incoming.WPAdminUser, WPAdminPassword: incoming.WPAdminPass, WPAdminEmail: incoming.WPAdminEmail,
		WPTitle: incoming.WPTitle, ManageDNS: incoming.ManageDNS && accountID > 0,
		DNSType: "A", DNSContent: system.PublicIP(), DNSProxied: incoming.DNSProxied,
	}

	id, err := a.Store.CreateSite(ctx, site)
	if err != nil {
		if databaseID > 0 {
			a.Store.DeleteDatabase(databaseID)
		}
		return 0, err
	}

	site.ID = id
	a.Store.SetSiteStatus(id, "migrating", "Arriving from another server.")

	if incoming.PHPSettings != "" {
		a.Store.SetSitePHPSettings(id, incoming.PHPSettings)
	}
	if incoming.CacheMode != "" {
		a.Store.SetSiteCache(id, incoming.CacheMode, incoming.CacheTTL)
	}
	if incoming.TLSMode == "edge" {
		a.Store.SetSiteTLSMode(id, "edge")
	}
	if incoming.Webhook != "" {
		a.Store.SetSiteWebhookSecret(id, incoming.Webhook)
	}
	if incoming.RepoAuth == "key" && incoming.DeployKey != "" {
		if err := sites.InstallDeployKey(site, incoming.DeployKey, incoming.DeployPublic); err == nil {
			a.Store.SetSiteDeployKey(id, incoming.DeployPublic)
		}
	}

	return id, nil
}

// receiveMail makes the rows for a mail domain that is arriving, and keeps
// its DKIM key beside the files until the import puts it in place.
func (a *App) receiveMail(ctx context.Context, owner, accountID int64, incoming *migrate.Mail, directory string) (int64, error) {
	name := strings.ToLower(strings.TrimSpace(incoming.Domain))

	if !validDomain(name) {
		return 0, errors.New("that is not a domain")
	}
	if !a.Mail.IsConfigured() {
		return 0, errors.New("this server has no mail server set up; install Mail on the Apps page first")
	}
	if _, err := a.Store.FindEmailDomainByName(ctx, name); err == nil {
		return 0, errors.New("this server already has the mail domain " + name)
	}

	for _, box := range incoming.Mailboxes {
		if !validLocalPart(box.LocalPart) {
			return 0, fmt.Errorf("%q is not a mailbox name this panel accepts", box.LocalPart)
		}
	}

	id, err := a.Store.CreateEmailDomain(ctx, &store.EmailDomain{
		UserID: owner, DNSAccountID: accountID, Domain: name, DKIMSelector: incoming.DKIMSelector,
		ManageDNS: incoming.ManageDNS && accountID > 0,
	})
	if err != nil {
		return 0, err
	}

	a.Store.SetEmailDomainStatus(id, "migrating", "Arriving from another server.")

	for _, box := range incoming.Mailboxes {
		if _, err := a.Store.CreateEmailAccount(ctx, &store.EmailAccount{
			EmailDomainID: id, UserID: owner, LocalPart: box.LocalPart, Name: box.Name,
			Password: box.Password, QuotaMB: box.QuotaMB,
		}); err != nil {
			return 0, err
		}
	}

	if incoming.DKIMPrivate != "" {
		if err := os.WriteFile(directory+"/dkim.private", []byte(incoming.DKIMPrivate), 0o600); err != nil {
			return 0, err
		}
	}

	manifest := *incoming
	manifest.DKIMPrivate = ""
	for i := range manifest.Mailboxes {
		manifest.Mailboxes[i].Password = ""
	}
	encoded, _ := json.Marshal(manifest)
	os.WriteFile(directory+"/manifest.json", encoded, 0o600)

	return id, nil
}

// incoming loads a transfer this panel is receiving, by the sender's request.
func (a *App) incoming(ctx context.Context, transfer string) (*store.Migration, error) {
	if !migrate.ValidTransferID(transfer) {
		return nil, errors.New("there is no such transfer")
	}

	m, err := a.Store.FindIncomingMigration(ctx, transfer)
	if err != nil {
		return nil, errors.New("there is no such transfer")
	}

	return m, nil
}

// uploads serialises writes to the files of a transfer.
var uploads sync.Mutex

func (a *App) migrateUpload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	a.migrationAPI(func(ctx context.Context, payload []byte) (any, error) {
		m, err := a.incoming(ctx, query.Get("transfer"))
		if err != nil {
			return nil, err
		}

		name := query.Get("file")
		if !migrate.Files[name] {
			return nil, errors.New("that is not a file a transfer carries")
		}
		if m.Status != "receiving" && !(name == "catch-up.tar.gz" && (m.Status == "finished" || m.Status == "done")) {
			return nil, errors.New("this transfer is not receiving files")
		}

		offset, err := strconv.ParseInt(query.Get("offset"), 10, 64)
		if err != nil || offset < 0 {
			return nil, errors.New("that is not an offset")
		}

		uploads.Lock()
		defer uploads.Unlock()

		file, err := os.OpenFile(incomingDir(m.RemoteID)+"/"+name, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		// A new file starts empty, even if a failed attempt left one.
		if offset == 0 {
			file.Truncate(0)
		}

		if _, err := file.WriteAt(payload, offset); err != nil {
			return nil, err
		}

		return nil, nil
	})(w, r)
}

func (a *App) checkHashes(m *store.Migration, files map[string]string) error {
	for name, want := range files {
		if !migrate.Files[name] {
			return errors.New("that is not a file a transfer carries")
		}

		got, err := migrate.FileHash(incomingDir(m.RemoteID) + "/" + name)
		if err != nil {
			return fmt.Errorf("%s did not arrive", name)
		}
		if got != want {
			return fmt.Errorf("%s arrived damaged — it does not match what was sent; start the move again", name)
		}
	}

	return nil
}

func (a *App) migrateFinish(ctx context.Context, payload []byte) (any, error) {
	var finish migrate.Finish
	if err := json.Unmarshal(payload, &finish); err != nil {
		return nil, err
	}

	m, err := a.incoming(ctx, finish.Transfer)
	if err != nil {
		return nil, err
	}
	if m.Status != "receiving" {
		return nil, errors.New("this transfer is not receiving files")
	}

	if err := a.checkHashes(m, finish.Files); err != nil {
		a.Store.SetMigrationStatus(m.ID, "failed", err.Error())
		return nil, err
	}

	a.Store.SetMigrationStatus(m.ID, "importing", "Setting "+m.Name+" up.")
	a.Queue.PushID(jobMigrateIn, "migration", m.ID)

	return nil, nil
}

func (a *App) migrateStatus(ctx context.Context, payload []byte) (any, error) {
	var transfer migrate.Transfer
	if err := json.Unmarshal(payload, &transfer); err != nil {
		return nil, err
	}

	m, err := a.incoming(ctx, transfer.Transfer)
	if err != nil {
		return nil, err
	}

	return migrate.Status{Status: m.Status, Message: m.Message}, nil
}

func (a *App) migrateFinalize(ctx context.Context, payload []byte) (any, error) {
	var transfer migrate.Transfer
	if err := json.Unmarshal(payload, &transfer); err != nil {
		return nil, err
	}

	m, err := a.incoming(ctx, transfer.Transfer)
	if err != nil {
		return nil, err
	}
	if m.Status != "imported" {
		return nil, errors.New("this transfer is not ready to finish")
	}

	a.Store.SetMigrationStatus(m.ID, "finishing", "Getting the certificates, now the DNS points here.")
	a.Queue.PushID(jobMigrateFinish, "migration", m.ID)

	return nil, nil
}

func (a *App) migrateCatchUpIn(ctx context.Context, payload []byte) (any, error) {
	var finish migrate.Finish
	if err := json.Unmarshal(payload, &finish); err != nil {
		return nil, err
	}

	m, err := a.incoming(ctx, finish.Transfer)
	if err != nil {
		return nil, err
	}
	if m.Kind != "mail" || (m.Status != "finished" && m.Status != "done") {
		return nil, errors.New("this transfer has no mail to catch up")
	}

	if err := a.checkHashes(m, finish.Files); err != nil {
		return nil, err
	}

	a.Store.SetMigrationStatus(m.ID, "catching_up", "Adding the mail that arrived during the move.")
	a.Queue.PushID(jobMigrateCatchUp, "migration", m.ID)

	return nil, nil
}

// migrateCancel takes away what was made for a transfer that never got past
// receiving: the site or mail domain rows, and the files that arrived.
func (a *App) migrateCancel(ctx context.Context, payload []byte) (any, error) {
	var transfer migrate.Transfer
	if err := json.Unmarshal(payload, &transfer); err != nil {
		return nil, err
	}

	m, err := a.incoming(ctx, transfer.Transfer)
	if err != nil {
		return nil, err
	}
	if m.Status != "receiving" && m.Status != "failed" {
		return nil, nil
	}
	if m.Status == "failed" && m.TaskID > 0 {
		// It got as far as being built: left for its owner to look at.
		return nil, nil
	}

	switch m.Kind {
	case "site":
		if site, err := a.Store.FindSite(ctx, m.LocalID); err == nil {
			if site.DatabaseID > 0 {
				a.Store.DeleteDatabase(site.DatabaseID)
			}
			if site.RepoAuth == "key" {
				key := sites.DeployKeyPath(site)
				proc.Ok("sudo rm -f " + proc.Quote(key) + " " + proc.Quote(key+".pub"))
			}
			a.Store.DeleteSite(site.ID)
		}
	case "mail":
		if accounts, err := a.Store.EmailAccounts(ctx, m.LocalID); err == nil {
			for _, account := range accounts {
				a.Store.DeleteEmailAccount(account.ID)
			}
		}
		a.Store.DeleteEmailDomain(m.LocalID)
	}

	proc.Ok("sudo rm -rf " + proc.Quote(incomingDir(m.RemoteID)))
	a.Store.SetMigrationStatus(m.ID, "cancelled", "The other server stopped the move before anything was set up here.")

	return nil, nil
}

// runMigrationIn builds what arrived.
func (a *App) runMigrationIn(ctx context.Context, migrationID int64) error {
	m, err := a.Store.FindMigration(ctx, migrationID)
	if err != nil {
		return err
	}

	directory := incomingDir(m.RemoteID)
	siteID := int64(0)
	if m.Kind == "site" {
		siteID = m.LocalID
	}

	taskID, err := a.Store.CreateTask(ctx, m.Kind, "migrate.in", a.ownerID(ctx), siteID, "Receiving "+m.Name)
	if err != nil {
		return err
	}
	a.Store.SetMigrationTask(m.ID, taskID)

	if m.Kind == "site" {
		err = a.Sites.ImportMigrated(ctx, m.LocalID, taskID, directory)
	} else {
		var manifest migrate.Mail
		if encoded, readErr := os.ReadFile(directory + "/manifest.json"); readErr == nil {
			json.Unmarshal(encoded, &manifest)
		}
		if key, readErr := os.ReadFile(directory + "/dkim.private"); readErr == nil {
			manifest.DKIMPrivate = string(key)
		}
		err = a.Mail.ImportMigrated(ctx, m.LocalID, taskID, directory, &manifest)
	}

	// The archives have done their job; a mail domain's directory stays for
	// the catch-up that follows.
	for _, name := range []string{"files.tar.gz", "database.sql.gz", "database.archive.gz", "mail.tar.gz", "dkim.private"} {
		os.Remove(directory + "/" + name)
	}

	if err != nil {
		a.Store.SetMigrationStatus(m.ID, "failed", err.Error())
		return err
	}

	a.Store.SetMigrationStatus(m.ID, "imported", m.Name+" is set up here, waiting for its DNS to point here.")

	return nil
}

// runMigrationFinish gets the certificates once the DNS points here.
func (a *App) runMigrationFinish(ctx context.Context, migrationID int64) error {
	m, err := a.Store.FindMigration(ctx, migrationID)
	if err != nil {
		return err
	}

	siteID := int64(0)
	if m.Kind == "site" {
		siteID = m.LocalID
	}

	taskID, err := a.Store.CreateTask(ctx, m.Kind, "migrate.in", a.ownerID(ctx), siteID, "Finishing "+m.Name)
	if err != nil {
		return err
	}
	a.Store.SetMigrationTask(m.ID, taskID)

	if m.Kind == "site" {
		err = a.Sites.FinishImport(ctx, m.LocalID, taskID)
		proc.Ok("sudo rm -rf " + proc.Quote(incomingDir(m.RemoteID)))
	} else {
		err = a.Mail.FinishImport(ctx, m.LocalID, taskID)
	}

	// The certificate is the one part that can wait: the site already
	// serves, and Request certificate on its page tries again.
	status := "done"
	if m.Kind == "mail" {
		status = "finished"
	}
	message := m.Name + " has moved here."
	if err != nil {
		message += " Its certificate is not in place yet: " + err.Error()
	}

	a.Store.SetMigrationStatus(m.ID, status, message)

	return nil
}

// runMigrationCatchUp adds mail that reached the old server during the move.
func (a *App) runMigrationCatchUp(ctx context.Context, migrationID int64) error {
	m, err := a.Store.FindMigration(ctx, migrationID)
	if err != nil {
		return err
	}

	taskID, err := a.Store.CreateTask(ctx, "mail", "migrate.in", a.ownerID(ctx), 0, "Catching up "+m.Name)
	if err != nil {
		return err
	}

	archive := incomingDir(m.RemoteID) + "/catch-up.tar.gz"
	list := &tasks.List{}
	list.AddFn("Add the mail that arrived during the move", func(runner *tasks.Runner) (string, error) {
		return a.Mail.ApplyCatchUp(runner, m.Name, archive)
	})

	ok := tasks.New(a.Store, taskID).Run(list)
	os.Remove(archive)

	if !ok {
		a.Store.SetMigrationStatus(m.ID, "failed", "The mail that arrived during the move could not be added.")
		return errors.New("catch-up failed")
	}

	a.Store.SetMigrationStatus(m.ID, "done", m.Name+" has moved here, with the mail that arrived during the move.")

	return nil
}
