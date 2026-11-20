package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/notfound"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/weblog"
)

// 404 protection, from Security → Cloudflare: an address that asks a site for
// pages that do not exist is challenged or blocked at Cloudflare for a while.
//
// Everything here works on Cloudflare's free plan. The counting is done on
// this server (internal/notfound) because free rate limiting rules cannot see
// response codes, and the answer is an IP access rule, which every plan has.
// The panel takes each rule away again when its time is up.

const (
	notFoundEnabledKey   = "notfound_enabled"
	notFoundActionKey    = "notfound_action"
	notFoundThresholdKey = "notfound_threshold"
	notFoundDurationKey  = "notfound_duration_hours"
)

// notFoundActions are the two answers, both on every Cloudflare plan.
var notFoundActions = map[string]string{
	"managed_challenge": "Managed Challenge",
	"block":             "Block",
}

// notFoundDurations are the lengths a rule can last, in hours. 0 is until
// somebody removes it on the Security page.
var notFoundDurations = []int{1, 6, 12, 24, 72, 168, 720, 2160, 0}

type notFoundSettings struct {
	Enabled   bool   `json:"enabled"`
	Action    string `json:"action"`
	Threshold int    `json:"threshold"`
	Hours     int    `json:"hours"`
}

func (a *App) notFoundSettings() notFoundSettings {
	settings := notFoundSettings{
		Enabled:   a.Store.SettingBool(notFoundEnabledKey, false),
		Action:    a.Store.Setting(notFoundActionKey, "managed_challenge"),
		Threshold: 1,
		Hours:     24,
	}

	if n, err := strconv.Atoi(a.Store.Setting(notFoundThresholdKey, "")); err == nil && n >= 1 && n <= 100 {
		settings.Threshold = n
	}

	if n, err := strconv.Atoi(a.Store.Setting(notFoundDurationKey, "")); err == nil && n >= 0 {
		settings.Hours = n
	}

	if notFoundActions[settings.Action] == "" {
		settings.Action = "managed_challenge"
	}

	return settings
}

// notFoundGuard is the running part: the watcher, the counts, the zones each
// hostname is in, and the clock that takes rules away.
type notFoundGuard struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	counter notfound.Counter

	// Hostnames already being dealt with, so a burst of 404s from one address
	// makes one rule rather than one per request in flight.
	inFlight map[string]bool

	zones   map[string]zoneAnswer
	crawler map[string]time.Time // addresses verified as search engines

	// Every zone a site on this server is in, worked out at most once an hour
	// and only when there is an address to send.
	allZones   []zoneAnswer
	allZonesAt time.Time

	// How many addresses are being sent to Cloudflare at once.
	sending chan struct{}

	lastError   string
	lastErrorAt time.Time

	expiry *time.Timer
	work   chan notfound.Hit
}

type zoneAnswer struct {
	accountID int64
	zoneID    string
	zoneName  string
	at        time.Time
}

var notFoundState = &notFoundGuard{
	sending:  make(chan struct{}, 2),
	inFlight: map[string]bool{},
	zones:    map[string]zoneAnswer{},
	crawler:  map[string]time.Time{},
}

func (g *notFoundGuard) fail(format string, args ...any) {
	g.mu.Lock()
	g.lastError = fmt.Sprintf(format, args...)
	g.lastErrorAt = time.Now()
	g.mu.Unlock()
}

// EnsureNotFoundGuard starts the watcher when the protection is on, and puts
// the rules still to be taken away back on the clock. On every start.
func (a *App) EnsureNotFoundGuard(ctx context.Context) {
	a.scheduleNotFoundExpiry(ctx)

	if !a.notFoundSettings().Enabled {
		return
	}

	if err := a.applyNotFoundNginx(true); err != nil {
		notFoundState.fail("%v", err)
		a.Log.Error("could not switch 404 protection on in nginx", "error", err)

		return
	}

	a.startNotFoundWatch(ctx)
}

