package web

import (
	"context"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// registerJobs is the whole map from a queued job to the service that does the
// work. Every handler is written to be safe to run twice: a job left running
// when the panel stopped goes back on the queue at startup.
func (a *App) registerJobs() {
	type sitePayload struct {
		Site  int64 `json:"site"`
		Files bool  `json:"files"`
	}

	type sitePHPPayload struct {
		Site    int64  `json:"site"`
		Version string `json:"version"`
	}

	type databasePayload struct {
		Database int64 `json:"database"`
	}

	type backupPayload struct {
		Backup int64 `json:"backup"`
	}

	// Which halves of a staging site are being pushed back over the site it
	// came from.
	type stagingPushPayload struct {
		Site        int64 `json:"site"`
		Files       bool  `json:"files"`
		Database    bool  `json:"database"`
		KeepUploads bool  `json:"keep_uploads"`
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

	a.Queue.Handle(queue.SiteCertificate, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Certificate(ctx, payload.Site)
	})

	a.Queue.Handle(queue.SitePHPVersion, func(ctx context.Context, job *store.Job) error {
		var payload sitePHPPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.movePHPVersion(ctx, payload.Site, payload.Version)
	})

	// Both keyed by the backup's own id rather than the site's: the row was
	// written before the job was pushed, so a backup that is queued is already
	// something the Backups tab can show as pending.
	a.Queue.Handle(queue.SiteBackup, func(ctx context.Context, job *store.Job) error {
		var payload backupPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Backup(ctx, payload.Backup)
	})

	a.Queue.Handle(queue.SiteStagingCreate, func(ctx context.Context, job *store.Job) error {
		var payload sitePayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.CreateStaging(ctx, payload.Site)
	})

	a.Queue.Handle(queue.SiteStagingPush, func(ctx context.Context, job *store.Job) error {
		var payload stagingPushPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Push(ctx, payload.Site, sites.StagingOptions{
			Files:       payload.Files,
			Database:    payload.Database,
			KeepUploads: payload.KeepUploads,
		})
	})

	a.Queue.Handle(queue.SiteRestore, func(ctx context.Context, job *store.Job) error {
		var payload backupPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Sites.Restore(ctx, payload.Backup)
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

	a.Queue.Handle(queue.ServicesUpgrade, func(ctx context.Context, job *store.Job) error {
		var payload userPayload

		if err := decode(job, &payload); err != nil {
			return err
		}

		return a.Catalog.UpgradeInstalled(ctx, payload.User)
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
