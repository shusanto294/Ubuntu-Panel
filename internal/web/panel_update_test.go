package web

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
)

// The panel that comes up after an update is how it learns how the update
// went: a different version answering is a success, the same one is the old
// panel put back.
func TestReconcilePanelUpdate(t *testing.T) {
	app := bootstrapApp(t, "all")
	ctx := context.Background()

	if state := app.reconcilePanelUpdate(ctx); state != "idle" {
		t.Fatalf("nothing in flight should be idle, got %s", state)
	}

	for _, test := range []struct {
		from, target, want string
	}{
		{"0.0.1", "", "succeeded"},
		{config.Version, "9.9.9", "failed"},
		{"0.0.1", config.Version, "succeeded"},
	} {
		task, _ := app.Store.CreateTask(ctx, "provision", "panel.update", 0, 0, "")
		app.Store.SetSetting(updateFromSetting, test.from)
		app.Store.SetSetting(updateTargetSetting, test.target)
		app.Store.SetSetting(updateStartedSetting, strconv.FormatInt(time.Now().Unix(), 10))
		app.Store.SetSetting(updateTaskSetting, strconv.FormatInt(task, 10))

		if state := app.reconcilePanelUpdate(ctx); state != test.want {
			t.Errorf("from %s to %s: got %s, want %s", test.from, test.target, state, test.want)
		}

		var result updateResult
		json.Unmarshal([]byte(app.Store.Setting(updateResultSetting, "")), &result)

		if result.State != test.want {
			t.Errorf("recorded %q, want %q", result.State, test.want)
		}

		if app.Store.Setting(updateStartedSetting, "") != "" {
			t.Error("the update in flight was not forgotten")
		}

		found, _ := app.Store.FindTask(ctx, task)
		if wantTask := map[string]string{"succeeded": "success", "failed": "failed"}[test.want]; found.Status != wantTask {
			t.Errorf("task is %s, want %s", found.Status, wantTask)
		}
	}
}

// The script the update unit runs has to be valid shell, and must not put sudo
// in front of bash, which would drop the variable telling the installer to
// leave the apps to the new panel.
func TestUpdateScriptIsValidShell(t *testing.T) {
	app := bootstrapApp(t, "all")

	script := app.updateUnitScript()

	if strings.Contains(script, "sudo") || !strings.Contains(script, "PANEL_UPDATE_APPS=0 bash") {
		t.Fatalf("the installer would not be told to leave the apps alone:\n%s", script)
	}

	if out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("not valid shell: %v\n%s", err, out)
	}

	if !strings.HasSuffix(app.updateScriptURL(), "/get.sh") {
		t.Fatalf("unexpected script URL %s", app.updateScriptURL())
	}
}
