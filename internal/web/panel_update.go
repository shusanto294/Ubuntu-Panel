package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
)

// Updating the panel from the panel.
//
// The update itself is the installer, the same one a shell runs: it backs the
// database up, stages the new binary beside the old, starts it, and puts the
// old one back if the new one does not answer. What lives here is everything
// around it — finding out there is something to install, starting it where it
// survives the panel stopping, and finding out afterwards how it went, which a
// process that was itself replaced halfway through cannot simply remember.
const (
	// autoUpdateSetting is whether the panel installs new releases on its own.
	// Off unless somebody turns it on.
	autoUpdateSetting = "panel_auto_update"

	// The update in flight: the version it started from, the one it is going
	// to, when, and the task row the Logs page shows it as. Cleared once the
	// outcome is known.
	updateFromSetting    = "panel_update_from"
	updateTargetSetting  = "panel_update_target"
	updateStartedSetting = "panel_update_started"
	updateTaskSetting    = "panel_update_task"

	// updateAppsSetting asks the panel to bring the installed apps up to date
	// once the new version is running — the installer is told to skip it.
	updateAppsSetting = "panel_update_apps_pending"

	// updateResultSetting is how the last one went, for the Settings card.
	updateResultSetting = "panel_update_result"

	// autoTriedSetting is the version an automatic update last tried, and
	// when: one that failed is not tried again for a day, rather than every
	// hour, restarting the panel each time.
	autoTriedSetting = "panel_auto_update_tried"

	// updateUnit is the transient systemd unit the installer runs in.
	updateUnit = "ubuntu-panel-update"

	// updateCheckEvery is how stale the answer from GitHub may get before the
	// background check asks again: four times a day.
	updateCheckEvery = 6 * time.Hour
)

// updateResult is how the last update went.
type updateResult struct {
	State   string `json:"state"` // succeeded | failed
	From    string `json:"from"`
	To      string `json:"to"`
	At      int64  `json:"at"`
	Auto    bool   `json:"auto"`
	Log     string `json:"log,omitempty"`
	Message string `json:"message"`
}

// updateRunning is whether the installer's unit is still at work.
func updateRunning() bool {
	state := strings.TrimSpace(proc.Run("systemctl is-active "+updateUnit+" 2>/dev/null", 15*time.Second).Output)

	return state == "active" || state == "activating" || state == "reloading"
}

// startPanelUpdate runs the installer in a transient unit of its own, so that
// stopping this service to replace its binary does not stop the installer.
//
// The installer is told to leave the apps alone (PANEL_UPDATE_APPS=0): with
// them, the panel is down for as long as apt takes. The new panel brings them
// up to date itself once it is running, as an ordinary task.
func (a *App) startPanelUpdate(ctx context.Context, userID int64, auto bool) error {
	if updateRunning() {
		return errors.New("an update is already running")
	}

	// The panel stops for the swap, and its work stops with it: a restore, a
	// deploy or a clone cut off halfway is a site left broken. The clock
	// already waits for these; a click waits too.
	if a.Store.WorkInProgress(ctx) {
		return errors.New("a task is running — a backup, a restore, a deploy or an install. " +
			"Update once it has finished, so it is not cut off halfway; the Logs page shows it")
	}

	target := ""
	if status := a.updateStatus(ctx, false); status != nil {
		if latest, ok := status["latest"].(map[string]any); ok {
			target, _ = latest["version"].(string)
		}
	}
	target = strings.TrimPrefix(target, "v")

	message := "Updating the panel from " + config.Version
	if target != "" {
		message += " to " + target
	}

	taskID, _ := a.Store.CreateTask(ctx, "provision", "panel.update", userID, 0, message)

	a.Store.SetSetting(updateFromSetting, config.Version)
	a.Store.SetSetting(updateTargetSetting, target)
	a.Store.SetSetting(updateStartedSetting, strconv.FormatInt(time.Now().Unix(), 10))
	a.Store.SetSetting(updateTaskSetting, strconv.FormatInt(taskID, 10))
	a.Store.SetSetting(updateAppsSetting, "1")
	a.Store.SetSetting("panel_update_auto", strconv.FormatBool(auto))

	command := fmt.Sprintf(
		"sudo systemctl reset-failed %[1]s >/dev/null 2>&1; "+
			"sudo systemd-run --unit=%[1]s --description='Ubuntu Panel update' --collect "+
			"--setenv=PANEL_UPDATE_APPS=0 /bin/bash -c %[2]s",
		updateUnit, proc.Quote(a.updateUnitScript()))

	if result := proc.Run(command, time.Minute); !result.OK() {
		a.forgetUpdateInFlight()
		a.Store.FinishTask(taskID, "failed", "The update could not be started.", 100)
		a.Store.AppendTaskOutput(taskID, result.Output)

		return fmt.Errorf("could not start the update: %s", strings.TrimSpace(result.Output))
	}

	a.Store.AppendTaskOutput(taskID, "Started the installer in the "+updateUnit+" unit.\n"+
		"Its full log is /var/log/ubuntu-panel/install.log.\n")
	a.Log.Info("panel update started", "from", config.Version, "to", target, "automatic", auto)

	return nil
}

