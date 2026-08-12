package sites

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// MaxEditableFile is the largest file the editor will open. Past this it is
// not a file somebody is editing in a browser, and holding it in a page
// payload helps nobody.
const MaxEditableFile = 512 * 1024

// FileEntry is one line of a directory listing.
type FileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	Mode      string `json:"mode"`
	Owner     string `json:"owner"`
	Symlink   bool   `json:"symlink"`
}

// resolve turns a path from the browser into an absolute one inside the site,
// or refuses.
//
// This is the whole security of the file manager, so it is done twice and in
// two different ways. First the path is cleaned and checked lexically, which
// stops `../../etc/shadow` before it reaches a command line. Then the machine
// is asked what the path really is — because a symlink inside the site
// directory pointing at / is a path that passes every string test and still
// leaves the site.
//
// The site root itself always resolves, so a site whose directory is reached
// through a symlink is still browsable.
func resolve(site *store.Site, relative string) (string, error) {
	root := strings.TrimRight(site.RootPath, "/")

	// Backslashes are not separators here, but they are in plenty of archives,
	// and a name containing one has no business being treated as a path.
	if strings.ContainsAny(relative, "\x00\\") {
		return "", errors.New("that is not a path inside this site")
	}

	cleaned := path.Clean("/" + strings.TrimSpace(relative))
	absolute := path.Join(root, cleaned)

	if absolute != root && !strings.HasPrefix(absolute, root+"/") {
		return "", errors.New("that is not a path inside this site")
	}

	// What the machine says it is, following every symlink on the way. -m so a
	// file that does not exist yet — a new folder, a file being created —
	// still resolves rather than failing here.
	real := strings.TrimSpace(proc.Run("realpath -m "+proc.Quote(absolute), 15*time.Second).Output)
	if real == "" {
		return "", errors.New("that path could not be read")
	}

	realRoot := strings.TrimSpace(proc.Run("realpath -m "+proc.Quote(root), 15*time.Second).Output)
	if realRoot == "" {
		realRoot = root
	}

	if real != realRoot && !strings.HasPrefix(real, realRoot+"/") {
		return "", errors.New("that path leads outside this site")
	}

	return absolute, nil
}

// Relative is the path a listing shows, back from an absolute one.
func relativeTo(site *store.Site, absolute string) string {
	root := strings.TrimRight(site.RootPath, "/")

	if absolute == root {
		return "/"
	}

	return strings.TrimPrefix(absolute, root)
}

// ListFiles is one directory.
//
// Read with `find -maxdepth 1 -printf`, which gives every field in one call and
// in a form that parses — `ls -l` is written for people and its columns move
// depending on the locale, the year, and whether anything in the directory is
// a device node.
func (s *Service) ListFiles(site *store.Site, relative string) ([]FileEntry, string, error) {
	absolute, err := resolve(site, relative)
	if err != nil {
		return nil, "", err
	}

	if !proc.Ok("sudo test -d " + proc.Quote(absolute)) {
		return nil, "", errors.New("that folder is not there")
	}

	// Tab-separated, because a filename may contain spaces but not a tab or a
	// newline in any name this will meet — and both are refused on the way in.
	command := fmt.Sprintf(
		"sudo find %s -mindepth 1 -maxdepth 1 -printf '%%y\\t%%s\\t%%TY-%%Tm-%%Td %%TH:%%TM\\t%%M\\t%%u:%%g\\t%%f\\n' 2>/dev/null",
		proc.Quote(absolute))

	result := proc.Run(command, time.Minute)

	entries := []FileEntry{}

	for _, line := range strings.Split(result.Output, "\n") {
		fields := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 6)
		if len(fields) < 6 {
			continue
		}

		size, _ := strconv.ParseInt(fields[1], 10, 64)

		entries = append(entries, FileEntry{
			Name:      fields[5],
			Path:      path.Join(relativeTo(site, absolute), fields[5]),
			Directory: fields[0] == "d",
			Symlink:   fields[0] == "l",
			Size:      size,
			Modified:  fields[2],
			Mode:      fields[3],
			Owner:     fields[4],
		})
	}

	// Folders first, then by name — the order a file manager is read in.
	sortEntries(entries)

	return entries, relativeTo(site, absolute), nil
}

