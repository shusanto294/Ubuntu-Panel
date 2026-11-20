package sites

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// NginxMainConfig is the file Ubuntu's nginx package writes, and the only
// place the settings below can go: worker_rlimit_nofile and the events block
// live outside http {}, and keepalive_timeout is already set inside it there,
// so a second one in conf.d is a "directive is duplicate" nginx refuses.
const NginxMainConfig = "/etc/nginx/nginx.conf"

const (
	// WorkerConnections is how many sockets one nginx worker may hold. Ubuntu
	// ships 768, and a proxied or PHP request holds two (the visitor and the
	// backend), so one burst from a crawler or one slow backend fills it and
	// nginx starts dropping visitors with "worker_connections are not enough".
	// Each connection costs a few hundred bytes of nginx memory; the limit is
	// not a reservation.
	WorkerConnections = 4096

	// WorkerOpenFiles has to cover every connection plus the files being
	// served, so it is well above twice WorkerConnections. The master runs as
	// root and sets it on the workers itself, so no systemd override is needed.
	WorkerOpenFiles = 65535

	// KeepaliveTimeout replaces the stock 65 seconds an idle browser may hold
	// a connection open after its last request. Those idle connections count
	// against WorkerConnections; fifteen seconds still covers a visitor going
	// from one page to the next.
	KeepaliveTimeout = 15
)

var (
	workerConnectionsLine = regexp.MustCompile(`(?m)^(\s*)worker_connections\s+(\d+)\s*;`)
	openFilesLine         = regexp.MustCompile(`(?m)^\s*worker_rlimit_nofile\s`)
	workerProcessesLine   = regexp.MustCompile(`(?m)^\s*worker_processes[^;\n]*;[^\n]*\n`)
	stockKeepaliveLine    = regexp.MustCompile(`(?m)^(\s*)keepalive_timeout\s+65s?\s*;`)
)

// TuneNginxMain raises the connection limits in nginx.conf and reports
// whether anything changed.
//
// Only ever upwards, and only from the stock values: a worker_connections
// somebody set higher, a worker_rlimit_nofile they already chose, or a
// keepalive_timeout other than Ubuntu's 65 is theirs and is left alone.
func TuneNginxMain(conf string) (string, bool) {
	tuned := workerConnectionsLine.ReplaceAllStringFunc(conf, func(line string) string {
		match := workerConnectionsLine.FindStringSubmatch(line)

		if current, err := strconv.Atoi(match[2]); err == nil && current >= WorkerConnections {
			return line
		}

		return fmt.Sprintf("%sworker_connections %d;", match[1], WorkerConnections)
	})

	if !openFilesLine.MatchString(tuned) {
		directive := fmt.Sprintf("worker_rlimit_nofile %d;\n", WorkerOpenFiles)

		if where := workerProcessesLine.FindStringIndex(tuned); where != nil {
			tuned = tuned[:where[1]] + directive + tuned[where[1]:]
		} else {
			tuned = directive + tuned
		}
	}

	tuned = stockKeepaliveLine.ReplaceAllString(tuned, fmt.Sprintf("${1}keepalive_timeout %d;", KeepaliveTimeout))

	return tuned, tuned != conf
}

// EnsureNginxCapacity applies TuneNginxMain to the machine's nginx.conf and
// reports whether it changed. The caller reloads nginx.
//
// The file is the one every site depends on, so it is proved before it is
// kept: if nginx -t refuses the result, the original goes back.
func (s *Service) EnsureNginxCapacity() (bool, error) {
	current, err := proc.ReadFile(NginxMainConfig, true)
	if err != nil {
		return false, err
	}

	tuned, changed := TuneNginxMain(current)
	if !changed {
		return false, nil
	}

	if err := proc.PutFile(NginxMainConfig, tuned, true); err != nil {
		return false, err
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		if err := proc.PutFile(NginxMainConfig, current, true); err != nil {
			return false, fmt.Errorf("nginx refused the raised limits, and putting nginx.conf back failed: %w", err)
		}

		return false, errors.New("nginx refused the raised limits; nginx.conf left as it was: " + check.Output)
	}

	return true, nil
}
