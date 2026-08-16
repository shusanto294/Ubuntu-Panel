package terminal

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// streamInterval is how often a subscribed topic is looked at. The pages used
// to ask over HTTP at about this rate; the difference is that this reads three
// files in /proc rather than opening a session and running the middleware for
// each one.
const streamInterval = time.Second

// subscriptions is what one socket is watching.
type subscriptions struct {
	sync.Mutex
	topics []string
}

func (s *subscriptions) set(topics []string) {
	s.Lock()
	defer s.Unlock()

	// Capped: the topic list arrives from the browser, and a page with a
	// thousand of them would be a thousand queries a second.
	if len(topics) > 32 {
		topics = topics[:32]
	}

	s.topics = topics
}

func (s *subscriptions) all() []string {
	s.Lock()
	defer s.Unlock()

	return append([]string(nil), s.topics...)
}

// stream sends updates for whatever the page subscribed to, and nothing else.
//
// A topic is `metrics` or `task:<id>`. Both are read from this machine, so the
// answer costs microseconds and there is no queue, daemon or round trip
// involved — which is the whole reason the socket exists.
func (s *Service) stream(c *conn) {
	watching := &subscriptions{}

	c.send(frame{Type: "status", State: "connected"})

	done := make(chan struct{})

	// The reader is also the keepalive: a socket nobody reads from never learns
	// that the browser went away.
	go func() {
		defer close(done)

		for {
			var incoming message

			if err := c.socket.ReadJSON(&incoming); err != nil {
				return
			}

			if incoming.Type == "subscribe" {
				watching.set(incoming.Topics)
			}
		}
	}()

	ticker := time.NewTicker(streamInterval)
	defer ticker.Stop()

	// What each topic last sent, so an unchanged task is not resent every
	// second. Metrics always change and are always sent.
	sent := map[string]string{}

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		for _, topic := range watching.all() {
			payload, ok := s.read(topic)
			if !ok {
				continue
			}

			if topic != "metrics" {
				encoded, err := json.Marshal(payload)
				if err != nil {
					continue
				}

				if sent[topic] == string(encoded) {
					continue
				}

				sent[topic] = string(encoded)
			}

			if c.send(frame{Type: "update", Topic: topic, Data: payload}) != nil {
				return
			}
		}
	}
}

// read is one topic's current value.
func (s *Service) read(topic string) (any, bool) {
	if topic == "metrics" {
		return system.Read().Summary(), true
	}

	id, found := strings.CutPrefix(topic, "task:")
	if !found {
		return nil, false
	}

	taskID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || taskID <= 0 {
		return nil, false
	}

	console := s.Store.TaskConsole(context.Background(), taskID, 0)
	if console == nil {
		return nil, false
	}

	return console, true
}
