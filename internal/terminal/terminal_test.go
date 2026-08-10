package terminal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func service(t *testing.T) *Service {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { st.Close() })

	return New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
}

// dial opens a socket the way the browser does: a ticket in the query string
// and an Origin header naming the page's own host.
func dial(t *testing.T, server *httptest.Server, query url.Values) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + Path + "?" + query.Encode()

	return websocket.DefaultDialer.Dial(endpoint, http.Header{
		"Origin": {server.URL},
	})
}

func read(t *testing.T, socket *websocket.Conn) frame {
	t.Helper()

	socket.SetReadDeadline(time.Now().Add(10 * time.Second))

	_, raw, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("reading a frame: %v", err)
	}

	var f struct {
		Type    string          `json:"type"`
		State   string          `json:"state"`
		Message string          `json:"message"`
		Topic   string          `json:"topic"`
		Data    json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("a frame was not JSON: %v (%s)", err, raw)
	}

	return frame{Type: f.Type, State: f.State, Message: f.Message, Topic: f.Topic, Data: f.Data}
}

// A shell in a browser is the most dangerous thing the panel offers, so the
// ticket is spent on redemption: a token that reaches a second socket is a
// session somebody else can open.
func TestATicketWorksOnceAndThenNeverAgain(t *testing.T) {
	s := service(t)

	server := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer server.Close()

	ticket, err := s.Store.IssueTicket(context.Background(), 1, "stream")
	if err != nil {
		t.Fatal(err)
	}

	socket, _, err := dial(t, server, url.Values{"ticket": {ticket}})
	if err != nil {
		t.Fatalf("the first dial was refused: %v", err)
	}

	if first := read(t, socket); first.Type != "status" || first.State != "connected" {
		t.Fatalf("the socket did not say it was connected: %+v", first)
	}

	socket.Close()

	_, response, err := dial(t, server, url.Values{"ticket": {ticket}})
	if err == nil {
		t.Fatal("the same ticket opened a second socket")
	}

	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("a spent ticket was refused with %v, want 403", response)
	}
}

func TestAnUnknownTicketIsRefused(t *testing.T) {
	s := service(t)

	server := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer server.Close()

	_, response, err := dial(t, server, url.Values{"ticket": {"not-a-ticket"}})

	if err == nil {
		t.Fatal("a made-up ticket opened a socket")
	}

	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("refused with %v, want 403", response)
	}
}

// The ticket is handed to the page over an authenticated request; it is not a
// licence for another site to open a shell with it.
func TestASocketFromAnotherOriginIsRefused(t *testing.T) {
	s := service(t)

	server := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer server.Close()

	ticket, _ := s.Store.IssueTicket(context.Background(), 1, "stream")

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + Path + "?ticket=" + ticket

	_, response, err := websocket.DefaultDialer.Dial(endpoint, http.Header{
		"Origin": {"https://somewhere-else.example"},
	})

	if err == nil {
		t.Fatal("a cross-origin socket was accepted")
	}

	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("refused with %v, want 403", response)
	}
}

// The dashboard subscribes and expects readings without asking again.
func TestStreamSendsSubscribedTopicsAndNothingElse(t *testing.T) {
	s := service(t)

	server := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer server.Close()

	ticket, _ := s.Store.IssueTicket(context.Background(), 1, "stream")

	socket, _, err := dial(t, server, url.Values{"ticket": {ticket}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()

	read(t, socket) // the connected status

	if err := socket.WriteJSON(map[string]any{
		"type":   "subscribe",
		"topics": []string{"metrics"},
	}); err != nil {
		t.Fatal(err)
	}

	update := read(t, socket)

	if update.Type != "update" || update.Topic != "metrics" {
		t.Fatalf("got %+v, want a metrics update", update)
	}

	// The shape the graphs read, arriving over the socket exactly as it does
	// over HTTP.
	var reading map[string]any

	if err := json.Unmarshal(update.Data.(json.RawMessage), &reading); err != nil {
		t.Fatalf("the reading was not an object: %v", err)
	}

	for _, key := range []string{"cpu", "memory", "disk", "load"} {
		if _, ok := reading[key]; !ok {
			t.Errorf("the reading is missing %q", key)
		}
	}
}

// A page that subscribes to nothing gets nothing: the socket is not a firehose
// somebody has to filter.
func TestStreamSendsNothingBeforeASubscription(t *testing.T) {
	s := service(t)

	server := httptest.NewServer(http.HandlerFunc(s.Handle))
	defer server.Close()

	ticket, _ := s.Store.IssueTicket(context.Background(), 1, "stream")

	socket, _, err := dial(t, server, url.Values{"ticket": {ticket}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()

	read(t, socket) // the connected status

	socket.SetReadDeadline(time.Now().Add(2500 * time.Millisecond))

	if _, raw, err := socket.ReadMessage(); err == nil {
		t.Fatalf("an unsubscribed socket was sent %s", raw)
	}
}