// applyNotFoundNginx writes or removes the two nginx files. The http-level
// file goes in first and comes out last, because the include refers to it.
func (a *App) applyNotFoundNginx(on bool) error {
	if !on {
		proc.Ok("sudo rm -f " + proc.Quote(notfound.GuardPath))
		proc.Ok("sudo rm -f " + proc.Quote(notfound.MapPath))
		proc.Ok("sudo nginx -t && sudo systemctl reload nginx")

		return nil
	}

	if err := ensureNotFoundLog(); err != nil {
		return err
	}

	current, _ := proc.ReadFile(notfound.MapPath, true)
	guard, _ := proc.ReadFile(notfound.GuardPath, true)

	if current == notfound.MapConfig() && guard == notfound.GuardConfig() {
		return nil
	}

	if err := proc.PutFile(notfound.MapPath, notfound.MapConfig(), true); err != nil {
		return err
	}

	if err := proc.PutFile(notfound.GuardPath, notfound.GuardConfig(), true); err != nil {
		proc.Ok("sudo rm -f " + proc.Quote(notfound.MapPath))
		return err
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		proc.Ok("sudo rm -f " + proc.Quote(notfound.GuardPath) + " " + proc.Quote(notfound.MapPath))

		return fmt.Errorf("nginx refused the 404 log, so nothing was changed: %s", check.Output)
	}

	if !proc.Ok("sudo systemctl reload nginx") {
		return fmt.Errorf("nginx did not reload")
	}

	return nil
}

// ensureNotFoundLog makes the log the panel's own, so it can read and empty
// it; nginx opens it as root and only ever appends.
func ensureNotFoundLog() error {
	file, err := os.OpenFile(notfound.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("could not create %s: %w", notfound.LogPath, err)
	}

	return file.Close()
}

func (a *App) startNotFoundWatch(ctx context.Context) {
	g := notFoundState

	g.mu.Lock()
	if g.cancel != nil {
		g.mu.Unlock()
		return
	}

	watchCtx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	g.work = make(chan notfound.Hit, 256)
	work := g.work
	g.mu.Unlock()

	// One worker: Cloudflare is asked about one address at a time, which is
	// plenty for a log that only grows on a 404.
	go func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case hit := <-work:
				a.handleNotFound(watchCtx, hit)
			}
		}
	}()

	go func() {
		defer func() {
			g.mu.Lock()
			g.cancel = nil
			g.mu.Unlock()
		}()

		err := notfound.Follow(watchCtx, notfound.LogPath, func(line string) {
			hit, ok := notfound.ParseLine(line)
			if !ok {
				return
			}

			select {
			case work <- hit:
			default: // a flood: the counts catch up from the next lines
			}
		}, func() error {
			if err := ensureNotFoundLog(); err != nil {
				return err
			}
			proc.Ok("sudo nginx -s reopen")
			return nil
		})

		if err != nil {
			g.fail("Stopped reading the 404 log: %v", err)
			a.Log.Error("404 protection stopped", "error", err)
		}
	}()

	a.Log.Info("404 protection is watching for visitors asking for missing pages")
}

func (a *App) stopNotFoundWatch() {
	g := notFoundState

	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	g.mu.Unlock()
}

// handleNotFound counts one 404 and, at the threshold, hands the address to
// blockEverywhere.
//
// Counted across every site: a scanner walking twenty domains a few missing
// pages at a time is as much a scanner as one asking a single site for sixty.
func (a *App) handleNotFound(ctx context.Context, hit notfound.Hit) {
	settings := a.notFoundSettings()
	if !settings.Enabled {
		return
	}

	if notfound.Ignored(hit.IP, []string{system.PublicIP()}, security.FromCloudflare) {
		return
	}

	target, value := notfound.Key(hit.IP)

	if a.notFoundCount(value) < settings.Threshold {
		return
	}

	g := notFoundState

	g.mu.Lock()
	if g.inFlight[value] || len(g.inFlight) >= notFoundMaxPending {
		// Already on its way, or a flood bigger than the queue: the count
		// stands, and the next 404 from this address tries again.
		g.mu.Unlock()
		return
	}
	g.inFlight[value] = true
	g.mu.Unlock()

	// The Cloudflare calls happen off the reading loop, so one address being
	// sent to thirty zones does not hold up the counting of the next.
	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.inFlight, value)
			g.mu.Unlock()
		}()

		g.sending <- struct{}{}
		defer func() { <-g.sending }()

		a.blockEverywhere(ctx, hit, target, value, settings)
	}()
}

