// Package assets carries the built frontend.
//
// The whole of public/ is compiled into the binary, so a release is one file
// with nothing beside it to get out of step — no public directory to install,
// no half-copied build to serve a blank page. Set PANEL_DEBUG to serve the
// same tree from disk instead, so a `npm run build` shows up on reload without
// rebuilding the binary.
package assets

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:public
var embedded embed.FS

// FS returns the filesystem holding the built frontend. publicPath is used
// when fromDisk is set; otherwise the embedded copy is returned.
func FS(publicPath string, fromDisk bool) fs.FS {
	if fromDisk {
		return os.DirFS(publicPath)
	}

	// The embed root is "public/"; the panel wants to serve its contents.
	sub, err := fs.Sub(embedded, "public")
	if err != nil {
		// Only reachable if the embed directive above stopped matching, which
		// is a build-time mistake, not a runtime condition.
		panic(err)
	}

	return sub
}

// DiskPath is where the frontend would be read from on disk, for logging.
func DiskPath(publicPath string) string {
	absolute, err := filepath.Abs(publicPath)
	if err != nil {
		return publicPath
	}

	return absolute
}
