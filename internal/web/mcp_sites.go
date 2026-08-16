package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// The tabs on a site's own page, as tools: its root directory, its PHP
// settings, its certificate, its cache, its schedule, its SSH accounts, the
// rest of the file manager, and the way into a WordPress dashboard.

func (a *App) mcpSetWebDirectory() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_web_directory",
		Capability: capSitesWrite,
		Title:      "Change a site's root directory",
		Description: "Change the directory inside a site that nginx serves — `public` for a " +
			"Laravel site, `dist` for a built front end, empty for the site's own root. The vhost " +
			"is rewritten and nginx reloaded straight away.",
		Properties: map[string]mcp.Property{
			"site":          {Description: "The site's id, or its domain."},
			"web_directory": {Description: "The directory, relative to the site root. Empty means the root itself."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			directory := args.String("web_directory")

			if err := a.Sites.SetWebDirectory(ctx, site.ID, directory); err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id":       site.ID,
				"domain":        site.Domain,
				"web_directory": directory,
				"applied":       true,
			}, nil
		},
	}
}

func (a *App) mcpSitePHPSettings() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_php_settings",
		Capability: capSitesWrite,
		Title:      "Read or write a site's PHP settings",
		Description: "The php.ini values the panel lets a site override — upload size, memory " +
			"limit, execution time and the rest. Called with no `settings`, it lists what can be " +
			"set, what this site has set, and what PHP would use otherwise. Called with them, it " +
			"writes them and reloads PHP-FPM.\n\n" +
			"Only the directives listed are accepted; anything else is refused rather than " +
			"written into a file the site's pool reads.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"settings": {Type: "object",
				Description: "The directives to set, as {name: value} — for example " +
					"{\"upload_max_filesize\": \"128M\"}. An empty string clears one."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			overrides := site.PHPOverrides()

			asked, _ := args["settings"].(map[string]any)

			if len(asked) == 0 {
				directives := make([]map[string]any, 0, len(sites.PHPDirectives))

				for _, directive := range sites.PHPDirectives {
					directives = append(directives, map[string]any{
						"key":     directive.Key,
						"label":   directive.Label,
						"help":    directive.Help,
						"default": directive.Default,
						"value":   nullable(overrides[directive.Key]),
					})
				}

				return map[string]any{
					"site_id":    site.ID,
					"domain":     site.Domain,
					"directives": directives,
				}, nil
			}

			// Merged onto what the site already has: a call naming one directive
			// changes that one, rather than clearing everything it did not
			// mention.
			settings := map[string]string{}

			for key, value := range overrides {
				settings[key] = value
			}

			unknown := []string{}
			values := mcp.Args(asked)

			for key := range asked {
				if !sites.IsPHPDirective(key) {
					unknown = append(unknown, key)

					continue
				}

				// An empty value is how a directive is cleared, which is the
				// site going back to whatever PHP itself uses.
				if value := values.String(key); value != "" {
					settings[key] = value
				} else {
					delete(settings, key)
				}
			}

			if len(unknown) > 0 {
				return nil, fmt.Errorf("this panel does not set %s through a site's PHP settings — "+
					"call this tool without `settings` for the list it does",
					strings.Join(unknown, ", "))
			}

			if err := a.Sites.SetPHPSettings(ctx, site.ID, settings); err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id":  site.ID,
				"domain":   site.Domain,
				"settings": settings,
				"applied":  true,
			}, nil
		},
	}
}

func (a *App) mcpRequestCertificate() *mcp.Tool {
	return &mcp.Tool{
		Name:       "request_certificate",
		Capability: capSitesWrite,
		Title:      "Request a site's certificate",
		Description: "Ask Let's Encrypt for this site's certificate now, and switch its vhost to " +
			"HTTPS when it arrives. The domain has to resolve to this server first, which is what " +
			"get_site's `resolution` says. Asynchronous — get_task has certbot's own output.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if !a.Store.ServiceInstalled("certbot") {
				return nil, errors.New("Certbot is not installed on this machine — install_apps " +
					"with `certbot` puts it there")
			}

			a.Queue.PushID(queue.SiteCertificate, "site", site.ID)

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"queued":  true,
				"next":    "Poll get_site; its `certificate` says what was issued and when it expires.",
			}, nil
		},
	}
}