// notFoundMaxPending is how many addresses may be waiting to be sent at once.
const notFoundMaxPending = 64

// blockEverywhere makes the rule for one address in every Cloudflare zone a
// site on this server is in — not only the one it was scanning — so it cannot
// move on to the next domain on the list.
func (a *App) blockEverywhere(ctx context.Context, hit notfound.Hit, target, value string, settings notFoundSettings) {
	g := notFoundState

	if a.verifiedCrawler(ctx, hit) {
		return
	}

	zones := a.notFoundZones(ctx, hit.Host)
	if len(zones) == 0 {
		g.fail("%s reached the limit, but none of this server's domains is in a connected Cloudflare account.", value)
		return
	}

	pending := []zoneAnswer{}
	for _, zone := range zones {
		if !a.Store.NotFoundBlocked(ctx, value, zone.zoneID) {
			pending = append(pending, zone)
		}
	}

	g.counter.Forget(value, notFoundAnyHost)

	if len(pending) == 0 {
		return
	}

	// expiresAt 0 is a rule kept until somebody removes it.
	expiresAt, until := int64(0), "until removed in the panel"
	if settings.Hours > 0 {
		expires := time.Now().Add(time.Duration(settings.Hours) * time.Hour)
		expiresAt, until = expires.Unix(), "until "+expires.UTC().Format("2006-01-02 15:04")+" UTC"
	}

	notes := fmt.Sprintf("Ubuntu Panel 404 protection: %s%s — %s", hit.Host, truncate(hit.Path, 80), until)

	clients := map[int64]*dns.Client{}
	for _, zone := range pending {
		if clients[zone.accountID] != nil {
			continue
		}
		if account, err := a.Store.FindDNSAccount(ctx, zone.accountID, 0); err == nil {
			clients[zone.accountID] = dns.New(a.Store, account)
		}
	}

	// A few zones at a time: quick for a server with many domains, and far
	// inside Cloudflare's 1,200 requests in five minutes.
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		made    int
		refused []string
		slots   = make(chan struct{}, 4)
	)

	for _, zone := range pending {
		client := clients[zone.accountID]
		if client == nil {
			continue
		}

		wg.Add(1)
		slots <- struct{}{}

		go func(zone zoneAnswer) {
			defer func() { <-slots; wg.Done() }()

			ruleID, err := client.CreateAccessRuleID(zone.zoneID, dns.AccessRule{
				Mode: settings.Action, Target: target, Value: value, Notes: notes,
			})
			if err != nil {
				// One already there — made by hand, an allow rule, or another
				// server's panel — is left alone.
				message := strings.ToLower(err.Error())
				if strings.Contains(message, "duplicate") || strings.Contains(message, "already exists") {
					return
				}

				a.Log.Warn("404 protection could not make a rule", "ip", value, "zone", zone.zoneName, "error", err)

				mu.Lock()
				refused = append(refused, zone.zoneName+": "+err.Error())
				mu.Unlock()

				return
			}

			a.Store.AddNotFoundBlock(ctx, &store.NotFoundBlock{
				IP:        value,
				Host:      hit.Host,
				Path:      truncate(hit.Path, 200),
				AccountID: zone.accountID,
				ZoneID:    zone.zoneID,
				ZoneName:  zone.zoneName,
				RuleID:    ruleID,
				Mode:      settings.Action,
				Hits:      settings.Threshold,
				CreatedAt: time.Now().Unix(),
				ExpiresAt: expiresAt,
			})

			mu.Lock()
			made++
			mu.Unlock()
		}(zone)
	}

	wg.Wait()

	if len(refused) > 0 {
		g.fail("Cloudflare refused the rule for %s on %d of %d domains — %s", value, len(refused), len(pending),
			refused[0])
	} else {
		g.mu.Lock()
		g.lastError = ""
		g.mu.Unlock()
	}

	if made > 0 {
		a.Log.Info("404 protection", "ip", value, "domains", made, "action", settings.Action,
			"host", hit.Host, "path", hit.Path)

		a.scheduleNotFoundExpiry(context.Background())
	}
}

