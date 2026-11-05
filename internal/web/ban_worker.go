package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
)

// The ban worker: the panel's own watcher of fail2ban, which gives nginx every
// ban and unban the moment fail2ban makes it.
//
// For a site behind Cloudflare, nginx is the only thing that can refuse a
// visitor — the firewall sees Cloudflare — and nginx learns about bans from
// files the panel writes. Waiting for the minute's sync left an attacker
// minutes; a fail2ban action that wrote the file itself depended on what
// fail2ban's service is allowed to touch, which is not the same everywhere.
// So the panel follows fail2ban's log as it is written, and does the work
// itself, with its own sudo:
//
//   - a ban in any jail the sites honour (not the SSH jail, which promises the
//     sites still load): the address goes into nginx's instant list and nginx
//     reloads — a second or so after the request that earned it;
//   - an unban: the address comes off the instant list, and the full list is
//     rebuilt from what fail2ban says now, so nothing keeps refusing it.
//
// The minute's sync stays, as the net under this one.

// fail2banEvent is the part of a fail2ban log line the worker acts on:
//
//	2026-11-05 10:00:00,123 fail2ban.actions [811]: NOTICE  [panel-web-secret] Ban 203.0.113.9
//	NOTICE  [panel-web-secret] Unban 203.0.113.9        (the journal, -o cat)
//
// "Restore Ban" is fail2ban putting a ban back as it starts, "Increase Ban" a
// longer one for an address seen again; both are bans.
var fail2banEvent = regexp.MustCompile(`\[([^\]\s]+)\]\s+(?:(?:Restore|Increase)\s+)?(Ban|Unban)\s+(\S+)\s*$`)

type banEvent struct {
	jail    string
	ban     bool
	address string
}

func parseFail2banEvent(line string) (banEvent, bool) {
	m := fail2banEvent.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil || !security.ValidAddress(m[3]) {
		return banEvent{}, false
	}

	return banEvent{jail: m[1], ban: m[2] == "Ban", address: m[3]}, true
}

// runBanWorker follows fail2ban for as long as the panel runs, starting again
// whenever the follow ends: fail2ban restarted, the log rotated away, or
// neither nginx nor fail2ban there yet.
func (a *App) runBanWorker(ctx context.Context) {
	for {
		if a.Store.ServiceInstalled("nginx") && proc.Which("fail2ban-client") {
			a.followFail2ban(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// followFail2ban reads fail2ban's log from now on — its file when it writes
// one, otherwise the journal — and hands every ban and unban to nginx.
func (a *App) followFail2ban(ctx context.Context) {
	command := "sudo journalctl -f -n 0 -o cat -u fail2ban"
	if proc.Ok("sudo test -s /var/log/fail2ban.log") {
		command = "sudo tail -n 0 -F /var/log/fail2ban.log 2>/dev/null"
	}

	reader, writer := io.Pipe()
	events := make(chan banEvent, 256)

	go func() {
		defer close(events)

		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		for scanner.Scan() {
			if event, ok := parseFail2banEvent(scanner.Text()); ok && event.jail != security.Jail {
				events <- event
			}
		}

		io.Copy(io.Discard, reader)
	}()

	go func() {
		// A day at most, then a fresh follow: nothing is lost between the two
		// that the minute's sync does not catch.
		err := proc.Pipe(ctx, command, 24*time.Hour, nil, writer)
		writer.CloseWithError(err)
	}()

	a.handleBanEvents(ctx, events)
}

// handleBanEvents applies the events in order. Two things wait a moment so a
// burst is answered once: the Security page's view of the bans, re-read half a
// second after the last event, and — after unbans — the rebuild of nginx's
// full list from that view.
func (a *App) handleBanEvents(ctx context.Context, events <-chan banEvent) {
	var (
		reread  <-chan time.Time
		rebuild bool
	)

	settle := func() {
		reread = nil
		a.refreshBans()
		if rebuild {
			rebuild = false
			a.syncNginxBans(ctx, false)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-reread:
			settle()

		case event, ok := <-events:
			if !ok {
				if reread != nil {
					settle()
				}
				return
			}

			if reread == nil {
				reread = time.After(500 * time.Millisecond)
			}

			if event.ban {
				started := time.Now()
				if err := security.BanInstant(event.address); err != nil {
					a.Log.Warn("nginx could not be told about a ban", "ip", event.address, "jail", event.jail, "error", err)
					continue
				}
				a.Log.Info("nginx refuses a banned address", "ip", event.address, "jail", event.jail,
					"took", time.Since(started).Round(time.Millisecond).String())
				continue
			}

			if err := security.ReleaseInstant(event.address); err != nil {
				a.Log.Warn("nginx could not be told about an unban", "ip", event.address, "jail", event.jail, "error", err)
			}
			rebuild = true
		}
	}
}

// refreshBans re-reads what the Banned IPs tab shows — the jails, when each
// banned what, and the firewall's blocks — into the Security page's cached
// copy, leaving the slow parts of it (SSH history, updates) as they are. The
// tab asks for the list every couple of seconds, so a ban is on it about as
// soon as nginx refuses it. With no cached copy there is nothing to do: the
// next page reads everything afresh anyway.
func (a *App) refreshBans() {
	securityCache.Lock()
	current, generation := securityCache.value, securityCache.generation
	securityCache.Unlock()

	if current == nil {
		return
	}

	// One after another rather than all at once: each is a Python start-up
	// (ufw, fail2ban-client), and a burst of bans asking for several at once
	// on a two-core VPS is the panel taking both cores from the sites.
	var (
		next = *current
		p    = a.protection()
	)

	next.Protect = map[string]security.JailStatus{}
	next.BanTimes = map[string]map[string]security.BanTime{}

	next.Jail = security.Status()
	next.Firewall = security.ReadFirewall()

	if p.AnyJail() {
		next.Protect = security.ProtectStatus(p)
	}

	for _, jail := range append([]string{security.Jail}, p.ProtectJails()...) {
		next.BanTimes[jail] = security.BanTimes(jail)
	}

	securityCache.Lock()
	if securityCache.generation == generation && securityCache.value == current {
		securityCache.value = &next
		securityCache.generation++
	}
	securityCache.Unlock()
}

// securityBansLive is the Banned IPs tab's list on its own, which the tab
// asks for every couple of seconds while it is open.
func (a *App) securityBansLive(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"bans": a.bans(r.Context(), a.snapshot(false))})
}
