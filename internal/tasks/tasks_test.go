package tasks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func runner(t *testing.T) (*store.Store, *Runner) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	taskID, err := st.CreateTask(context.Background(), "test", "test.run", 0, 0, "a task")
	if err != nil {
		t.Fatal(err)
	}

	return st, New(st, taskID)
}

func steps(t *testing.T, st *store.Store, taskID int64) []store.TaskStep {
	t.Helper()

	task, err := st.FindTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}

	var list []store.TaskStep

	if err := json.Unmarshal([]byte(task.Steps), &list); err != nil {
		t.Fatalf("steps are not JSON: %v (%q)", err, task.Steps)
	}

	return list
}

func TestEverySucceedingStepFinishesTheTask(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.Add("first", "true")
	list.Add("second", "echo hello")

	if !r.Run(list) {
		t.Fatal("Run reported a failure")
	}

	task, _ := st.FindTask(context.Background(), r.TaskID)

	if task.Status != "success" {
		t.Fatalf("status is %q", task.Status)
	}

	if task.Progress != 100 {
		t.Fatalf("progress is %d", task.Progress)
	}

	if !strings.Contains(task.Output, "hello") {
		t.Fatalf("the console did not capture the output:\n%s", task.Output)
	}

	for _, step := range steps(t, st, r.TaskID) {
		if step.Status != "success" {
			t.Errorf("step %q is %q", step.Name, step.Status)
		}
	}
}

// An ungrouped failure is a shared step — waiting for apt, the single install
// transaction — and nothing after it can work.
func TestUngroupedFailureStopsTheTask(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.Add("first", "true")
	list.Add("second", "echo why >&2; exit 2")
	list.Add("third", "true")

	if r.Run(list) {
		t.Fatal("Run reported success for a failing task")
	}

	task, _ := st.FindTask(context.Background(), r.TaskID)

	if task.Status != "failed" {
		t.Fatalf("status is %q", task.Status)
	}

	// The summary is what ends up beside the failed row, and naming only the
	// command says nothing about the cause.
	if !strings.Contains(task.Message, "exited 2") || !strings.Contains(task.Message, "why") {
		t.Fatalf("the failure message does not say what happened: %q", task.Message)
	}

	recorded := steps(t, st, r.TaskID)

	if recorded[1].Status != "failed" || recorded[2].Status != "pending" {
		t.Fatalf("steps after the failure ran: %+v", recorded)
	}
}

// Installing nine things and abandoning eight because the fifth would not
// configure is not a batch, it is a queue that stops at the first pothole.
func TestAGroupedFailureOnlyTakesItsOwnGroupDown(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.Add("redis: install", "exit 1").Group("redis")
	list.Add("redis: configure", "true").Group("redis")
	list.Add("nginx: install", "true").Group("nginx")

	if r.Run(list) {
		t.Fatal("Run reported success while part of the batch failed")
	}

	recorded := steps(t, st, r.TaskID)

	if recorded[0].Status != "failed" {
		t.Errorf("the failing step is %q", recorded[0].Status)
	}

	if recorded[1].Status != "skipped" {
		t.Errorf("the rest of the failed group is %q, want skipped", recorded[1].Status)
	}

	if recorded[2].Status != "success" {
		t.Errorf("the other group is %q, want success", recorded[2].Status)
	}

	task, _ := st.FindTask(context.Background(), r.TaskID)

	if !strings.Contains(task.Message, "redis") {
		t.Fatalf("the summary does not name what failed: %q", task.Message)
	}
}

func TestAnOptionalFailureIsASkipRatherThanAStop(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.Add("optional", "exit 1").Optional()
	list.Add("after", "true")

	if !r.Run(list) {
		t.Fatal("an optional failure failed the task")
	}

	recorded := steps(t, st, r.TaskID)

	if recorded[0].Status != "skipped" || recorded[1].Status != "success" {
		t.Fatalf("got %+v", recorded)
	}
}

func TestFunctionStepsReportTheirOutputAndErrors(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.AddFn("writes", func(runner *Runner) (string, error) {
		return "a sentence for the console", nil
	})

	if !r.Run(list) {
		t.Fatal("Run reported a failure")
	}

	task, _ := st.FindTask(context.Background(), r.TaskID)

	if !strings.Contains(task.Output, "a sentence for the console") {
		t.Fatalf("the console is missing the step's output:\n%s", task.Output)
	}
}

// The console component asks for the tail it does not have yet; an offset past
// the end asks for the status line alone rather than a hundred kilobytes of
// apt.
func TestConsoleOffsetTrimsWhatTheBrowserAlreadyHas(t *testing.T) {
	st, r := runner(t)

	list := &List{}
	list.Add("noisy", "echo abcdefghij")

	r.Run(list)

	task, _ := st.FindTask(context.Background(), r.TaskID)

	full := task.Console(0)["output"].(string)
	tail := task.Console(len(full) - 3)["output"].(string)

	if tail != full[len(full)-3:] {
		t.Fatalf("tail is %q", tail)
	}

	if empty := task.Console(1_000_000_000)["output"].(string); empty != "" {
		t.Fatalf("an offset past the end returned %q", empty)
	}
}