// notFoundAnyHost is the one bucket every site's 404s are counted in.
const notFoundAnyHost = "*"

func (a *App) notFoundCount(key string) int {
	return notFoundState.counter.Add(key, notFoundAnyHost, time.Now())
}

// verifiedCrawler is whether the address is a search engine's crawler, proved
// by its reverse DNS — Googlebot finding a dead link is not an attack, and a
// challenge would keep it from the rest of the site. Remembered for a day.
func (a *App) verifiedCrawler(ctx context.Context, hit notfound.Hit) bool {
	bot := weblog.FindGoodBot(hit.Agent)
	if bot == nil || len(bot.Domains) == 0 {
		return false
	}

	g := notFoundState

	g.mu.Lock()
	at, known := g.crawler[hit.IP]
	g.mu.Unlock()

	if known && time.Since(at) < 24*time.Hour {
		return true
	}

	if _, verified := weblog.CheckDNS(ctx, hit.IP, bot.Domains); !verified {
		return false
	}

	g.mu.Lock()
	g.crawler[hit.IP] = time.Now()
	g.mu.Unlock()

	return true
}

// notFoundZone is the Cloudflare zone a hostname is in, and a client for the
// credential that holds it: the site's own, when the panel manages its DNS,
// and otherwise whichever connected Cloudflare account has the zone. Answers
// are kept for an hour, including "no zone".
func (a *App) notFoundZone(ctx context.Context, host string) (zoneAnswer, *dns.Client, bool) {
	g := notFoundState

	g.mu.Lock()
	cached, ok := g.zones[host]
	g.mu.Unlock()

	if !ok || time.Since(cached.at) > time.Hour {
		cached = a.findNotFoundZone(ctx, host)

		g.mu.Lock()
		g.zones[host] = cached
		g.mu.Unlock()
	}

	if cached.zoneID == "" {
		return cached, nil, false
	}

	account, err := a.Store.FindDNSAccount(ctx, cached.accountID, 0)
	if err != nil || !dns.SupportsEdgeRules(account.Provider) {
		return cached, nil, false
	}

	return cached, dns.New(a.Store, account), true
}

// notFoundZones is every Cloudflare zone a rule goes into: the zone of each
// site on this server, across every connected Cloudflare account, and the
// zone of the hostname that was asked for, should it be one no site names.
func (a *App) notFoundZones(ctx context.Context, host string) []zoneAnswer {
	g := notFoundState

	g.mu.Lock()
	zones, at := g.allZones, g.allZonesAt
	g.mu.Unlock()

	if at.IsZero() || time.Since(at) > time.Hour {
		zones = a.findSiteZones(ctx)

		g.mu.Lock()
		g.allZones, g.allZonesAt = zones, time.Now()
		g.mu.Unlock()
	}

	result := append([]zoneAnswer{}, zones...)

	if own, _, ok := a.notFoundZone(ctx, host); ok {
		seen := false
		for _, zone := range result {
			seen = seen || zone.zoneID == own.zoneID
		}
		if !seen {
			result = append(result, own)
		}
	}

	return result
}

// findSiteZones lists each connected Cloudflare account's zones once and keeps
// the ones a site's hostname is in. One zone listing per account, rather than
// one per hostname.
func (a *App) findSiteZones(ctx context.Context) []zoneAnswer {
	sites, err := a.Store.Sites(ctx)
	if err != nil {
		return nil
	}

	hostnames := []string{}
	for _, site := range sites {
		for _, name := range site.Hostnames() {
			hostnames = append(hostnames, strings.TrimRight(strings.ToLower(name), "."))
		}
	}

	accounts, err := a.Store.EdgeDNSAccounts(ctx)
	if err != nil {
		return nil
	}

	found := []zoneAnswer{}
	seen := map[string]bool{}

	for _, account := range accounts {
		if !dns.SupportsEdgeRules(account.Provider) {
			continue
		}

		zones, err := dns.New(a.Store, account).Zones()
		if err != nil {
			a.Log.Warn("404 protection could not list a Cloudflare account's zones", "account", account.ID, "error", err)
			continue
		}

		for _, zone := range zones {
			name := strings.TrimRight(strings.ToLower(zone.Name), ".")
			if seen[zone.ID] || !zoneHasSite(name, hostnames) {
				continue
			}

			seen[zone.ID] = true
			found = append(found, zoneAnswer{
				accountID: account.ID, zoneID: zone.ID, zoneName: name, at: time.Now(),
			})
		}
	}

	return found
}

