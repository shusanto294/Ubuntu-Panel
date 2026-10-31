package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/s3"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

// Remote backup storage, on the Settings page: one S3-compatible bucket the
// sites that choose it back up to. The secret key is stored encrypted and never
// sent back to the page — the field says whether one is stored, and leaving it
// empty keeps it, as the SMTP password does.

func (a *App) backupStorageProps() map[string]any {
	current := sites.RemoteConfig(a.Store)

	return map[string]any{
		"configured": current.Ready(),
		"endpoint":   current.Endpoint,
		"region":     current.Region,
		"bucket":     current.Bucket,
		"prefix":     current.Prefix,
		"access_key": current.AccessKey,
		"has_secret": current.SecretKey != "",
		"path_style": current.PathStyle,
		"sites":      a.Store.CountSitesBackingUpTo("s3"),
	}
}

// backupStorageInput is the form, with the stored secret standing in for an
// empty field.
func (a *App) backupStorageInput(input map[string]string) s3.Config {
	config := s3.Config{
		Endpoint:  input["endpoint"],
		Region:    input["region"],
		Bucket:    input["bucket"],
		Prefix:    input["prefix"],
		AccessKey: input["access_key"],
		SecretKey: input["secret_key"],
		PathStyle: boolInput(input, "path_style", true),
	}

	if strings.TrimSpace(config.SecretKey) == "" {
		config.SecretKey = a.Store.SecretSetting(sites.S3SecretSetting)
	}

	return config.Normalise()
}

// backupStorageUpdate checks the bucket before saving it: storage that cannot
// be written to is not saved, so a site cannot be pointed at it.
func (a *App) backupStorageUpdate(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	config := a.backupStorageInput(input)

	for field, problem := range config.Validate() {
		sess.Error(field, problem)
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/settings")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s3.New(config).Check(ctx); err != nil {
		a.failBack(w, r, "Not saved — the panel could not write to the bucket: "+err.Error())
		return
	}

	if err := a.Store.SetSecretSetting(sites.S3SecretSetting, config.SecretKey); err != nil {
		a.failBack(w, r, "Could not store the secret key. The panel's application key may be missing.")
		return
	}

	a.Store.SetSetting(sites.S3EndpointSetting, config.Endpoint)
	a.Store.SetSetting(sites.S3RegionSetting, config.Region)
	a.Store.SetSetting(sites.S3BucketSetting, config.Bucket)
	a.Store.SetSetting(sites.S3PrefixSetting, config.Prefix)
	a.Store.SetSetting(sites.S3AccessKeySetting, config.AccessKey)

	pathStyle := "0"
	if config.PathStyle {
		pathStyle = "1"
	}

	a.Store.SetSetting(sites.S3PathStyleSetting, pathStyle)

	a.Log.Info("remote backup storage saved", "endpoint", config.Endpoint, "bucket", config.Bucket)
	a.okBack(w, r, "Remote backup storage saved — the panel wrote to "+config.Bucket+
		" and deleted what it wrote. Choose it on a site's Backups tab.")
}

// backupStorageTest checks what is stored, without saving anything.
func (a *App) backupStorageTest(w http.ResponseWriter, r *http.Request) {
	config := sites.RemoteConfig(a.Store)
	if !config.Ready() {
		a.failBack(w, r, "Remote backup storage is not set up.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s3.New(config).Check(ctx); err != nil {
		a.failBack(w, r, "The panel could not write to "+config.Bucket+": "+err.Error())
		return
	}

	a.okBack(w, r, "Connected — the panel can write to "+config.Bucket+".")
}

// backupStorageDestroy forgets the storage. Sites set to it go back to this
// server, so their next backup does not fail; the backups already in the bucket
// stay there, and stay listed, but cannot be restored until it is added again.
func (a *App) backupStorageDestroy(w http.ResponseWriter, r *http.Request) {
	for _, key := range sites.S3Settings {
		a.Store.ForgetSetting(key)
	}

	moved := a.Store.MoveSiteBackups("s3", "local")

	message := "Remote backup storage removed. Backups already in the bucket are left there."
	if moved > 0 {
		message += " Sites that used it back up to this server from now on."
	}

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", message)
	}

	inertia.Back(w, r, "/settings")
}