func sortEntries(entries []FileEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			left, right := entries[j-1], entries[j]

			if left.Directory != right.Directory {
				if right.Directory {
					entries[j-1], entries[j] = right, left
					continue
				}

				break
			}

			if strings.ToLower(right.Name) < strings.ToLower(left.Name) {
				entries[j-1], entries[j] = right, left
				continue
			}

			break
		}
	}
}

// ReadFile is a file's contents, for the editor.
func (s *Service) ReadFile(site *store.Site, relative string) (string, error) {
	absolute, err := resolve(site, relative)
	if err != nil {
		return "", err
	}

	if !proc.Ok("sudo test -f " + proc.Quote(absolute)) {
		return "", errors.New("that file is not there")
	}

	size := strings.TrimSpace(proc.Run("sudo stat -c %s "+proc.Quote(absolute), 30*time.Second).Output)
	if bytes, _ := strconv.ParseInt(size, 10, 64); bytes > MaxEditableFile {
		return "", fmt.Errorf("that file is %s and the editor stops at %d KB — "+
			"download it instead", humanSize(bytes), MaxEditableFile/1024)
	}

	// Refused rather than rendered: a binary in a textarea is a page of
	// replacement characters, and saving it back would destroy the file.
	if !proc.Ok("sudo grep -Iq . " + proc.Quote(absolute) + " 2>/dev/null") {
		return "", errors.New("that file is not text — download it instead")
	}

	return proc.Run("sudo cat "+proc.Quote(absolute), time.Minute).Output, nil
}

// WriteFile saves the editor's contents back.
func (s *Service) WriteFile(site *store.Site, relative, contents string) error {
	absolute, err := resolve(site, relative)
	if err != nil {
		return err
	}

	if proc.Ok("sudo test -d " + proc.Quote(absolute)) {
		return errors.New("that is a folder")
	}

	if err := proc.PutFile(absolute, contents, true); err != nil {
		return err
	}

	// Back to the user the site runs as. PutFile writes as root, and a file
	// PHP-FPM cannot read is a file that was fine until it was edited.
	proc.Ok("sudo chown www-data:www-data " + proc.Quote(absolute))

	return nil
}

// CreateEntry makes a file or a folder.
func (s *Service) CreateEntry(site *store.Site, relative string, directory bool) error {
	absolute, err := resolve(site, relative)
	if err != nil {
		return err
	}

	if proc.Ok("sudo test -e " + proc.Quote(absolute)) {
		return errors.New("something with that name is already there")
	}

	command := "sudo touch " + proc.Quote(absolute)
	if directory {
		command = "sudo mkdir -p " + proc.Quote(absolute)
	}

	if !proc.Ok(command) {
		return errors.New("that could not be created")
	}

	proc.Ok("sudo chown -R www-data:www-data " + proc.Quote(absolute))

	return nil
}

// RenameEntry moves a file or folder within the site.
func (s *Service) RenameEntry(site *store.Site, from, to string) error {
	source, err := resolve(site, from)
	if err != nil {
		return err
	}

	// Resolved on its own, so a rename cannot be a way out of the site either.
	target, err := resolve(site, to)
	if err != nil {
		return err
	}

	if source == strings.TrimRight(site.RootPath, "/") {
		return errors.New("the site's own directory cannot be renamed from here")
	}

	if proc.Ok("sudo test -e " + proc.Quote(target)) {
		return errors.New("something with that name is already there")
	}

	if !proc.Ok(fmt.Sprintf("sudo mv %s %s", proc.Quote(source), proc.Quote(target))) {
		return errors.New("that could not be renamed")
	}

	return nil
}

// DeleteEntry removes a file or a folder and everything in it.
func (s *Service) DeleteEntry(site *store.Site, relative string) error {
	absolute, err := resolve(site, relative)
	if err != nil {
		return err
	}

	// The one path this refuses. `rm -rf` on the site root from a file manager
	// is the site deleted by a misclick, and deleting a site is a different
	// button with a different confirmation.
	if absolute == strings.TrimRight(site.RootPath, "/") {
		return errors.New("that is the site's own directory — delete the site instead")
	}

	if !proc.Ok("sudo rm -rf " + proc.Quote(absolute)) {
		return errors.New("that could not be deleted")
	}

	return nil
}

// humanSize is a byte count as somebody would say it.
func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
