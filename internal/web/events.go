package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/serverlog"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The collector copies what the machine's programs logged into the panel's
// database once a minute, so the Logs page can list it with what the panel
// did — filtered by time, source, level and site, and paged — without reading
// files on every click.
//
// It reads each file from where it stopped last time. A file whose inode
// changed was rotated: the rest of the old one is read from its .1 first, then
// the new one from the start. A file smaller than where the copy had got to
// was emptied, and is read from the start. A file seen for the first time is
// read from its last couple of megabytes, so the list has the recent past in
// it from the first minute without importing years.

const (
	// collectFirstBytes is how far back a file seen for the first time is read.
	collectFirstBytes = 2 << 20

	// collectMaxBytes is the most read from one file in one minute; a file
	// growing faster than this is caught up over the following minutes.
	collectMaxBytes = 16 << 20

	// collectMaxEvents is the most kept from one file in one minute. A log
	// spewing the same line ten thousand times a minute is one thing
	// happening, not ten thousand.
	collectMaxEvents = 2000
)

var collecting sync.Mutex

// collectFiles are the logs the collector reads: the Server logs list, by the
// source each is filed under. The firewall's log is left out — it is a line
// per refused packet, which on a public address is most of the internet.
func (a *App) collectFiles(ctx context.Context) []serverlog.File {
	files := []serverlog.File{}

	for _, log := range a.serverLogs(ctx) {
		file := serverlog.File{Path: log.Path}

		switch {
		case log.Key == "nginx-error":
			file.Source = serverlog.SourceNginx
		case log.Key == "nginx-access":
			file.Source, file.Access = serverlog.SourceRequests, true
		case strings.HasPrefix(log.Key, "php-fpm-"):
			file.Source = serverlog.SourcePHP
		case log.Key == "mysql" || log.Key == "redis" || log.Key == "mongodb":
			file.Source = serverlog.SourceDatabase
		case log.Key == "auth":
			file.Source = serverlog.SourceAuth
		case log.Key == "kern":
			file.Source = serverlog.SourceKernel
		case log.Key == "syslog":
			file.Source = serverlog.SourceSystem
		case log.Key == "mail":
			file.Source = serverlog.SourceMail
		case strings.HasPrefix(log.Key, "site:"):
			parts := strings.SplitN(log.Key, ":", 3)
			if len(parts) != 3 {
				continue
			}
			file.Site = parts[1]
			file.Source = serverlog.SourceSite
			if parts[2] == "access" {
				file.Source, file.Access = serverlog.SourceRequests, true
			}
		default:
			continue
		}

		files = append(files, file)
	}

	return files
}

// collectServerLogs is the minute's copy. One at a time: a slow minute is not
// overtaken by the next.
func (a *App) collectServerLogs(ctx context.Context) {
	if !collecting.TryLock() {
		return
	}
	defer collecting.Unlock()

	defer func() {
		if problem := recover(); problem != nil {
			a.Log.Error("copying the server's logs panicked", "panic", problem)
		}
	}()

	files := a.collectFiles(ctx)
	if len(files) == 0 {
		return
	}

	// Every file and its rotated copy, stat'd by the panel itself where it
	// can see them, and in one sudo for the rest.
	paths := make([]string, 0, 2*len(files))
	for _, file := range files {
		paths = append(paths, file.Path, file.Path+".1")
	}

	started := time.Now()
	stats := statLogs(paths)

	trusted := a.trustedSet(ctx)

	// What to read, decided first, so every read the panel cannot make itself
	// goes out in one sudo rather than one per file: a sudo is a process
	// start, a PAM session and two lines in auth.log — which this then reads
	// next minute.
	reads := []logRead{}

	// Whether the copy of auth.log is up to date, which makes it the SSH
	// log's copy too (see recordSSHEvents).
	authSeen, authBehind := false, false

	for _, file := range files {
		current, ok := stats[file.Path]
		if !ok {
			continue
		}

		if file.Source == serverlog.SourceAuth {
			authSeen = true
		}

		inode, offset, known := a.Store.LogCursor(ctx, file.Path)

		// Rotated since the last read: the rest of the old file first.
		if known && inode != current.inode {
			if old, ok := stats[file.Path+".1"]; ok && old.inode == inode && old.size > offset {
				rotated := file
				rotated.Path = file.Path + ".1"
				reads = append(reads, logRead{file: rotated, from: offset, to: old.size})
			}
			offset = 0
		}

		start, skipFirst := offset, false
		switch {
		case !known:
			start = max(0, current.size-collectFirstBytes)
			skipFirst = start > 0
		case current.size < offset:
			start = 0 // emptied
		}

		if current.size == start {
			a.Store.SetLogCursor(ctx, file.Path, current.inode, start)
			continue
		}

		reads = append(reads, logRead{file: file, from: start, to: current.size, skipFirst: skipFirst,
			cursor: true, inode: current.inode})
	}

	events := []store.ServerEvent{}
	logins := []store.SSHEvent{}

	take := func(read logRead, contents []byte) {
		found, consumed := a.logEvents(read.file, contents, read.skipFirst, trusted)
		events = append(events, found...)

		if read.cursor {
			a.Store.SetLogCursor(ctx, read.file.Path, read.inode, read.from+consumed)
		}

		if read.file.Source == serverlog.SourceAuth {
			logins = append(logins, sshEventsIn(contents[:consumed], read.skipFirst)...)
			if read.length() < read.to-read.from || int64(len(contents)) < read.length() {
				authBehind = true
			}
		}
	}

	// The panel's own reads first, one file at a time, so a minute of
	// catching up is never every file's megabytes held at once.
	viaSudo := []logRead{}
	for _, read := range reads {
		contents, ok := readLogDirect(read.file.Path, read.from, read.length())
		if !ok {
			viaSudo = append(viaSudo, read)
			continue
		}

		take(read, contents)
	}

	for _, batch := range logBatches(viaSudo) {
		contents := readLogBatch(ctx, batch)

		for i, read := range batch {
			take(read, contents[i])
		}
	}

	if err := a.Store.RecordServerEvents(ctx, events); err != nil {
		a.Log.Warn("could not keep the server's log lines", "error", err)
	}

	a.recordAuthLogins(ctx, logins, authSeen && !authBehind, started)
}