// zoneHasSite is whether any of the hostnames is the zone or inside it.
func zoneHasSite(zone string, hostnames []string) bool {
	for _, host := range hostnames {
		if host == zone || strings.HasSuffix(host, "."+zone) {
			return true
		}
	}

	return false
}

func (a *App) findNotFoundZone(ctx context.Context, host string) zoneAnswer {
	answer := zoneAnswer{at: time.Now()}

	if sites, err := a.Store.Sites(ctx); err == nil {
		for _, site := range sites {
			if site.DNSAccountID == 0 || site.DNSZoneID == "" {
				continue
			}

			for _, name := range site.Hostnames() {
				if strings.EqualFold(name, host) {
					if account, err := a.Store.FindDNSAccount(ctx, site.DNSAccountID, 0); err == nil &&
						dns.SupportsEdgeRules(account.Provider) {
						if zoneID, zoneName, err := dns.New(a.Store, account).FindZone(host); err == nil {
							answer.accountID, answer.zoneID, answer.zoneName = account.ID, zoneID, zoneName
							return answer
						}
					}
				}
			}
		}
	}

	accounts, err := a.Store.EdgeDNSAccounts(ctx)
	if err != nil {
		return answer
	}

	for _, account := range accounts {
		if !dns.SupportsEdgeRules(account.Provider) {
			continue
		}

		if zoneID, zoneName, err := dns.New(a.Store, account).FindZone(host); err == nil {
			answer.accountID, answer.zoneID, answer.zoneName = account.ID, zoneID, zoneName
			return answer
		}
	}

	return answer
}

// scheduleNotFoundExpiry sets one timer for the soonest rule due to go. With
// no rules there is no timer at all.
func (a *App) scheduleNotFoundExpiry(ctx context.Context) {
	g := notFoundState

	next := a.Store.NextNotFoundExpiry(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.expiry != nil {
		g.expiry.Stop()
		g.expiry = nil
	}

	if next.IsZero() {
		return
	}

	wait := time.Until(next)
	if wait < time.Second {
		wait = time.Second
	}

	g.expiry = time.AfterFunc(wait, func() { a.expireNotFoundBlocks() })
}

// expireNotFoundBlocks takes away the rules whose time is up. One Cloudflare
// refuses to remove is tried again in ten minutes; one already gone is simply
// forgotten.
func (a *App) expireNotFoundBlocks() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	blocks, _ := a.Store.ExpiredNotFoundBlocks(ctx, time.Now())

	retry := false
	for _, block := range blocks {
		if err := a.liftNotFoundBlock(ctx, block); err != nil {
			retry = true
		}
	}

	if retry {
		notFoundState.mu.Lock()
		if notFoundState.expiry != nil {
			notFoundState.expiry.Stop()
		}
		notFoundState.expiry = time.AfterFunc(10*time.Minute, func() { a.expireNotFoundBlocks() })
		notFoundState.mu.Unlock()

		return
	}

	a.scheduleNotFoundExpiry(ctx)
}

func (a *App) liftNotFoundBlock(ctx context.Context, block *store.NotFoundBlock) error {
	account, err := a.Store.FindDNSAccount(ctx, block.AccountID, 0)
	if err != nil {
		// The credential is gone, and with it any way to reach the rule.
		return a.Store.DeleteNotFoundBlock(ctx, block.ID)
	}

	if block.RuleID != "" {
		if err := dns.New(a.Store, account).DeleteAccessRule(block.ZoneID, block.RuleID); err != nil {
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "not found") && !strings.Contains(message, "404") &&
				!strings.Contains(message, "does not exist") {
				a.Log.Warn("could not lift a 404 protection rule", "ip", block.IP, "zone", block.ZoneName, "error", err)
				return err
			}
		}
	}

	return a.Store.DeleteNotFoundBlock(ctx, block.ID)
}