// updateUnitScript is what the update unit runs: get.sh, downloaded to a file
// and then run rather than piped — a download that fails half way hands a pipe
// an empty or truncated script, which bash runs and calls a success. No sudo:
// the unit is root's, and sudo in front of bash would drop the variable that
// leaves the apps to the new panel.
func (a *App) updateUnitScript() string {
	return fmt.Sprintf(`set -u
script="$(mktemp)"
trap 'rm -f "$script"' EXIT
if ! curl -fsSL --retry 3 --retry-delay 5 %s -o "$script" || [ ! -s "$script" ]; then
    echo "Could not download the update script from GitHub." >&2
    exit 1
fi
PANEL_UPDATE_APPS=0 bash "$script"`, proc.Quote(a.updateScriptURL()))
}

func (a *App) forgetUpdateInFlight() {
	for _, key := range []string{updateFromSetting, updateTargetSetting, updateStartedSetting,
		updateTaskSetting, updateAppsSetting, "panel_update_auto"} {
		a.Store.ForgetSetting(key)
	}
}

// reconcilePanelUpdate works out how an update in flight went, once the
// installer has finished: this process may be the new panel it installed, or
// the old one it put back. Running is "running"; the rest is decided by which
// version is answering. Idempotent, and cheap when nothing is in flight.
func (a *App) reconcilePanelUpdate(ctx context.Context) string {
	started, _ := strconv.ParseInt(a.Store.Setting(updateStartedSetting, ""), 10, 64)
	if started == 0 {
		return "idle"
	}

	if updateRunning() {
		return "running"
	}

	from := a.Store.Setting(updateFromSetting, "")
	target := a.Store.Setting(updateTargetSetting, "")
	taskID, _ := strconv.ParseInt(a.Store.Setting(updateTaskSetting, ""), 10, 64)
	auto := a.Store.SettingBool("panel_update_auto", false)
	upgradeApps := a.Store.SettingBool(updateAppsSetting, false)

	result := updateResult{From: from, To: config.Version, At: time.Now().Unix(), Auto: auto}

	// Moved off the version it started from, or onto the one it was going to.
	if (from != "" && config.Version != from) || (target != "" && config.Version == target) {
		result.State = "succeeded"
		result.Message = fmt.Sprintf("Updated from %s to %s.", from, config.Version)

		if upgradeApps {
			if _, err := a.Queue.PushID(queue.ServicesUpgrade, "user", 0); err == nil {
				result.Message += " The server's apps are being brought up to date in the " +
					"background; the Apps page has the console."
			}
		}

		a.Log.Info("panel updated", "from", from, "to", config.Version)
	} else {
		result.State = "failed"
		result.To = target
		result.Log = updateLogTail(started, 40)
		result.Message = "The update did not complete, and " + config.Version + " is still running. " +
			"Nothing was left half-installed: the installer puts the previous version back when " +
			"the new one does not start."

		a.Log.Warn("panel update did not complete", "from", from, "target", target)
	}

	if taskID > 0 {
		status := "success"
		if result.State == "failed" {
			status = "failed"
		}

		if result.Log != "" {
			a.Store.AppendTaskOutput(taskID, "\nThe end of the installer's log:\n"+result.Log+"\n")
		}

		a.Store.FinishTask(taskID, status, result.Message, 100)
	}

	encoded, _ := json.Marshal(result)
	a.Store.SetSetting(updateResultSetting, string(encoded))
	a.forgetUpdateInFlight()

	// What GitHub said was "newer than what is running"; what is running has
	// just changed, so the next look should ask again.
	updateCache.Lock()
	updateCache.value = nil
	updateCache.Unlock()

	return result.State
}

// updateLogTail is the end of what this update printed, which is where the
// reason a failed one failed is. The unit's own journal first, from when this
// update started — install.log is overwritten by every run and says nothing
// about a download that never reached the installer. Both are root's.
func updateLogTail(started int64, lines int) string {
	output := proc.Run(fmt.Sprintf("sudo journalctl -u %s --since @%d -n %d -o cat --no-pager 2>/dev/null",
		updateUnit, started, lines), 15*time.Second).Output

	if strings.TrimSpace(output) == "" {
		output = proc.Run(fmt.Sprintf("sudo tail -n %d /var/log/ubuntu-panel/install.log 2>/dev/null", lines),
			15*time.Second).Output
	}

	return strings.TrimRight(output, "\n")
}

