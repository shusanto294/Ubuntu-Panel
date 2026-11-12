package system

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SiteRoot is one site's directory, so a process working inside it can be
// put down to the site rather than to the program's name.
type SiteRoot struct {
	Domain string
	Root   string
}

// siteShare is part of one process's time that belongs to a site.
type siteShare struct {
	domain string
	weight float64 // 0–1; the shares of one process add up to 1
}

// siteIndex finds the site a directory is inside. The longest root wins, so a
// site kept inside another one's directory is still its own.
type siteIndex []SiteRoot

func newSiteIndex(roots []SiteRoot) siteIndex {
	index := siteIndex{}

	for _, root := range roots {
		path := strings.TrimRight(filepath.Clean(root.Root), "/")
		if root.Domain == "" || path == "" || path == "/" {
			continue
		}

		index = append(index, SiteRoot{Domain: root.Domain, Root: path})
	}

	sort.Slice(index, func(i, j int) bool { return len(index[i].Root) > len(index[j].Root) })

	return index
}

func (index siteIndex) site(directory string) string {
	for _, root := range index {
		if directory == root.Root || strings.HasPrefix(directory, root.Root+"/") {
			return root.Domain
		}
	}

	return ""
}

// ---------------------------------------------------------------- the helper

// cwdLookFast is how often a PHP worker's directory is looked at between the
// whole-table reads. A worker sits in the directory of the script it is
// running and goes back to / when the request is over, so a request shorter
// than this can be missed; ten looks a second catch most of them.
const cwdLookFast = 100 * time.Millisecond

// WatchCWDs is the panel's helper mode, run as root: a process's working
// directory is only readable by its owner, and PHP-FPM's workers belong to
// www-data. Once a second it writes, for every process working anywhere but
// /, how many times in that second it was seen in each directory:
//
//	pid<TAB>times<TAB>directory
//
// and then an empty line. It returns when nobody reads it any more.
func WatchCWDs(out io.Writer) error {
	writer := bufio.NewWriter(out)
	fast := map[int]bool{}

	// Whether each process is PHP, read once per process rather than every
	// second: its name does not change, and two hundred reads a second of
	// files whose answer is already known is the helper costing more than
	// what it measures.
	php := map[int]bool{}

	for {
		seen := map[int]map[string]int{}

		look := func(pid int) bool {
			directory, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
			if err != nil {
				return false
			}

			if directory != "/" {
				if seen[pid] == nil {
					seen[pid] = map[string]int{}
				}
				seen[pid][directory]++
			}

			return true
		}

		// Every process once a second, which is also when a new PHP worker
		// is found.
		alive := map[int]bool{}

		entries, _ := os.ReadDir("/proc")
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}

			if !look(pid) {
				continue
			}

			alive[pid] = true

			isPHP, known := php[pid]
			if !known {
				comm, err := os.ReadFile("/proc/" + entry.Name() + "/comm")
				isPHP = err == nil && strings.HasPrefix(string(comm), "php")
				php[pid] = isPHP
			}

			if isPHP {
				fast[pid] = true
			}
		}

		for pid := range php {
			if !alive[pid] {
				delete(php, pid)
			}
		}

		// PHP more often than that: its requests are shorter than a second.
		for i := 1; i < int(time.Second/cwdLookFast); i++ {
			time.Sleep(cwdLookFast)

			for pid := range fast {
				if !look(pid) {
					delete(fast, pid)
				}
			}
		}

		for pid, directories := range seen {
			for directory, times := range directories {
				writer.WriteString(strconv.Itoa(pid) + "\t" + strconv.Itoa(times) + "\t" + directory + "\n")
			}
		}

		writer.WriteString("\n")

		if err := writer.Flush(); err != nil {
			return err
		}
	}
}

// cwdFeed reads the helper: where each process was working in the last
// second it reported.
type cwdFeed struct {
	mu      sync.Mutex
	latest  map[int]map[string]int
	running bool
	tried   time.Time
}

// start runs the helper if it is not running, at most once a minute — a
// machine where sudo is refused would otherwise be asked every second.
func (f *cwdFeed) start() {
	if runtime.GOOS != "linux" {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.running || time.Since(f.tried) < time.Minute {
		return
	}

	f.tried = time.Now()

	executable, err := os.Executable()
	if err != nil {
		return
	}

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(context.Background(), executable, "-watch-cwds")
	} else {
		cmd = exec.CommandContext(context.Background(), "sudo", "-n", executable, "-watch-cwds")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}

	if cmd.Start() != nil {
		return
	}

	f.running = true

	go func() {
		f.read(stdout)
		cmd.Wait()

		f.mu.Lock()
		f.running, f.latest = false, nil
		f.mu.Unlock()
	}()
}

func (f *cwdFeed) read(from io.Reader) {
	scanner := bufio.NewScanner(from)
	second := map[int]map[string]int{}

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			f.mu.Lock()
			f.latest = second
			f.mu.Unlock()

			second = map[int]map[string]int{}

			continue
		}

		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}

		pid, err1 := strconv.Atoi(parts[0])
		times, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}

		if second[pid] == nil {
			second[pid] = map[string]int{}
		}
		second[pid][parts[2]] += times
	}
}

func (f *cwdFeed) snapshot() map[int]map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.latest
}

// siteShares is which sites a process was working for, from where it was
// seen working: a worker seen three times in one site's directory and once in
// another's was three quarters the first's.
func siteShares(directories map[string]int, index siteIndex) []siteShare {
	counts := map[string]int{}
	total := 0

	for directory, times := range directories {
		if domain := index.site(directory); domain != "" {
			counts[domain] += times
			total += times
		}
	}

	if total == 0 {
		return nil
	}

	shares := make([]siteShare, 0, len(counts))
	for domain, times := range counts {
		shares = append(shares, siteShare{domain: domain, weight: float64(times) / float64(total)})
	}

	sort.Slice(shares, func(i, j int) bool { return shares[i].weight > shares[j].weight })

	return shares
}