// ------------------------------------------------------------------ pages

func (a *App) notFoundStatus(ctx context.Context) map[string]any {
	blocks, _ := a.Store.NotFoundAddresses(ctx, 200)

	g := notFoundState

	g.mu.Lock()
	watching := g.cancel != nil
	lastError, lastErrorAt := g.lastError, g.lastErrorAt
	g.mu.Unlock()

	var errorAt any
	if lastError != "" {
		errorAt = lastErrorAt.Unix()
	}

	actions := []map[string]string{}
	for _, key := range []string{"managed_challenge", "block"} {
		actions = append(actions, map[string]string{"key": key, "label": notFoundActions[key]})
	}

	return map[string]any{
		"settings":   a.notFoundSettings(),
		"watching":   watching,
		"blocks":     blocks,
		"total":      a.Store.CountNotFoundBlocks(ctx),
		"last_error": nullable(lastError),
		"error_at":   errorAt,
		"actions":    actions,
		"durations":  notFoundDurations,
	}
}

func (a *App) notFoundShow(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.notFoundStatus(r.Context()))
}

// notFoundSave switches the protection on or off and sets how it answers.
func (a *App) notFoundSave(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "That request could not be read."})
		return
	}

	enabled := input["enabled"] == "1" || input["enabled"] == "true"
	action := input["action"]
	threshold, _ := strconv.Atoi(input["threshold"])
	hours, _ := strconv.Atoi(input["hours"])

	if notFoundActions[action] == "" {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose Managed Challenge or Block."})
		return
	}

	if threshold < 1 || threshold > 100 {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "The number of 404s is between 1 and 100."})
		return
	}

	known := false
	for _, d := range notFoundDurations {
		known = known || d == hours
	}
	if !known {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Choose how long a rule lasts."})
		return
	}

	if enabled && len(a.edgeAccounts(r.Context(), userFrom(r.Context()).ID)) == 0 {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "Connect a Cloudflare account on the DNS page first: the rules are made there.",
		})
		return
	}

	if err := a.applyNotFoundNginx(enabled); err != nil {
		a.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}

	a.Store.SetSetting(notFoundActionKey, action)
	a.Store.SetSetting(notFoundThresholdKey, strconv.Itoa(threshold))
	a.Store.SetSetting(notFoundDurationKey, strconv.Itoa(hours))

	if enabled {
		a.Store.SetSetting(notFoundEnabledKey, "1")
		a.startNotFoundWatch(context.Background())
	} else {
		a.Store.SetSetting(notFoundEnabledKey, "0")
		a.stopNotFoundWatch()
	}

	notFoundState.mu.Lock()
	notFoundState.lastError = ""
	notFoundState.mu.Unlock()

	a.writeJSON(w, http.StatusOK, a.notFoundStatus(r.Context()))
}

// notFoundLift lets one address through again on every domain now, or every
// address with id "all".
func (a *App) notFoundLift(w http.ResponseWriter, r *http.Request) {
	var blocks []*store.NotFoundBlock

	if r.PathValue("block") == "all" {
		blocks, _ = a.Store.AllNotFoundBlocks(r.Context())
	} else {
		block, err := a.Store.FindNotFoundBlock(r.Context(), routeID(r, "block"))
		if err != nil {
			a.writeJSON(w, http.StatusNotFound, map[string]any{"error": "That rule is already gone."})
			return
		}

		// The address comes off every domain, as it went on.
		blocks, _ = a.Store.NotFoundBlocksFor(r.Context(), block.IP)
	}

	failed := 0
	for _, block := range blocks {
		if err := a.liftNotFoundBlock(r.Context(), block); err != nil {
			failed++
		}
	}

	a.scheduleNotFoundExpiry(r.Context())

	status := a.notFoundStatus(r.Context())
	if failed > 0 {
		status["error"] = fmt.Sprintf("Cloudflare would not remove %d of them; try again in a moment.", failed)
		a.writeJSON(w, http.StatusUnprocessableEntity, status)
		return
	}

	a.writeJSON(w, http.StatusOK, status)
}