func (a *App) mcpSiteCache() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_cache",
		Capability: capSitesWrite,
		Title:      "Read or change a site's cache",
		Description: "A site's page cache: what it is set to now, and how to change it. `action` " +
			"is `status` to read, `set` to change the mode, or `purge` to empty it.\n\n" +
			"`nginx` caches whole pages in front of the site, `redis` gives WordPress an object " +
			"cache, and `off` builds every request again.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"action": {Description: "What to do.", Default: "status",
				Enum: []string{"status", "set", "purge"}},
			"mode": {Description: "The cache to use, for `set`.", Enum: []string{"off", "nginx", "redis"}},
			"ttl":  {Description: "How long a cached page stays fresh, for the nginx cache — like `1h`."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			switch args.StringOr("action", "status") {
			case "status":
				return map[string]any{
					"site_id": site.ID,
					"domain":  site.Domain,
					"cache":   a.cacheSummary(site),
				}, nil

			case "purge":
				if err := a.Sites.PurgeCache(ctx, site.ID); err != nil {
					return nil, err
				}

				return map[string]any{
					"site_id": site.ID,
					"domain":  site.Domain,
					"purged":  true,
				}, nil

			case "set":
				mode := args.String("mode")

				if err := a.Sites.SetCache(ctx, site.ID, mode, args.String("ttl")); err != nil {
					return nil, err
				}

				fresh, err := a.Store.FindSite(ctx, site.ID)
				if err != nil {
					fresh = site
				}

				return map[string]any{
					"site_id": site.ID,
					"domain":  site.Domain,
					"cache":   a.cacheSummary(fresh),
					"applied": true,
				}, nil
			}

			return nil, errors.New("`action` is status, set or purge")
		},
	}
}

func (a *App) mcpSiteCron() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_cron",
		Capability: capSitesWrite,
		Title:      "Read or write a site's schedule",
		Description: "The scheduled commands that run as this site — WordPress's own cron, an " +
			"artisan schedule, a nightly script. Called with no `jobs`, it reads the schedule " +
			"back. Called with them, it writes the whole file.\n\n" +
			"The whole file: what is sent is what the site ends up with, so read it first and " +
			"send the existing lines back alongside the new one, or the ones left out are gone.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"jobs": {Type: "array", Items: "object",
				Description: "The whole schedule, as objects with `schedule` (five cron fields, " +
					"like `0 3 * * *`) and `command`."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			raw, sent := args["jobs"].([]any)

			if !sent {
				return map[string]any{
					"site_id": site.ID,
					"domain":  site.Domain,
					"jobs":    a.Sites.CronJobs(site),
				}, nil
			}

			jobs := make([]sites.CronJob, 0, len(raw))

			for _, entry := range raw {
				fields, ok := entry.(map[string]any)
				if !ok {
					return nil, errors.New("each job is an object with `schedule` and `command`")
				}

				line := mcp.Args(fields)

				jobs = append(jobs, sites.CronJob{
					Schedule: line.String("schedule"),
					Command:  line.String("command"),
				})
			}

			if err := a.Sites.SetCronJobs(ctx, site.ID, jobs); err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"jobs":    a.Sites.CronJobs(site),
				"written": true,
			}, nil
		},
	}
}

