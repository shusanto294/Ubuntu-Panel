package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/serverlog"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
)

// The Hardening tab's switches: kernel network hardening, not answering ping,
// PHP hardening and compilers kept from site accounts. Each is a file the
// switch writes and the machine reads once; none of them watches anything or
// runs while the server works, so none of them costs CPU.
//
// They used to share the Protection tab with fail2ban jails, nginx rate limits,
// country rules, a browser check and a WAF. Those banned and throttled
// addresses, and every one of them read logs or reloaded nginx to do it; they
// are gone (see security/cleanup.go). The switches kept their setting, so a
// machine that had them on keeps them on.

const (
	hardeningSetting      = "security_protection"
	kernelPreviousSetting = "security_kernel_previous"
	pingPreviousSetting   = "security_ping_previous"

	// ipBlockingRemovedSetting is set once the leftovers of the address
	// blocking are off the machine.
	ipBlockingRemovedSetting = "security_ip_blocking_removed"
)

// serverHardening is the switches, as saved. The names are the ones the old
// Protection settings used, so what was saved then is read as it was.
type serverHardening struct {
	Kernel    bool `json:"kernel"`
	NoPing    bool `json:"no_ping"`
	PHPHarden bool `json:"php_harden"`
	Compilers bool `json:"compilers"`
}

func (a *App) hardening() serverHardening {
	h := serverHardening{}
	a.settingJSON(hardeningSetting, &h)

	return h
}

func (a *App) saveHardening(h serverHardening) { a.saveJSON(hardeningSetting, h) }

// retiredSettings are what the address blocking, the traffic scans and the
// background watches kept in the settings table.
var retiredSettings = []string{
	"security_ssh_jail", "security_fail2ban_offered", "security_trusted_applied",
	"security_access_logs", "security_protection_failures", "security_challenge_secret",
	"security_countries_refreshed", "security_block_times", "security_watch_baseline",
	"security_watch_changes", "security_code_changes", "security_site_work",
	"security_ssh_trap", "security_scan_result", "security_scan_unreported",
	"alert_ssh_ban", "alert_ssh_banned", "alert_web_ban", "alert_web_banned",
	"alert_attacks", "alert_attacks_seen", "alert_suspicious_process",
}

// EnsureHardening runs at start. Once, it takes the address blocking an
// earlier version set up off the machine (security.RemoveIPBlocking). Every
// time, it puts back a switch that is on and whose file has gone — a sysctl
// file somebody deleted, a PHP version installed since — and the port
// forwards, which a firewall reset drops.
func (a *App) EnsureHardening(ctx context.Context) {
	if a.Store.Setting(ipBlockingRemovedSetting, "") == "" {
		done, err := security.RemoveIPBlocking()
		if err != nil {
			a.Log.Error("could not take all of the address blocking off the machine", "error", err)
		}
		if len(done) > 0 {
			a.Log.Info("address blocking removed: " + strings.Join(done, "; "))
		}

		for _, key := range retiredSettings {
			a.Store.ForgetSetting(key)
		}

		// The code change monitor's snapshots of every site.
		removeIntegritySnapshots(a.dataDir())

		if err == nil {
			a.Store.SetSetting(ipBlockingRemovedSetting, time.Now().Format(time.RFC3339))
		}
	}

	// The panel's own lines left the Server logs tab in 2.15.1; the ones
	// collected before that go with them, once.
	if a.Store.Setting("server_events_panel_cleared", "") == "" {
		removed := a.Store.ForgetServerEvents(ctx,
			[]string{serverlog.SourceSystem, serverlog.SourceAuth, serverlog.SourceNginx},
			[]string{"ubuntu-panel", "Ubuntu Panel", "Reloading ", "Reloaded ", "signal process started"},
			serverlog.PanelCaused)
		a.Store.SetSetting("server_events_panel_cleared", "1")
		a.Log.Info("removed the panel's own lines from the server logs", "events", removed)
	}

	h := a.hardening()

	if h.Kernel && !security.KernelApplied() {
		if err := security.ApplyKernel(true, nil); err != nil {
			a.Log.Error("could not apply the kernel network settings", "error", err)
		}
	}

	// A PHP version installed since is hardened like the rest.
	if h.PHPHarden && !security.PHPHardeningApplied() {
		if err := security.ApplyPHPHardening(true); err != nil {
			a.Log.Error("could not harden PHP", "error", err)
		}
	}

	if h.NoPing && !security.PingApplied() {
		if err := security.ApplyNoPing(true, nil); err != nil {
			a.Log.Error("could not stop the server answering ping", "error", err)
		}
	}

	// Port forwards survive a firewall reset.
	if forwards := a.forwards(); len(forwards) > 0 {
		if err := security.ApplyForwards(forwards); err != nil {
			a.Log.Error("could not put the port forwards back", "error", err)
		}
	}
}

