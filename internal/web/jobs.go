package web

import (
	"context"

	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// registerJobs is the whole map from a queued job to the service that does the
// work. Every handler is written to be safe to run twice: a job left running
// when the panel stopped goes back on the queue at startup.
func (a *App) registerJobs() {
	type sitePayload struct {
		Site  int64 `json:"site"`
		Files bool  `json:"files"`
	}

	type databasePayload struct {
		Database int64 `json:"database"`
	}

	type userPayload struct {
		User int64 `json:"user"`
	}

	type domainPayload struct {
		Domain int64 `json:"domain"`
	}

	type accountPayload struct {
		Account int64 `json:"account"`
		Mail    bool  `json:"mail"`
	}

	type panelDomainPayload struct {
		Domain string `json:"domain"`
		Email  string `json:"email"`
	}

	decode := func(job *store.Job, into any) error { return job.Decode(into) }

	a.Queue.Handle(queue.SiteDeploy, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Deploy(ctx, payload.Site)
	})

	a.Queue.Handle(queue.SiteDelete, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Destroy(ctx, payload.Site, payload.Files)
	})

	a.Queue.Handle(queue.SiteRestart, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Restart(ctx, payload.Site)
	})

	a.Queue.Handle(queue.SitePull, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Pull(ctx, payload.Site)
	})

	a.Queue.Handle(queue.DatabaseCreate, func(ctx context.Context, job *store.Job) error {
		var payload databasePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Databases.CreateTask(ctx, payload.Database)
	})

	a.Queue.Handle(queue.DatabaseDelete, func(ctx context.Context, job *store.Job) error {
		var payload databasePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Databases.DeleteTask(ctx, payload.Database)
	})

	a.Queue.Handle(queue.ServicesInstall, func(ctx context.Context, job *store.Job) error {
		var payload userPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Catalog.InstallQueued(ctx, payload.User)
	})

	a.Queue.Handle(queue.ServicesUninstall, func(ctx context.Context, job *store.Job) error {
		var payload struct {
			Key  string `json:"key"`
			User int64  `json:"user"`
		}

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Catalog.Uninstall(ctx, payload.Key, payload.User)
	})

	a.Queue.Handle(queue.MailConfigure, func(ctx context.Context, job *store.Job) error {
		var payload userPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.Configure(ctx, payload.User)
	})

	a.Queue.Handle(queue.MailTest, func(ctx context.Context, job *store.Job) error {
		var payload userPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.TestDelivery(ctx, payload.User)
	})

	a.Queue.Handle(queue.MailDomainCreate, func(ctx context.Context, job *store.Job) error {
		var payload domainPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.CreateDomain(ctx, payload.Domain)
	})

	a.Queue.Handle(queue.MailDomainDelete, func(ctx context.Context, job *store.Job) error {
		var payload domainPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.DeleteDomain(ctx, payload.Domain)
	})

	a.Queue.Handle(queue.MailAccountCreate, func(ctx context.Context, job *store.Job) error {
		var payload accountPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.CreateAccount(ctx, payload.Account)
	})

	a.Queue.Handle(queue.MailAccountDelete, func(ctx context.Context, job *store.Job) error {
		var payload accountPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Mail.DeleteAccount(ctx, payload.Account, payload.Mail)
	})

	a.Queue.Handle(queue.PanelDomain, func(ctx context.Context, job *store.Job) error {
		var payload panelDomainPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.configurePanelDomain(ctx, payload.Domain, payload.Email)
	})
}
