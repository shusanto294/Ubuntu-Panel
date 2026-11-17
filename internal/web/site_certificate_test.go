package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A moved site whose DNS does not point here yet is looked at again later
// rather than handed to certbot; once the week is up it is let go.
func TestAMovedSiteWaitsForItsDNSBeforeItsCertificate(t *testing.T) {
	ctx := context.Background()
	app := bootstrapApp(t, "none")

	id, err := app.Store.CreateSite(ctx, &store.Site{
		Domain: "not-pointed-here.invalid", Type: "static", RootPath: "/tmp/not-pointed-here.invalid", SSL: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	job := func(until time.Time) *store.Job {
		payload, _ := json.Marshal(certificateAwait{Site: id, Until: until.Unix()})
		return &store.Job{Payload: string(payload)}
	}

	var retry *queue.RetryLater
	if err := app.awaitCertificate(ctx, job(time.Now().Add(time.Hour))); !errors.As(err, &retry) {
		t.Fatalf("a domain that does not point here was not waited for: %v", err)
	}

	if err := app.awaitCertificate(ctx, job(time.Now().Add(-time.Hour))); err != nil {
		t.Fatalf("the wait did not end after its deadline: %v", err)
	}
}