// recordAuthLogins keeps the SSH logins read out of auth.log, and when the
// copy is up to date says so: then the SSH log has been copied up to the
// moment the files were looked at — less two seconds for a line still on its
// way to the file, which is then the next minute's — and the minute's
// journalctl is not needed.
func (a *App) recordAuthLogins(ctx context.Context, logins []store.SSHEvent, current bool, at time.Time) {
	a.Store.RecordSSHEvents(ctx, logins)

	sshFromAuthLog.Store(current)
	if !current {
		return
	}

	copied := at.Unix() - 2
	if last, _ := strconv.ParseInt(a.Store.Setting(sshEventsSetting, ""), 10, 64); copied > last {
		a.Store.SetSetting(sshEventsSetting, strconv.FormatInt(copied, 10))
	}
}

// sshFromAuthLog is whether the last minute's copy of auth.log was complete,
// so it is where the SSH logins come from rather than the journal.
var sshFromAuthLog atomic.Bool

// sshEventsIn are the SSH logins in a stretch of auth.log.
func sshEventsIn(contents []byte, skipFirst bool) []store.SSHEvent {
	if skipFirst {
		if _, rest, ok := bytes.Cut(contents, []byte("\n")); ok {
			contents = rest
		} else {
			contents = nil
		}
	}

	found := security.ParseAuthLog(string(contents), time.Now())
	events := make([]store.SSHEvent, 0, len(found))

	for _, event := range found {
		events = append(events, store.SSHEvent{
			Kind: event.Kind, User: event.User, IP: event.IP, Method: event.Method,
			CreatedAt: event.Time.Unix(),
		})
	}

	return events
}

// logStat is what the collector needs of a file: which file it is, and how
// long.
type logStat struct {
	inode string
	size  int64
}

// statLogs looks at the files as the panel itself — which the unit puts in
// the adm group, the one Ubuntu's logs are readable by — and asks sudo only
// about the ones it may not see, all in one. A file that is not there is left
// out.
func statLogs(paths []string) map[string]logStat {
	stats := map[string]logStat{}
	denied := []string{}

	for _, path := range paths {
		info, err := os.Stat(path)

		switch {
		case err == nil:
			stats[path] = logStat{inode: inodeOf(info), size: info.Size()}
		case errors.Is(err, fs.ErrPermission):
			denied = append(denied, proc.Quote(path))
		}
	}

	if len(denied) == 0 {
		return stats
	}

	output := proc.Run("sudo stat -L -c '%i %s %n' "+strings.Join(denied, " ")+" 2>/dev/null", time.Minute).Output
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		stats[fields[2]] = logStat{inode: fields[0], size: size}
	}

	return stats
}

