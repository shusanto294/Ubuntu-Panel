// Package queue is the panel's background work.
//
// Deployments, installs and mail work take minutes and must not run inside an
// HTTP request: the browser would time out, and a reload would start a second
// one. They are recorded as rows in `jobs` and picked up by workers, so a panel
// restart mid-deployment resumes rather than losing the work.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The kinds of work the queue carries. They are strings in the database, so
// they are constants here rather than spelled out at every call site.
const (
	SiteDeploy  = "site.deploy"
	SiteDelete  = "site.delete"
	SiteRestart = "site.restart"
	SitePull    = "site.pull"

	DatabaseCreate = "database.create"
	DatabaseDelete = "database.delete"

	ServicesInstall   = "services.install"
	ServicesUninstall = "services.uninstall"

	MailDomainCreate  = "mail.domain.create"
	MailDomainDelete  = "mail.domain.delete"
	MailAccountCreate = "mail.account.create"
	MailAccountDelete = "mail.account.delete"
	MailConfigure     = "mail.configure"
	MailDomainDNS     = "mail.domain.dns"

	PanelDomain = "panel.domain"
)

// Handler runs one job. Returning an error records it against the job row; the
// user-facing outcome is the task the handler wrote, not this.
type Handler func(ctx context.Context, job *store.Job) error

// Queue owns the workers.
type Queue struct {
	store *store.Store
	log   *slog.Logger

	workers  int
	handlers map[string]Handler

	stop   context.CancelFunc
	waited sync.WaitGroup
}

// New prepares a queue. Nothing runs until Start.
func New(st *store.Store, log *slog.Logger, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}

	return &Queue{
		store:    st,
		log:      log,
		workers:  workers,
		handlers: map[string]Handler{},
	}
}

// Handle registers the function for a kind of job.
func (q *Queue) Handle(kind string, handler Handler) {
	q.handlers[kind] = handler
}

// Push enqueues work.
func (q *Queue) Push(kind string, payload any) (int64, error) {
	if _, ok := q.handlers[kind]; !ok {
		return 0, fmt.Errorf("no handler for %s", kind)
	}

	id, err := q.store.PushJob(kind, payload)
	if err != nil {
		return 0, err
	}

	q.log.Info("queued", "kind", kind, "job", id)

	return id, nil
}

// PushID is the common single-id payload.
func (q *Queue) PushID(kind, field string, id int64) (int64, error) {
	return q.Push(kind, map[string]any{field: id})
}

// Start brings the workers up.
//
// Anything left running when the panel stopped goes back on the queue first:
// every handler is written to be safe to run twice, which is what makes a
// restart mid-deployment resume rather than strand a half-configured site.
func (q *Queue) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	q.stop = cancel

	if requeued := q.store.RequeueRunningJobs(); requeued > 0 {
		q.log.Warn("requeued jobs that were running when the panel stopped", "count", requeued)
	}

	// A task row that says "running" for a process that no longer exists is
	// worse than a failure: the page waits on it forever.
	q.store.FailRunningTasks()
	q.store.ResetInstallingServices()

	for i := 0; i < q.workers; i++ {
		q.waited.Add(1)

		go func(worker int) {
			defer q.waited.Done()

			q.run(ctx, worker)
		}(i + 1)
	}

	q.log.Info("queue started", "workers", q.workers)
}

// Stop asks the workers to finish and waits for them.
func (q *Queue) Stop() {
	if q.stop == nil {
		return
	}

	q.stop()
	q.waited.Wait()
}

func (q *Queue) run(ctx context.Context, worker int) {
	// Poll rather than notify: the work arrives every few minutes at most, and
	// a second of latency on a deployment that takes two is not worth a
	// wake-up channel that has to survive a restart.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		for {
			job, err := q.store.ClaimJob(ctx)
			if err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					q.log.Error("could not claim a job", "error", err)
				}
				break
			}

			q.execute(ctx, worker, job)

			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (q *Queue) execute(ctx context.Context, worker int, job *store.Job) {
	handler, ok := q.handlers[job.Kind]
	if !ok {
		q.log.Error("no handler for job", "kind", job.Kind, "job", job.ID)
		q.store.FailJob(job.ID, "no handler for "+job.Kind)

		return
	}

	log := q.log.With("worker", worker, "job", job.ID, "kind", job.Kind)
	log.Info("running")

	started := time.Now()

	// A panic in a deployment must not take the panel with it: the work is
	// what failed, and the operator needs the panel to say so.
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Error("panicked", "error", recovered, "stack", string(debug.Stack()))
			q.store.FailJob(job.ID, fmt.Sprint(recovered))
		}
	}()

	if err := handler(ctx, job); err != nil {
		log.Error("failed", "error", err, "took", time.Since(started))
		q.store.FailJob(job.ID, err.Error())

		return
	}

	log.Info("finished", "took", time.Since(started))
	q.store.FinishJob(job.ID)
}
