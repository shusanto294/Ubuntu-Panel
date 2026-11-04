package web

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
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
		case log.Key == "fail2ban":
			file.Source = serverlog.SourceFail2ban
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

	// Every file and its rotated copy in one sudo.
	paths := make([]string, 0, 2*len(files))
	for _, file := range files {
		paths = append(paths, proc.Quote(file.Path), proc.Quote(file.Path+".1"))
	}

	type stat struct {
		inode string
		size  int64
	}

	stats := map[string]stat{}
	output := proc.Run("sudo stat -c '%i %s %n' "+strings.Join(paths, " ")+" 2>/dev/null", time.Minute).Output
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		stats[fields[2]] = stat{inode: fields[0], size: size}
	}

	events := []store.ServerEvent{}
	trusted := a.trustedSet(ctx)

	for _, file := range files {
		current, ok := stats[file.Path]
		if !ok {
			continue
		}

		inode, offset, known := a.Store.LogCursor(ctx, file.Path)

		// Rotated since the last read: the rest of the old file first.
		if known && inode != current.inode {
			if old, ok := stats[file.Path+".1"]; ok && old.inode == inode && old.size > offset {
				events = append(events, a.readLogBytes(ctx, file, file.Path+".1", offset, old.size, trusted)...)
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

		read, consumed := a.readLogFile(ctx, file, start, current.size, skipFirst, trusted)
		events = append(events, read...)

		a.Store.SetLogCursor(ctx, file.Path, current.inode, start+consumed)
	}

	if err := a.Store.RecordServerEvents(ctx, events); err != nil {
		a.Log.Warn("could not keep the server's log lines", "error", err)
	}
}

// readLogBytes reads a rotated file's tail, where the cursor is not moved.
func (a *App) readLogBytes(ctx context.Context, file serverlog.File, path string, from, to int64, trusted map[string]bool) []store.ServerEvent {
	copied := file
	copied.Path = path
	events, _ := a.readLogFile(ctx, copied, from, to, false, trusted)

	return events
}

// readLogFile reads [from, to) of a file and returns its events and how many
// bytes of whole lines were read — a line still being written is left for the
// next minute.
func (a *App) readLogFile(ctx context.Context, file serverlog.File, from, to int64, skipFirst bool, trusted map[string]bool) ([]store.ServerEvent, int64) {
	length := min(to-from, collectMaxBytes)

	reader := &serverlog.Reader{File: file, Location: time.Local, Now: time.Now(), PanelUser: a.Config.SystemUser,
		Trusted: trusted}
	sink := &cursorWriter{skip: skipFirst, line: reader.Line}

	command := fmt.Sprintf("sudo tail -c +%d %s | head -c %d", from+1, proc.Quote(file.Path), length)
	if err := proc.Pipe(ctx, command, 2*time.Minute, nil, sink); err != nil {
		a.Log.Debug("could not read a log", "path", file.Path, "error", err)
	}

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