// inodeOf is a file's inode as stat -c %i prints it, which is how the cursors
// written before the panel read the files itself have it.
func inodeOf(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Ino), 10)
	}

	return ""
}

// readLogDirect reads at most length bytes of a file from offset, as the
// panel itself. It reports false only when the panel may not open the file,
// which is sudo's to read; a file gone since it was looked at reads as empty
// and is tried again next minute from the same place.
func readLogDirect(path string, offset, length int64) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, !errors.Is(err, fs.ErrPermission)
	}
	defer file.Close()

	contents := make([]byte, length)
	n, _ := file.ReadAt(contents, offset)

	return contents[:n], true
}

// logRead is one stretch of one file the minute's copy reads: [from, to), at
// most collectMaxBytes of it. cursor says whether the file's cursor moves
// after it — not for a rotated file's tail, which is read once and left.
type logRead struct {
	file      serverlog.File
	from, to  int64
	skipFirst bool
	cursor    bool
	inode     string
}

func (r logRead) length() int64 { return min(r.to-r.from, collectMaxBytes) }

// logBatchBytes is the most one sudo reads. A minute is normally a few
// kilobytes; this is for the first minute and for catching up, so a machine
// with a lot of logs is not held in memory all at once.
const logBatchBytes = 32 << 20

// logBatches groups the reads so each group is one sudo of at most
// logBatchBytes — a single file larger than that is a group of its own.
func logBatches(reads []logRead) [][]logRead {
	batches := [][]logRead{}
	current, size := []logRead{}, int64(0)

	for _, read := range reads {
		if len(current) > 0 && size+read.length() > logBatchBytes {
			batches = append(batches, current)
			current, size = []logRead{}, 0
		}

		current = append(current, read)
		size += read.length()
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches
}

// readLogBatch reads every stretch in one sudo, each preceded by a marker
// made fresh for this read — a line in a log cannot contain it — and returns
// them in order. A file that could not be read comes back empty.
func readLogBatch(ctx context.Context, batch []logRead) [][]byte {
	marker := "@@panel-log-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
		strconv.Itoa(len(batch)) + "@@"

	var script strings.Builder
	for i, read := range batch {
		fmt.Fprintf(&script, "printf '%%s %d\\n' %s; tail -c +%d %s 2>/dev/null | head -c %d; ",
			i, proc.Quote(marker), read.from+1, proc.Quote(read.file.Path), read.length())
	}

	// A failure is not returned: what came before it is still good, and the
	// rest reads as empty and is tried again next minute from the same cursor.
	var output bytes.Buffer
	proc.Pipe(ctx, "sudo bash -c "+proc.Quote(script.String()), 2*time.Minute, nil, &output)

	return splitLogBatch(output.Bytes(), marker, len(batch))
}

// splitLogBatch takes a batch's output apart at its markers.
func splitLogBatch(output []byte, marker string, count int) [][]byte {
	contents := make([][]byte, count)
	token := []byte(marker + " ")

	parts := bytes.Split(output, token)
	for _, part := range parts[1:] {
		header, body, ok := bytes.Cut(part, []byte("\n"))
		if !ok {
			continue
		}

		index, err := strconv.Atoi(string(header))
		if err != nil || index < 0 || index >= count {
			continue
		}

		contents[index] = body
	}

	return contents
}

// logEvents reads a stretch of a file and returns its events and how many
// bytes of whole lines it held — a line still being written is left for the
// next minute.
func (a *App) logEvents(file serverlog.File, contents []byte, skipFirst bool, trusted map[string]bool) ([]store.ServerEvent, int64) {
	reader := &serverlog.Reader{File: file, Location: time.Local, Now: time.Now(), PanelUser: a.Config.SystemUser,
		Trusted: trusted}
	sink := &cursorWriter{skip: skipFirst, line: reader.Line}
	sink.Write(contents)

	read := reader.Events
	dropped := 0
	if len(read) > collectMaxEvents {
		dropped = len(read) - collectMaxEvents
		read = read[dropped:]
	}

	events := make([]store.ServerEvent, 0, len(read)+1)

	if dropped > 0 {
		events = append(events, store.ServerEvent{
			CreatedAt: read[0].At, Source: file.Source, Site: file.Site, Level: serverlog.LevelWarning,
			Message: fmt.Sprintf("%d more lines from %s in the same minute were not kept — %s logged "+
				"faster than anybody reads; the file itself has them all.", dropped, file.Path, file.Path),
		})
	}

	for _, event := range read {
		events = append(events, store.ServerEvent{
			CreatedAt: event.At, Source: event.Source, Site: event.Site, Level: event.Level,
			IP: event.IP, Message: event.Message,
		})
	}

	return events, sink.consumed
}

// cursorWriter hands over whole lines and counts the bytes they took.
type cursorWriter struct {
	pending  []byte
	consumed int64
	skip     bool
	line     func(string)
}

func (w *cursorWriter) Write(chunk []byte) (int, error) {
	w.pending = append(w.pending, chunk...)

	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}

		line := string(w.pending[:end])
		w.consumed += int64(end + 1)
		w.pending = w.pending[end+1:]

		// Reading from the middle of a file starts mid-line.
		if w.skip {
			w.skip = false
			continue
		}

		w.line(line)
	}

	// A line no program writes is junk; skipped, so it cannot stall the copy.
	if len(w.pending) > 64<<10 {
		w.consumed += int64(len(w.pending))
		w.pending = w.pending[:0]
	}

	return len(chunk), nil
}