func (a *App) mcpSiteAccess() *mcp.Tool {
	return &mcp.Tool{
		Name:       "site_access",
		Capability: capSitesWrite,
		Title:      "Manage a site's SSH and SFTP accounts",
		Description: "The accounts that can sign in to this site's files over SSH or SFTP: list " +
			"them, add one, change a password, or remove one. A created or changed password is " +
			"returned once and not stored, so pass it on.\n\n" +
			"An account here opens a session on the server. `sftp_only` keeps it to files.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"action": {Description: "What to do.", Default: "list",
				Enum: []string{"list", "create", "password", "delete"}},
			"username":  {Description: "The account, for anything but `list`."},
			"password":  {Description: "For `create` and `password`. Generated when left out."},
			"sftp_only": {Type: "boolean", Default: true, Description: "For `create`: files only, no shell."},
			"confirm": {Type: "boolean", Default: false,
				Description: "For `delete`: the person has been told and has said yes."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			action := args.StringOr("action", "list")

			if action == "list" {
				return map[string]any{
					"site_id": site.ID,
					"domain":  site.Domain,
					"users":   a.Sites.AccessUsers(site),
				}, nil
			}

			username := strings.ToLower(args.String("username"))
			if username == "" {
				return nil, errors.New("`username` is which account this is about")
			}

			password := args.String("password")
			generated := false

			if password == "" {
				password = crypto.Password(24)
				generated = true
			}

			answer := map[string]any{
				"site_id":  site.ID,
				"domain":   site.Domain,
				"username": username,
				"action":   action,
			}

			switch action {
			case "create":
				if err := a.Sites.CreateAccessUser(ctx, site.ID, username, password,
					args.Bool("sftp_only", true)); err != nil {
					return nil, err
				}

				answer["password"] = password
				answer["password_generated"] = generated
				answer["sftp_only"] = args.Bool("sftp_only", true)
				answer["host"] = orDefault(system.PublicIP(), site.Domain)
				answer["created"] = true

			case "password":
				if err := a.Sites.SetAccessPassword(ctx, site.ID, username, password); err != nil {
					return nil, err
				}

				answer["password"] = password
				answer["password_generated"] = generated

			case "delete":
				if err := a.requireConfirmation(args,
					"Removing the account "+username+" from "+site.Domain); err != nil {
					return nil, err
				}

				if err := a.Sites.DeleteAccessUser(ctx, site.ID, username); err != nil {
					return nil, err
				}

				answer["deleted"] = true

			default:
				return nil, errors.New("`action` is list, create, password or delete")
			}

			return answer, nil
		},
	}
}

func (a *App) mcpManageFiles() *mcp.Tool {
	return &mcp.Tool{
		Name:        "manage_files",
		Capability:  capFilesWrite,
		Title:       "Create, rename, delete and archive a site's files",
		Destructive: true,
		Description: "The rest of the file manager, inside one site: make a folder or an empty " +
			"file, rename or move something, delete it, zip a selection, or extract an archive. " +
			"read_file and write_file are for a file's contents; list_files reads a directory.\n\n" +
			"Every path is relative to the site's own root, and nothing outside it is reachable.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"action": {Description: "What to do.",
				Enum: []string{"folder", "file", "rename", "delete", "zip", "unzip"}},
			"path": {Description: "What it is done to: the new folder or file, the one being " +
				"renamed or deleted, the archive being extracted, or the folder a zip is made in."},
			"to": {Description: "The new path, for `rename`."},
			"names": {Type: "array",
				Description: "For `zip`: the entries inside `path` to include. All of it when left out."},
			"name": {Description: "For `zip`: the archive's filename."},
			"confirm": {Type: "boolean", Default: false,
				Description: "For `delete`: the person has been told what goes and has said yes."},
		},
		Required: []string{"site", "action", "path"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			path := args.String("path")

			switch args.String("action") {
			case "folder":
				err = a.Sites.CreateEntry(site, path, true)
			case "file":
				err = a.Sites.CreateEntry(site, path, false)
			case "rename":
				if args.String("to") == "" {
					return nil, errors.New("`to` is where it is being renamed to")
				}

				err = a.Sites.RenameEntry(site, path, args.String("to"))
			case "delete":
				if err := a.requireConfirmation(args,
					"Deleting "+path+" in "+site.Domain); err != nil {
					return nil, err
				}

				err = a.Sites.DeleteEntry(site, path)
			case "zip":
				err = a.Sites.Compress(site, path, args.List("names"), args.String("name"))
			case "unzip":
				err = a.Sites.Extract(site, path)
			default:
				return nil, errors.New("`action` is folder, file, rename, delete, zip or unzip")
			}

			if err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"action":  args.String("action"),
				"path":    path,
				"done":    true,
			}, nil
		},
	}
}

// maxBinaryFile is the ceiling on a file moved through a tool call in either
// direction. Base64 costs a third on top, and a tool result is read into an
// assistant's context — a 50 MB archive handed over this way is not a transfer,
// it is an outage.
const maxBinaryFile = 4 << 20 // 4 MiB

