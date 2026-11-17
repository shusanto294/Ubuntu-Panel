package web

import (
	"context"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// A moved site gets its certificate the moment its DNS points here — which,
// when the old panel could not change the record itself, is whenever somebody
// gets round to it. Until then this asks the public resolver every few minutes
// and does nothing else: no task, no certbot, no Let's Encrypt rate limit spent
// on a name that still answers somewhere else.

const (
	// certificateLook is how often the DNS is asked while it points elsewhere.
	certificateLook = 3 * time.Minute

	// certificateRetry is how long after a failed request it is tried again,
	// once the DNS does point here: Let's Encrypt allows five failures an hour.
	certificateRetry = 30 * time.Minute

	// certificatePatience is how long a move waits for its DNS at all.
	certificatePatience = 7 * 24 * time.Hour
)

type certificateAwait struct {
	Site  int64 `json:"site"`
	Until int64 `json:"until"`
}

// awaitCertificateFor queues the wait for one site.
func (a *App) awaitCertificateFor(siteID int64) {
	a.Queue.Push(queue.SiteCertificateAwait, certificateAwait{
		Site: siteID, Until: time.Now().Add(certificatePatience).Unix(),
	})
}

// awaitCertificate is the job: done once the certificate is on disk, the site
// is gone, or the week is up; otherwise it comes back later.
func (a *App) awaitCertificate(ctx context.Context, job *store.Job) error {
	var payload certificateAwait
	if err := job.Decode(&payload); err != nil {
		return err
	}

	site, err := a.Store.FindSite(ctx, payload.Site)
	if err != nil || site.Status == "deleting" || site.EdgeTLS() || sites.HasCertificate(site) {
		return nil
	}

	if time.Now().Unix() > payload.Until {
		a.Log.Info("gave up waiting for a moved site's DNS", "site", site.Domain)
		return nil
	}

	if !system.ResolveNow(ctx, site.Domain, system.PublicIP()).Matches {
		return &queue.RetryLater{After: certificateLook, Reason: errDNSElsewhere(site.Domain)}
	}

	// It points here: the same request the site page's button makes.
	system.ForgetResolution(site.Domain)

	if err := a.Sites.Certificate(ctx, site.ID); err != nil || !sites.HasCertificate(site) {
		return &queue.RetryLater{After: certificateRetry, Reason: errCertificateNotYet(site.Domain)}
	}

	return nil
}

type certificateWait string

func (e certificateWait) Error() string { return string(e) }

func errDNSElsewhere(domain string) error {
	return certificateWait(domain + " does not point here yet")
}

func errCertificateNotYet(domain string) error {
	return certificateWait("the certificate for " + domain + " was not issued; trying again")
}
