// Package tasks runs a named list of steps in order, flushing progress to the
// task row after every command so the browser can poll and watch it happen.
//
// A step is either a list of shell commands or a function, for the parts that
// need real logic (writing a vhost, calling a DNS provider). Steps can be
// tagged with a group: when a grouped step fails, the rest of that group is
// abandoned and everything else carries on. Installing nine things and
// abandoning eight because the fifth would not configure is not a batch, it is
// a queue that stops at the first pothole.
package tasks

import (
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// Step is one entry in a task.
type Step struct {
	Name string

	// Commands run in order; the first failure fails the step.
	Commands []string

	// Fn is the alternative to Commands, for a step that is not a command.
	// Whatever it writes to the returned string is appended to the console.
	Fn func(runner *Runner) (string, error)

	// Optional makes a failure a skip rather than a stop.
	Optional bool

	// Group ties this step to one unit of work inside a batch.
	Group string
}

// List is a task's steps, built up by the service that owns them.
type List struct {
	steps []Step
}

// Add appends a step whose body is one or more shell commands.
func (l *List) Add(name string, commands ...string) *List {
	l.steps = append(l.steps, Step{Name: name, Commands: commands})

	return l
}

// AddFn appends a step whose body is a function.
func (l *List) AddFn(name string, fn func(runner *Runner) (string, error)) *List {
	l.steps = append(l.steps, Step{Name: name, Fn: fn})

	return l
}

// Optional marks the last step added.
func (l *List) Optional() *List {
	if len(l.steps) > 0 {
		l.steps[len(l.steps)-1].Optional = true
	}

	return l
}

// Group tags the last step added.
func (l *List) Group(group string) *List {
	if len(l.steps) > 0 {
		l.steps[len(l.steps)-1].Group = group
	}

	return l
}

// Len is how many steps the list holds.
func (l *List) Len() int { return len(l.steps) }

// Runner executes a list against one task row.
type Runner struct {
	Store  *store.Store
	TaskID int64

	list    *List
	status  []string
	skipped map[string]bool
	failed  []groupFailure
}

type groupFailure struct {
	group  string
	reason string
}

// New prepares a runner.
func New(st *store.Store, taskID int64) *Runner {
	return &Runner{Store: st, TaskID: taskID, skipped: map[string]bool{}}
}

// Write appends text to the console.
func (r *Runner) Write(text string) {
	r.Store.AppendTaskOutput(r.TaskID, text)
}

// Writef is the formatted form.
func (r *Runner) Writef(format string, args ...any) {
	r.Write(fmt.Sprintf(format, args...))
}

// Exec runs one command inside a step, streaming its output to the console,
// and returns the exit code.
func (r *Runner) Exec(command string, timeout time.Duration) int {
	r.Write("$ " + proc.Redact(command) + "\n")

	result := proc.Stream(command, timeout, func(chunk []byte) {
		r.Store.AppendTaskOutput(r.TaskID, string(chunk))
	})

	r.Write("\n")

	return result.Code
}

// Must is the same, but returns an error describing the failure.
func (r *Runner) Must(command string, timeout time.Duration) error {
	r.Write("$ " + proc.Redact(command) + "\n")

	result := proc.Run(command, timeout)

	if result.Output != "" {
		r.Write(result.Output + "\n")
	}

	if result.OK() {
		return nil
	}

	return failure(command, result.Code, result.Output)
}

// GroupSkipped reports whether an earlier step established that this group
// cannot succeed.
func (r *Runner) GroupSkipped(group string) bool { return group != "" && r.skipped[group] }

// SkipGroup abandons the rest of a group.
func (r *Runner) SkipGroup(group string) {
	if group != "" {
		r.skipped[group] = true
	}
}

// RecordFailure notes why one part of a batch failed.
func (r *Runner) RecordFailure(group, reason string) {
	r.failed = append(r.failed, groupFailure{group: group, reason: reason})
}

// GroupFailed reports whether a group was recorded as failed.
func (r *Runner) GroupFailed(group string) bool {
	for _, failure := range r.failed {
		if failure.group == group {
			return true
		}
	}

	return false
}

// Failures is why each part of the batch failed, as one line.
func (r *Runner) Failures() string {
	if len(r.failed) == 0 {
		return ""
	}

	parts := make([]string, 0, len(r.failed))
	for _, failure := range r.failed {
		parts = append(parts, failure.group+" — "+failure.reason)
	}

	return fmt.Sprintf("%d of the batch failed: %s", len(r.failed), strings.Join(parts, "; "))
}

// Run executes every step. It reports whether everything required succeeded.
func (r *Runner) Run(list *List) bool {
	r.list = list
	r.status = make([]string, len(list.steps))

	for i := range r.status {
		r.status[i] = "pending"
	}

	r.Store.SetTaskStatus(r.TaskID, "running", "")
	r.flush("")

	ok := true

	for i := range list.steps {
		step := &list.steps[i]

		// An earlier step already established this work cannot succeed.
		if r.GroupSkipped(step.Group) {
			r.status[i] = "skipped"
			r.flush("")
			continue
		}

		r.status[i] = "running"
		r.Write("\n\033[1m== " + step.Name + "\033[0m\n")
		r.flush(step.Name)

		err := r.runStep(step)

		if err == nil {
			r.status[i] = "success"
			r.flush("")
			continue
		}

		r.Write("\n!! " + err.Error() + "\n")

		if step.Optional {
			r.status[i] = "skipped"
			r.flush("")
			continue
		}

		r.status[i] = "failed"

		// A step that belongs to one service takes only that service down.
		// Ungrouped steps are the shared ones — waiting for apt, the single
		// install transaction — and nothing after them can work.
		if step.Group != "" {
			r.RecordFailure(step.Group, err.Error())
			r.SkipGroup(step.Group)
			r.flush("")
			continue
		}

		r.Store.FinishTask(r.TaskID, "failed", err.Error(), r.progress())
		ok = false
		break
	}

	switch {
	case ok && len(r.failed) > 0:
		r.Store.FinishTask(r.TaskID, "failed", r.Failures(), r.progress())
		ok = false
	case ok:
		r.Store.FinishTask(r.TaskID, "success",
			fmt.Sprintf("Completed %d steps.", len(list.steps)), 100)
	}

	r.Store.SetTaskSteps(r.TaskID, r.steps(), "", r.progress())

	return ok
}

func (r *Runner) runStep(step *Step) error {
	if step.Fn != nil {
		output, err := step.Fn(r)

		if output != "" {
			r.Write(output + "\n")
		}

		return err
	}

	for _, command := range step.Commands {
		if err := r.Must(command, proc.DefaultTimeout); err != nil {
			return err
		}
	}

	return nil
}

func (r *Runner) steps() []store.TaskStep {
	steps := make([]store.TaskStep, len(r.list.steps))

	for i, step := range r.list.steps {
		steps[i] = store.TaskStep{
			Name:     step.Name,
			Status:   r.status[i],
			Optional: step.Optional,
			Group:    step.Group,
		}
	}

	return steps
}

func (r *Runner) progress() int {
	if len(r.list.steps) == 0 {
		return 100
	}

	done := 0

	for _, status := range r.status {
		switch status {
		case "success", "skipped", "failed":
			done++
		}
	}

	return done * 100 / len(r.list.steps)
}

func (r *Runner) flush(current string) {
	r.Store.SetTaskSteps(r.TaskID, r.steps(), current, r.progress())
}

// failure is what went wrong, in the one line that ends up beside the failed
// row.
//
// The full console is kept on the task, but the summary is all most failures
// are ever read through — and a summary that names only the command ("Command
// exited 1: sudo systemctl restart postfix dovecot") says nothing about the
// cause. The last few lines of output usually do.
func failure(command string, code int, output string) error {
	message := fmt.Sprintf("Command exited %d: %s", code, proc.Redact(command))

	var lines []string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, " \r\t")

		if line != "" {
			lines = append(lines, line)
		}
	}

	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}

	if len(lines) > 0 {
		tail := strings.Join(lines, " | ")

		if len(tail) > 700 {
			tail = tail[:700]
		}

		message += " — " + tail
	}

	return fmt.Errorf("%s", message)
}
