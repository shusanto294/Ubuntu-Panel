package sites

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/s3"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Remote backups: the same directory a local backup is, built here the same
// way, then uploaded to S3-compatible storage and removed from this server.
// A restore downloads it back into a directory of its own first and restores
// from that, so everything after the download is the local restore unchanged.
//
// One storage account for the whole panel, set up in Settings like SMTP. Each
// site chooses whether its backups go there or stay here.

// The settings remote storage is kept in. The secret key is encrypted.
const (
	S3EndpointSetting  = "backup_s3_endpoint"
	S3RegionSetting    = "backup_s3_region"
	S3BucketSetting    = "backup_s3_bucket"
	S3PrefixSetting    = "backup_s3_prefix"
	S3AccessKeySetting = "backup_s3_access_key"
	S3SecretSetting    = "backup_s3_secret"
	S3PathStyleSetting = "backup_s3_path_style"
)

// S3Settings are every setting above, for forgetting them all at once.
var S3Settings = []string{S3EndpointSetting, S3RegionSetting, S3BucketSetting, S3PrefixSetting,
	S3AccessKeySetting, S3SecretSetting, S3PathStyleSetting}

// RemoteConfig is the storage set up in Settings, which may be empty.
func RemoteConfig(st *store.Store) s3.Config {
	return s3.Config{
		Endpoint:  st.Setting(S3EndpointSetting, ""),
		Region:    st.Setting(S3RegionSetting, "us-east-1"),
		Bucket:    st.Setting(S3BucketSetting, ""),
		Prefix:    st.Setting(S3PrefixSetting, ""),
		AccessKey: st.Setting(S3AccessKeySetting, ""),
		SecretKey: st.SecretSetting(S3SecretSetting),
		PathStyle: st.SettingBool(S3PathStyleSetting, true),
	}
}

func (s *Service) remote() (*s3.Client, error) {
	config := RemoteConfig(s.Store)
	if !config.Ready() {
		return nil, errors.New("remote backup storage is not set up — add it in Settings")
	}

	return s3.New(config), nil
}

// backupParts are the files a backup directory can hold, in the order they are
// uploaded: the manifest last, so a folder with a manifest in it is complete.
var backupParts = []string{"files.tar.gz", "database.sql.gz", "database.archive.gz", "manifest.json"}

// remoteKey is the folder in the bucket a backup is uploaded to.
func remoteKey(client *s3.Client, site *store.Site, directory string) string {
	return client.Config().Key(site.Domain, path.Base(directory))
}

// uploadBackup sends a finished backup directory to the bucket.
func uploadBackup(runner *tasks.Runner, client *s3.Client, directory, key string) (string, error) {
	sent := 0
	total := int64(0)

	for _, part := range backupParts {
		file := directory + "/" + part

		if !proc.Ok("sudo test -f " + proc.Quote(file)) {
			continue
		}

		runner.Writef("Uploading %s to %s/%s\n", part, client.Config().Bucket, key+"/"+part)

		reader, writer := io.Pipe()
		read := make(chan error, 1)

		go func() {
			err := proc.Pipe(context.Background(), "sudo cat "+proc.Quote(file), 6*time.Hour, nil, writer)
			writer.CloseWithError(err)
			read <- err
		}()

		lastReport := time.Now()

		size, err := client.Upload(context.Background(), key+"/"+part, reader, func(done int64) {
			if time.Since(lastReport) > 15*time.Second {
				runner.Writef("  %s sent\n", humanBytes(done))
				lastReport = time.Now()
			}
		})

		// Stop the reader if the upload gave up first.
		reader.CloseWithError(errors.New("upload stopped"))

		if readErr := <-read; err == nil && readErr != nil {
			err = fmt.Errorf("could not read %s: %w", part, readErr)
		}

		if err != nil {
			return "", fmt.Errorf("uploading %s failed: %w", part, err)
		}

		runner.Writef("  %s uploaded\n", humanBytes(size))

		sent++
		total += size
	}

	if sent == 0 {
		return "", errors.New("the backup directory was empty, so nothing was uploaded")
	}

	return fmt.Sprintf("%d file(s), %s, uploaded to %s.", sent, humanBytes(total), client.Config().Bucket), nil
}

// downloadBackup fetches a remote backup into directory.
func downloadBackup(runner *tasks.Runner, client *s3.Client, key, directory string) (string, error) {
	objects, err := client.List(context.Background(), key+"/")
	if err != nil {
		return "", fmt.Errorf("could not list the backup in the bucket: %w", err)
	}

	if len(objects) == 0 {
		return "", errors.New("this backup is no longer in the bucket")
	}

	if err := runner.Must(fmt.Sprintf("sudo rm -rf %[1]s && sudo mkdir -p %[1]s && sudo chmod 700 %[1]s",
		proc.Quote(directory)), time.Minute); err != nil {
		return "", err
	}

	for _, object := range objects {
		part := path.Base(object.Key)

		if !knownPart(part) {
			continue
		}

		runner.Writef("Downloading %s (%s)\n", part, humanBytes(object.Size))

		reader, writer := io.Pipe()
		fetched := make(chan error, 1)

		go func() {
			_, err := client.Download(context.Background(), object.Key, writer)
			writer.CloseWithError(err)
			fetched <- err
		}()

		err := proc.Pipe(context.Background(),
			"sudo dd of="+proc.Quote(directory+"/"+part)+" bs=1M status=none", 6*time.Hour, reader, nil)

		reader.CloseWithError(errors.New("write stopped"))

		// dd sees a failed download as the end of its input and exits happily,
		// so the download's own error is the one that decides.
		if downloadErr := <-fetched; downloadErr != nil {
			return "", fmt.Errorf("downloading %s failed: %w", part, downloadErr)
		}

		if err != nil {
			return "", fmt.Errorf("could not write %s: %w", part, err)
		}
	}

	return "Downloaded from " + client.Config().Bucket + ".", nil
}

func knownPart(name string) bool {
	for _, part := range backupParts {
		if part == name {
			return true
		}
	}

	return false
}

// removeRemote deletes a remote backup's folder from the bucket.
func (s *Service) removeRemote(key string) error {
	if strings.Trim(key, "/") == "" {
		return nil
	}

	client, err := s.remote()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	return client.DeletePrefix(ctx, key)
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(n)
	unit := 0

	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}

	if unit == 0 {
		return fmt.Sprintf("%d B", n)
	}

	return fmt.Sprintf("%.1f %s", value, units[unit])
}