// runPanelUpdates is the clock behind the update badge and automatic updates.
// Hourly, which is often enough to notice a release the same day and rare
// enough that GitHub — asked at most every six hours — never notices it.
func (a *App) runPanelUpdates(ctx context.Context) {
	// Shortly after start, not at it: the first minute belongs to the rest of
	// the start-up, and this one is a network call.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		a.panelUpdateTick(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) panelUpdateTick(ctx context.Context) {
	defer func() {
		if problem := recover(); problem != nil {
			a.Log.Error("the update check panicked", "panic", problem)
		}
	}()

	if a.reconcilePanelUpdate(ctx) == "running" {
		return
	}

	updateCache.Lock()
	stale := updateCache.value == nil || time.Since(updateCache.fetched) >= updateCheckEvery
	updateCache.Unlock()

	status := a.updateStatus(ctx, stale)
	available, _ := status["available"].(bool)

	if !available || !a.Store.SettingBool(autoUpdateSetting, false) {
		return
	}

	latest, _ := status["latest"].(map[string]any)
	target, _ := latest["version"].(string)

	// One failed attempt at a version is enough for a day: the next hour would
	// fail the same way, and restart the panel doing it.
	tried, at, _ := strings.Cut(a.Store.Setting(autoTriedSetting, ""), ":")
	if when, err := strconv.ParseInt(at, 10, 64); err == nil && tried == target &&
		time.Since(time.Unix(when, 0)) < 24*time.Hour {
		return
	}

	if a.Store.WorkInProgress(ctx) {
		a.Log.Info("an update is available; waiting for the work in progress to finish", "version", target)
		return
	}

	a.Store.SetSetting(autoTriedSetting, target+":"+strconv.FormatInt(time.Now().Unix(), 10))

	if err := a.startPanelUpdate(ctx, 0, true); err != nil {
		a.Log.Error("could not start an automatic update", "error", err)
	}
}

// updateProps is what the Settings card reads: the version check, whether
// updates are automatic, an update in flight, and how the last one went.
func (a *App) updateProps(ctx context.Context, fresh bool) map[string]any {
	// A copy: the map from updateStatus may be the cached one, and what is
	// added here is about this moment, not about GitHub.
	status := map[string]any{}
	for key, value := range a.updateStatus(ctx, fresh) {
		status[key] = value
	}

	status["auto"] = a.Store.SettingBool(autoUpdateSetting, false)
	status["running"] = a.reconcilePanelUpdate(ctx) == "running"

	var last *updateResult
	if raw := a.Store.Setting(updateResultSetting, ""); raw != "" {
		var result updateResult
		if json.Unmarshal([]byte(raw), &result) == nil {
			last = &result
		}
	}

	status["last"] = last

	return status
}

// updateBadge is what the menu shows beside Settings: read from memory only,
// because it is on every page and a page must never wait on GitHub.
func (a *App) updateBadge() map[string]any {
	updateCache.Lock()
	defer updateCache.Unlock()

	if updateCache.value == nil {
		return map[string]any{"available": false}
	}

	latest, _ := updateCache.value["latest"].(map[string]any)
	version, _ := latest["version"].(string)

	return map[string]any{
		"available": behind(map[string]any{"version": config.Version}, latest),
		"version":   nullable(version),
	}
}

// ------------------------------------------------------------- endpoints

func (a *App) checkUpdates(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.updateProps(r.Context(), r.URL.Query().Get("fresh") != "0"))
}

// updatePanel starts an update from the Settings card. JSON, because the card
// follows the update itself — the page it was on is about to stop answering.
func (a *App) updatePanel(w http.ResponseWriter, r *http.Request) {
	var userID int64
	if user := userFrom(r.Context()); user != nil {
		userID = user.ID
	}

	if err := a.startPanelUpdate(r.Context(), userID, false); err != nil {
		a.writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"started": true, "from": config.Version})
}

// updateStatusEndpoint is what the card polls while an update runs. The
// version is the point: when it changes, the new panel is the one answering.
func (a *App) updateStatusEndpoint(w http.ResponseWriter, r *http.Request) {
	state := a.reconcilePanelUpdate(r.Context())

	var last any
	if raw := a.Store.Setting(updateResultSetting, ""); raw != "" {
		var result updateResult
		if json.Unmarshal([]byte(raw), &result) == nil {
			last = result
		}
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"state":   state,
		"version": config.Version,
		"last":    last,
	})
}

func (a *App) updateAutomatic(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	enabled := boolInput(input, "enabled", false)
	a.Store.SetSetting(autoUpdateSetting, strconv.FormatBool(enabled))

	a.writeJSON(w, http.StatusOK, map[string]any{"auto": enabled})
}
