package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

const rsByte = '\x1e'

const broadcastID = "*"

// MessageLog records every message that passes through the relay.
type MessageLog struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	paused   bool
	onChange func()
}

func newMessageLog(maxLines int) *MessageLog {
	return &MessageLog{maxLines: maxLines}
}

// SetOnChange installs a callback fired after every new entry. It is called
// from arbitrary goroutines.
func (l *MessageLog) SetOnChange(cb func()) {
	l.mu.Lock()
	l.onChange = cb
	l.mu.Unlock()
}

// SetPaused pauses or resumes recording. While paused, Add drops messages
// without recording them (and without firing the change callback). The
// already-recorded history is kept and shown again on resume.
func (l *MessageLog) SetPaused(paused bool) {
	l.mu.Lock()
	l.paused = paused
	l.mu.Unlock()
}

// Paused reports whether recording is currently paused.
func (l *MessageLog) Paused() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.paused
}

// Clear removes all recorded messages.
func (l *MessageLog) Clear() {
	l.mu.Lock()
	l.lines = nil
	cb := l.onChange
	l.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// Add records a message that passed through the relay.
func (l *MessageLog) Add(source string, data []byte) {
	l.mu.Lock()

	if l.paused {
		l.mu.Unlock()
		return
	}

	ts := time.Now().Format("15:04:05.000")
	l.lines = append(l.lines,
		fmt.Sprintf("%s  %-24s %s", ts, source, sanitizeForLog(data)))
	if len(l.lines) > l.maxLines {
		l.lines = l.lines[len(l.lines)-l.maxLines:]
	}
	cb := l.onChange

	l.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// Text returns the log contents.
func (l *MessageLog) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// sanitizeForLog makes a message readable in the debug panel: RS separators
// become the visible "␟" symbol and other control characters are dropped.
func sanitizeForLog(data []byte) string {
	var b strings.Builder
	for _, r := range string(data) {
		switch {
		case r == rsByte:
			b.WriteRune('␟')
		case unicode.IsPrint(r) || r == '\t':
			b.WriteRune(r)
		default:
			b.WriteRune('·')
		}
	}
	return b.String()
}

// client is one endpoint connected to the relay: a remote WebSocket peer or
// the in-process hub UI.
type client struct {
	source   string
	conn     *websocket.Conn // nil for the local hub UI client
	outbound chan []byte     // remote clients only
	deliver  func([]byte)    // local client only
}

// Relay is the stateless MMCP relay: it accepts WebSocket connections and
// rebroadcasts every text message to all other connected clients.
type Relay struct {
	port int
	log  *MessageLog

	mu       sync.Mutex
	clients  map[*client]struct{}
	local    *client
	onUpdate func()
	onMsg    func([]byte)
}

func newRelay(port int, log *MessageLog) *Relay {
	return &Relay{
		port:    port,
		log:     log,
		clients: make(map[*client]struct{}),
	}
}

// SetOnUpdate installs a callback fired whenever the set of connected
// clients changes. It is called from arbitrary goroutines.
func (r *Relay) SetOnUpdate(cb func()) {
	r.mu.Lock()
	r.onUpdate = cb
	r.mu.Unlock()
}

// SetOnMessage installs a callback fired for every message that passes
// through the relay, including messages injected by the hub itself. It is
// called from arbitrary goroutines and must not block.
func (r *Relay) SetOnMessage(cb func([]byte)) {
	r.mu.Lock()
	r.onMsg = cb
	r.mu.Unlock()
}

// Start begins listening for relay connections.
func (r *Relay) Start() error {
	addr := fmt.Sprintf(":%d", r.port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("relay: listen on %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleWS)

	go func() {
		_ = http.Serve(ln, mux)
	}()

	return nil
}

var upgrader = websocket.Upgrader{
	// The relay runs in a trusted local environment.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (r *Relay) handleWS(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	c := &client{
		source:   "ws/" + req.RemoteAddr,
		conn:     conn,
		outbound: make(chan []byte, 64),
	}
	r.add(c)
	defer r.remove(c)

	// Writer: drains the outbound queue.
	go func() {
		for msg := range c.outbound {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				_ = conn.Close()
				return
			}
		}
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType != websocket.TextMessage {
			continue
		}
		r.broadcast(data, c)
	}
}

func (r *Relay) add(c *client) {
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	r.notifyUpdate()
}

func (r *Relay) remove(c *client) {
	r.mu.Lock()
	delete(r.clients, c)
	r.mu.Unlock()
	r.notifyUpdate()
}

func (r *Relay) notifyUpdate() {
	r.mu.Lock()
	cb := r.onUpdate
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// AttachLocal registers the in-process hub UI as a relay client. Deliveries
// happen synchronously from relay reader goroutines, so deliver must be
// fast and thread-safe.
func (r *Relay) AttachLocal(deliver func([]byte)) *client {
	c := &client{source: "hub/ui", deliver: deliver}
	r.mu.Lock()
	r.local = c
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	r.notifyUpdate()
	return c
}

// broadcast sends msg to every connected client except the sender, and
// records it in the message log. This is the relay's only job.
func (r *Relay) broadcast(msg []byte, from *client) {
	r.log.Add(sourceOf(from), msg)

	r.mu.Lock()
	onMsg := r.onMsg
	targets := make([]*client, 0, len(r.clients))
	for c := range r.clients {
		if c == from {
			continue
		}
		targets = append(targets, c)
	}
	r.mu.Unlock()

	for _, c := range targets {
		if c.conn == nil {
			if c.deliver != nil {
				c.deliver(msg)
			}
			continue
		}
		select {
		case c.outbound <- msg:
		default:
			// Client cannot keep up: drop rather than block the relay.
		}
	}

	if onMsg != nil {
		onMsg(msg)
	}
}

// SendFromLocal injects a message from the hub UI, as though the UI were a
// connected controller. Per the relay rules the UI does not receive its own
// message back.
func (r *Relay) SendFromLocal(msg []byte) bool {
	r.mu.Lock()
	local := r.local
	r.mu.Unlock()

	if local == nil {
		return false
	}

	r.broadcast(msg, local)
	return true
}

// ClientCount reports the number of connected clients (including the
// hub UI itself).
func (r *Relay) ClientCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients)
}

// Port reports the relay port.
func (r *Relay) Port() int { return r.port }

func sourceOf(c *client) string {
	if c == nil {
		return "hub/ui"
	}
	return c.source
}
