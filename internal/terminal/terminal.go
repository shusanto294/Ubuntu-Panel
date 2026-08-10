// Package terminal is the browser shell and the live stream that share its
// websocket.
//
// Both are one connection carrying JSON frames. A shell session is a PTY: what
// the browser types goes in, what the process writes comes back, and a resize
// is a message rather than a reconnect. A stream session sends nothing but
// updates for the topics the page asked for, which is what lets the dashboard
// show a reading a second without a request a second.
//
// The socket authenticates with a one-shot ticket rather than the session
// cookie: a cross-scheme upgrade does not reliably carry the cookie, and the
// panel is reached both ways.
package terminal

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// Path is where the socket lives. The browser is handed this rather than an
// address: it resolves against the page's own origin, so it stays correct
// whether the panel is reached by IP or by hostname, over TLS or not — and a
// loopback address here could only ever mean the machine the *browser* is on.
const Path = "/terminal-ws"

// Service serves the socket.
type Service struct {
	Store      *store.Store
	Log        *slog.Logger
	SystemUser string

	upgrader websocket.Upgrader
}

// New wires it up.
func New(st *store.Store, log *slog.Logger, systemUser string) *Service {
	return &Service{
		Store:      st,
		Log:        log,
		SystemUser: systemUser,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,

			// Same-origin only: the page and the socket are served by the same
			// nginx, and a ticket is not a licence for another site to open a
			// shell with it.
			CheckOrigin: sameOrigin,
		},
	}
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")

	// A client that sends no Origin is not a browser, and browsers are the only
	// thing this endpoint is for.
	if origin == "" {
		return false
	}

	for _, prefix := range []string{"http://", "https://"} {
		if origin == prefix+r.Host {
			return true
		}
	}

	return false
}

// frame is what the browser reads. Its shape is the frontend's: a status, a
// chunk of output, or an update for a topic.
type frame struct {
	Type    string `json:"type"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Topic   string `json:"topic,omitempty"`
}

// message is what the browser sends.
type message struct {
	Type   string   `json:"type"`
	Data   string   `json:"data"`
	Cols   int      `json:"cols"`
	Rows   int      `json:"rows"`
	Topics []string `json:"topics"`
}

// conn serialises writes. A PTY reader and a ticker both write to the same
// socket, and gorilla allows exactly one writer at a time.
type conn struct {
	socket *websocket.Conn

	mutex  sync.Mutex
	closed bool
}

func (c *conn) send(f frame) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		return errors.New("the socket is closed")
	}

	c.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))

	return c.socket.WriteJSON(f)
}

func (c *conn) close() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		return
	}

	c.closed = true
	c.socket.Close()
}

// Handle is the websocket endpoint. It is registered as a public route: the
// ticket is the whole of its authentication, and it is spent here.
func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	userID, mode, err := s.Store.RedeemTicket(r.Context(), r.URL.Query().Get("ticket"))
	if err != nil {
		// Refused before the upgrade, so the browser gets a status code rather
		// than a socket that closes on it.
		http.Error(w, "That ticket is not valid.", http.StatusForbidden)

		return
	}

	socket, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own response.
		s.Log.Warn("terminal upgrade failed", "error", err)

		return
	}

	c := &conn{socket: socket}
	defer c.close()

	if mode == "stream" {
		s.stream(c)

		return
	}

	s.shell(c, r, userID)
}

// shell runs a login shell on a PTY and pumps it both ways.
func (s *Service) shell(c *conn, r *http.Request, userID int64) {
	cols := queryInt(r, "cols", 120)
	rows := queryInt(r, "rows", 30)

	command, home := s.loginShell()

	command.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"HOME="+home,
		"LANG=C.UTF-8",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	)
	command.Dir = home

	file, err := pty.StartWithSize(command, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		s.Log.Error("could not start a shell", "error", err)
		c.send(frame{Type: "status", State: "failed",
			Message: "The panel could not start a shell: " + err.Error()})

		return
	}

	defer func() {
		file.Close()

		// The shell is not asked politely: the socket is gone, and a login
		// shell waiting on a terminal nobody is reading would sit there.
		if command.Process != nil {
			command.Process.Kill()
			command.Wait()
		}
	}()

	s.Log.Info("terminal opened", "user", userID, "pid", command.Process.Pid)

	c.send(frame{Type: "status", State: "connected",
		Message: system.Hostname() + " — " + s.SystemUser})

	// The PTY into the socket.
	done := make(chan struct{})

	go func() {
		defer close(done)

		buffer := make([]byte, 8192)

		for {
			n, err := file.Read(buffer)

			if n > 0 {
				if c.send(frame{Type: "output", Data: string(buffer[:n])}) != nil {
					return
				}
			}

			if err != nil {
				return
			}
		}
	}()

	// A shell that exits has to reach the browser now, not the next time the
	// browser happens to type something — so the socket is closed from here,
	// which is also what unblocks the read below.
	go func() {
		<-done

		c.send(frame{Type: "status", State: "closed", Message: "Session ended."})
		c.close()
	}()

	// The socket into the PTY.
	for {
		var incoming message

		if err := c.socket.ReadJSON(&incoming); err != nil {
			return
		}

		switch incoming.Type {
		case "input":
			if _, err := file.WriteString(incoming.Data); err != nil {
				return
			}

		case "resize":
			if incoming.Cols > 0 && incoming.Rows > 0 {
				pty.Setsize(file, &pty.Winsize{
					Cols: uint16(incoming.Cols),
					Rows: uint16(incoming.Rows),
				})
			}
		}
	}
}

// loginShell is the command to run, and the directory to run it in.
//
// The panel runs as its own user, so this is that user's shell — and if the
// panel is running as root, the shell drops to the panel user before exec
// rather than handing out a root prompt in a browser.
func (s *Service) loginShell() (*exec.Cmd, string) {
	shell, home := "/bin/bash", "/tmp"

	account, err := user.Lookup(s.SystemUser)
	if err == nil {
		if account.HomeDir != "" {
			home = account.HomeDir
		}

		if os.Getuid() == 0 {
			command := exec.Command(shell, "-l")
			command.SysProcAttr = credentials(account)

			return command, home
		}
	}

	if current, err := user.Current(); err == nil && current.HomeDir != "" && account == nil {
		home = current.HomeDir
	}

	return exec.Command(shell, "-l"), home
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || value <= 0 || value > 1000 {
		return fallback
	}

	return value
}