func (a *App) mcpUploadFile() *mcp.Tool {
	return &mcp.Tool{
		Name:       "upload_file",
		Capability: capFilesWrite,
		Title:      "Upload a file into a site",
		Description: "Put a file inside a site from base64 content — an image, an archive, a " +
			"font, anything that is not text. write_file is the one to use for text, and it takes " +
			"the contents as they are.\n\n" +
			"Up to 4 MB. Something larger belongs on the machine another way: run_command with " +
			"curl or wget fetches it straight to the server, which is faster and does not pass " +
			"through this conversation.",
		Properties: map[string]mcp.Property{
			"site":    {Description: "The site's id, or its domain."},
			"path":    {Description: "The directory to put it in, relative to the site root.", Default: "/"},
			"name":    {Description: "The filename to write."},
			"content": {Description: "The file's bytes, base64 encoded."},
		},
		Required: []string{"site", "name", "content"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			name := args.String("name")
			if name == "" || strings.ContainsAny(name, "/\\") {
				return nil, errors.New("`name` is a filename, without a path in it — `path` is the folder")
			}

			raw, err := base64.StdEncoding.DecodeString(
				strings.TrimSpace(args.String("content")))
			if err != nil {
				return nil, errors.New("`content` is the file's bytes, base64 encoded")
			}

			if len(raw) > maxBinaryFile {
				return nil, fmt.Errorf("that is %d bytes, and this tool carries up to %d — "+
					"fetch it onto the server with run_command instead", len(raw), maxBinaryFile)
			}

			// Staged and moved, the same as the browser's own upload: the bytes
			// never appear on a command line, and the file lands owned by the
			// site's user.
			staged, err := os.CreateTemp("", "ubuntu-panel-mcp-upload-*")
			if err != nil {
				return nil, errors.New("the upload could not be staged on the server")
			}

			defer os.Remove(staged.Name())

			if _, err := staged.Write(raw); err != nil {
				staged.Close()

				return nil, errors.New("the upload could not be staged on the server")
			}

			staged.Close()

			// Readable by root, which is what moves it into place.
			os.Chmod(staged.Name(), 0o644)

			folder := args.StringOr("path", "/")

			if err := a.Sites.PlaceUpload(site, folder, name, staged.Name()); err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id":  site.ID,
				"domain":   site.Domain,
				"path":     strings.TrimRight(folder, "/") + "/" + name,
				"bytes":    len(raw),
				"uploaded": true,
			}, nil
		},
	}
}

func (a *App) mcpDownloadFile() *mcp.Tool {
	return &mcp.Tool{
		Name:       "download_file",
		Capability: capFilesRead,
		Title:      "Download a file from a site",
		ReadOnly:   true,
		Description: "Read a file out of a site as base64 — an image, a database dump, anything " +
			"that is not text. read_file is the one for text, and it needs no decoding.\n\n" +
			"Up to 4 MB. For a folder, or for something larger, make an archive with manage_files " +
			"and copy it off the machine another way.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"path": {Description: "The file, relative to the site root."},
		},
		Required: []string{"site", "path"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			download, err := a.Sites.DownloadFor(site, args.String("path"))
			if err != nil {
				return nil, err
			}

			if strings.HasSuffix(download.Name, ".tar.gz") &&
				!strings.HasSuffix(args.String("path"), ".tar.gz") {
				return nil, errors.New("that is a directory. Zip it with manage_files first, or " +
					"read the files inside it one at a time")
			}

			var (
				buffer bytes.Buffer
				over   bool
			)

			proc.Stream(download.Command, 10*time.Minute, func(chunk []byte) {
				if buffer.Len() > maxBinaryFile {
					over = true

					return
				}

				buffer.Write(chunk)
			})

			if over || buffer.Len() > maxBinaryFile {
				return nil, fmt.Errorf("that file is larger than the %d bytes this tool carries — "+
					"copy it off the machine another way", maxBinaryFile)
			}

			return map[string]any{
				"site_id":      site.ID,
				"domain":       site.Domain,
				"path":         args.String("path"),
				"name":         download.Name,
				"content_type": download.Type,
				"bytes":        buffer.Len(),
				"content":      base64.StdEncoding.EncodeToString(buffer.Bytes()),
				"encoding":     "base64",
			}, nil
		},
	}
}

func (a *App) mcpWordPressLogin() *mcp.Tool {
	return &mcp.Tool{
		Name:       "wordpress_login_link",
		Capability: capSitesWrite,
		Title:      "Get a WordPress sign-in link",
		Description: "A one-shot link into a WordPress site's dashboard, already signed in as its " +
			"administrator. It is minted per call, spent on arrival and valid for two minutes, so " +
			"hand it straight to the person rather than storing it.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			link, err := a.Sites.WordPressLoginURL(ctx, site.ID)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id":    site.ID,
				"domain":     site.Domain,
				"login_url":  link,
				"expires_in": "2 minutes",
			}, nil
		},
	}
}