// ------------------------------------------------------------------ handler

// securityServerHardening switches one of the hardening switches on or off.
func (a *App) securityServerHardening(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	h := a.hardening()
	enabled := boolInput(input, "enabled", false)
	feature := input["feature"]

	var label string
	var apply func() error

	switch feature {
	case "kernel":
		label = "Network hardening"
		apply = func() error {
			if enabled && !h.Kernel {
				encoded, _ := json.Marshal(security.KernelValues())
				a.Store.SetSetting(kernelPreviousSetting, string(encoded))
			}
			var previous map[string]string
			json.Unmarshal([]byte(a.Store.Setting(kernelPreviousSetting, "{}")), &previous)
			if err := security.ApplyKernel(enabled, previous); err != nil {
				return err
			}
			h.Kernel = enabled
			return nil
		}
	case "no_ping":
		label = "Ignoring ping"
		apply = func() error {
			if enabled && !h.NoPing {
				encoded, _ := json.Marshal(security.PingValues())
				a.Store.SetSetting(pingPreviousSetting, string(encoded))
			}
			var previous map[string]string
			json.Unmarshal([]byte(a.Store.Setting(pingPreviousSetting, "{}")), &previous)
			if err := security.ApplyNoPing(enabled, previous); err != nil {
				return err
			}
			h.NoPing = enabled
			return nil
		}
	case "php_harden":
		label = "PHP hardening"
		apply = func() error {
			if err := security.ApplyPHPHardening(enabled); err != nil {
				return err
			}
			h.PHPHarden = enabled
			return nil
		}
	case "compilers":
		label = "Compilers for administrators only"
		apply = func() error {
			if err := security.RestrictCompilers(enabled, a.Config.SystemUser); err != nil {
				return err
			}
			h.Compilers = enabled
			return nil
		}
	default:
		a.securityBack(w, r, "That is not one of the hardening switches.", true)
		return
	}

	if err := apply(); err != nil {
		a.securityBack(w, r, err.Error(), true)
		return
	}

	a.saveHardening(h)
	a.Log.Info("hardening changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
	a.securityBack(w, r, label+map[bool]string{true: " is on.", false: " is off."}[enabled], false)
}

// hardeningProps are what the Hardening tab shows.
func (a *App) hardeningProps(ctx context.Context) map[string]any {
	h := a.hardening()

	type siteChoice struct {
		Domain string `json:"domain"`
		Edge   bool   `json:"edge"`
	}
	sites := []siteChoice{}
	if list, err := a.Store.Sites(ctx); err == nil {
		for _, site := range list {
			sites = append(sites, siteChoice{Domain: site.Domain, Edge: site.EdgeTLS()})
		}
	}

	return map[string]any{
		"settings":          h,
		"kernel_applied":    h.Kernel && security.KernelApplied(),
		"php_applied":       h.PHPHarden && security.PHPHardeningApplied(),
		"ping_applied":      h.NoPing && security.PingApplied(),
		"locks":             a.lockProps(ctx),
		"sites":             sites,
		"cloudflare_attack": a.cloudflareAttackOn(),
	}
}

// hardeningChecks are the checklist's lines for the switches.
func (a *App) hardeningChecks() []check {
	h := a.hardening()

	return []check{
		{
			Key: "kernel", Label: "Kernel network hardening", Level: "info", Tab: "hardening",
			OK: h.Kernel,
			Detail: map[bool]string{
				true:  "SYN-flood protection on; ICMP redirects and source routing refused.",
				false: "The kernel accepts ICMP redirects and has the default SYN backlog.",
			}[h.Kernel],
		},
	}
}