// ------------------------------------------------------------------ props

// eventRanges are the periods the combined list can be read over.
var eventRanges = []struct {
	Key   string        `json:"key"`
	Label string        `json:"label"`
	Span  time.Duration `json:"-"`
}{
	{"1h", "Last hour", time.Hour},
	{"24h", "Last 24 hours", 24 * time.Hour},
	{"7d", "Last 7 days", 7 * 24 * time.Hour},
	{"30d", "Last 30 days", 30 * 24 * time.Hour},
}

const eventsPerPage = 20

// logsEventsProps are a list's: the server's own entries (scope "server") or
// the panel's (scope "panel").
func (a *App) logsEventsProps(ctx context.Context, query map[string]string, scope string) map[string]any {
	rangeKey := query["range"]
	since := int64(0)
	found := false

	for _, r := range eventRanges {
		if r.Key == rangeKey {
			found = true
			if r.Span > 0 {
				since = time.Now().Add(-r.Span).Unix()
			}
		}
	}
	if !found {
		rangeKey = "1h"
		since = time.Now().Add(-time.Hour).Unix()
	}

	search := strings.TrimSpace(query["q"])
	if len(search) > 200 {
		search = search[:200]
	}

	level := query["level"]
	if level != "warning" && level != "error" {
		level = ""
	}

	filter := store.EventFilter{
		Scope: scope,
		Since: since, Source: query["source"], Level: level, Site: query["site"], Search: search,
	}

	page, _ := strconv.Atoi(query["page"])
	if page < 1 {
		page = 1
	}

	entries, total, err := a.Store.Events(ctx, filter, eventsPerPage, (page-1)*eventsPerPage)
	if err != nil {
		a.Log.Error("could not read the logs", "error", err)
	}

	pages := max(1, (total+eventsPerPage-1)/eventsPerPage)
	if page > pages {
		page = pages
		entries, _, _ = a.Store.Events(ctx, filter, eventsPerPage, (page-1)*eventsPerPage)
	}

	sites := a.Store.EventSites(ctx)
	if list, err := a.Store.Sites(ctx); err == nil {
		known := map[string]bool{}
		for _, site := range sites {
			known[site] = true
		}
		for _, site := range list {
			if !known[site.Domain] {
				sites = append(sites, site.Domain)
			}
		}
	}

	sources := []map[string]string{}
	if scope == "panel" {
		sources = append(sources,
			map[string]string{"key": store.EventsPanel, "label": "Panel activity"},
			map[string]string{"key": store.EventsLogins, "label": "Panel sign-ins"},
			map[string]string{"key": store.EventsSSH, "label": "SSH"},
		)
	} else {
		for _, key := range []string{serverlog.SourceRequests, serverlog.SourceSite, serverlog.SourceNginx,
			serverlog.SourcePHP, serverlog.SourceDatabase, serverlog.SourceFail2ban, serverlog.SourceAuth,
			serverlog.SourceKernel, serverlog.SourceSystem, serverlog.SourceMail} {
			sources = append(sources, map[string]string{"key": key, "label": serverlog.SourceLabels[key]})
		}
	}

	if entries == nil {
		entries = []store.EventEntry{}
	}

	return map[string]any{
		"events":  entries,
		"filters": map[string]any{"range": rangeKey, "source": filter.Source, "level": level, "site": filter.Site, "q": search},
		"pagination": map[string]any{
			"page": page, "pages": pages, "total": total, "per_page": eventsPerPage,
		},
		"counts":  a.Store.EventCounts(ctx, since),
		"ranges":  eventRanges,
		"sources": sources,
		"sites":   sites,
	}
}
