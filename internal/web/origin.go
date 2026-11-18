package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

// Who the websites answer, on the Hardening tab: Cloudflare only, and nothing
// at all for this server's bare IP address. Both are nginx configuration the
// switch writes once; neither watches traffic or runs in the background. SSH,
// mail and the panel's own port are not affected by either.

const cloudflareOnlySetting = "security_cloudflare_only"

// securityOrigin switches one of the two on or off.
func (a *App) securityOrigin(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if !a.Store.ServiceInstalled("nginx") {
		a.securityBack(w, r, "nginx is not installed, so there are no websites to protect.", true)
		return
	}

	enabled := boolInput(input, "enabled", false)
	feature := input["feature"]

	var label string

	switch feature {
	case "cloudflare_only":
		label = "Cloudflare only"

		if err := security.ApplyCloudflareOnly(enabled); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}

		a.Store.SetSetting(cloudflareOnlySetting, boolSetting(enabled))
	case "refuse_address":
		label = "Refusing requests to the IP address"

		if err := a.applyRefuseAddress(enabled); err != nil {
			a.securityBack(w, r, err.Error(), true)
			return
		}
	default:
		a.securityBack(w, r, "That is not one of the switches.", true)
		return
	}

	a.Log.Info("origin protection changed", "feature", feature, "enabled", enabled, "ip", session.ClientIP(r))
	a.securityBack(w, r, label+map[bool]string{true: " is on.", false: " is off."}[enabled], false)
}

// applyRefuseAddress rewrites the catch-all and the panel's own vhost — the
// two default servers — to answer a name nobody configured with nothing, or
// with the usual 404 again. Put back as it was if nginx will not have it.
func (a *App) applyRefuseAddress(on bool) error {
	previous := a.Store.SettingBool(sites.RefuseAddressSetting, false)
	a.Store.SetSetting(sites.RefuseAddressSetting, boolSetting(on))

	rewrite := func() error {
		if err := a.Sites.WriteSharedConfig(nil); err != nil {
			return err
		}

		// The panel's port refuses unknown names only once a panel hostname
		// is set; until then that port on the bare IP is the way in.
		return a.refreshPanelVhost(nil)
	}

	err := rewrite()
	if err == nil {
		if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
			err = fmt.Errorf("nginx refused the configuration, so nothing was changed: %s", strings.TrimSpace(check.Output))
		}
	}

	if err != nil {
		a.Store.SetSetting(sites.RefuseAddressSetting, boolSetting(previous))
		rewrite()
		proc.Ok("sudo systemctl reload nginx")

		return err
	}

	if !proc.Ok("sudo systemctl reload nginx") {
		return errors.New("nginx did not reload")
	}

	return nil
}

// ensureCloudflareOnly puts the Cloudflare-only files back at start when the
// switch is on and they are missing, or when this build knows different
// Cloudflare ranges than the ones on disk.
func (a *App) ensureCloudflareOnly() {
	if !a.Store.SettingBool(cloudflareOnlySetting, false) || security.CloudflareOnlyApplied() {
		return
	}

	if err := security.ApplyCloudflareOnly(true); err != nil {
		a.Log.Error("could not put the Cloudflare-only configuration back", "error", err)
	}
}

// originProps are what the two switches show.
func (a *App) originProps(ctx context.Context) map[string]any {
	cloudflareOnly := a.Store.SettingBool(cloudflareOnlySetting, false)

	sitesNotEdge := []string{}
	if list, err := a.Store.Sites(ctx); err == nil {
		for _, site := range list {
			if !site.EdgeTLS() {
				sitesNotEdge = append(sitesNotEdge, site.Domain)
			}
		}
	}

	return map[string]any{
		"nginx":                   a.Store.ServiceInstalled("nginx"),
		"cloudflare_only":         cloudflareOnly,
		"cloudflare_only_applied": cloudflareOnly && security.CloudflareOnlyApplied(),
		"refuse_address":          a.Store.SettingBool(sites.RefuseAddressSetting, false),
		"panel_domain":            a.Store.Setting("panel_domain", ""),
		"panel_port":              a.panelPort(),
		"sites_not_edge":          sitesNotEdge,
	}
}
