package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
)

const rs = "\x1e"

// fakeRelay is a minimal stand-in for a stateless MMCP relay that also
// plays the role of a single provider: it answers broadcast INFO with
// TRACK, POS, and CAPABILITIES, and echoes other CONTROL messages.
type fakeRelay struct {
	sync.Mutex

	server *httptest.Server

	controllerConn chan *websocket.Conn

	gotInfo chan string
	gotCmd  chan string
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()

	r := &fakeRelay{
		controllerConn: make(chan *websocket.Conn, 1),
		gotInfo:        make(chan string, 8),
		gotCmd:         make(chan string, 8),
	}

	upgrader := websocket.Upgrader{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		r.controllerConn <- conn

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.TextMessage {
				continue
			}
			text := string(data)

			// Broadcast INFO: respond as a provider would.
			if text == "1/CONTROL"+rs+"*"+rs+"INFO" {
				r.gotInfo <- text
				_ = r.write(conn,
					"1/CAPABILITIES"+rs+"a7Kx92Qm"+rs+"PLAY"+rs+"PAUSE"+rs+"NEXT"+rs+"PREV"+rs+"SEEK"+rs+"ART")
				_ = r.write(conn,
					"1/TRACK"+rs+"a7Kx92Qm"+rs+"P"+rs+"YTM"+rs+"Song A"+rs+"Artist A"+rs+"Album A"+rs+"https://example.invalid/art.jpg")
				_ = r.write(conn,
					"1/POS"+rs+"a7Kx92Qm"+rs+"37.4"+rs+"213.0")
				continue
			}

			// Targeted CONTROL: echo back for assertion.
			if strings.HasPrefix(text, "1/CONTROL"+rs+"a7Kx92Qm") {
				r.gotCmd <- text
			}
			// Everything else (unknown, malformed) is ignored, as a relay
			// and provider would.
		}
	}))

	t.Cleanup(r.server.Close)
	return r
}

// write sends a text message; gorilla/websocket forbids concurrent writes
// to a single connection, so all writes are serialized.
func (r *fakeRelay) write(conn *websocket.Conn, text string) error {
	r.Lock()
	defer r.Unlock()
	return conn.WriteMessage(websocket.TextMessage, []byte(text))
}

func TestEndToEnd(t *testing.T) {
	relay := newFakeRelay(t)
	wsURL := "ws" + strings.TrimPrefix(relay.server.URL, "http")

	m := newModel(wsURL)

	program := tea.NewProgram(
		m,
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	m.ws.start(program)
	t.Cleanup(m.ws.stop)

	programDone := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(programDone)
	}()

	// The controller must send a broadcast INFO on connect.
	select {
	case info := <-relay.gotInfo:
		if info != "1/CONTROL"+rs+"*"+rs+"INFO" {
			t.Fatalf("connect INFO = %q", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for broadcast INFO")
	}

	// The provider state must appear in the controller's derived state.
	waitFor(t, "provider discovery", func() bool {
		p, ok := m.state.view("a7Kx92Qm")
		return ok &&
			p.State == statePlaying &&
			p.Track != nil && p.Track.title == "Song A" &&
			p.Pos != nil && p.Pos.position == 37.4 && p.Pos.length == 213 &&
			len(p.Caps) == 6
	})

	// The playing provider must become the command target.
	if id := m.state.playingID(); id != "a7Kx92Qm" {
		t.Fatalf("playing = %q, want a7Kx92Qm", id)
	}
	if target, ok := m.target(); !ok || target.ID != "a7Kx92Qm" {
		t.Fatalf("target = %+v", target)
	}

	// Send a SEEK via the model and verify it reaches the relay.
	cmd := m.sendSeekTo(92.5)
	if cmd != nil {
		_ = cmd()
	}
	select {
	case got := <-relay.gotCmd:
		want := "1/CONTROL" + rs + "a7Kx92Qm" + rs + "SEEK" + rs + "92.5"
		if got != want {
			t.Fatalf("SEEK = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SEEK")
	}

	// PAUSE must be delivered as a plain CONTROL.
	cmd = m.sendCommand("PAUSE")
	if cmd != nil {
		_ = cmd()
	}
	select {
	case got := <-relay.gotCmd:
		want := "1/CONTROL" + rs + "a7Kx92Qm" + rs + "PAUSE"
		if got != want {
			t.Fatalf("PAUSE = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PAUSE")
	}

	// A malformed message from the relay must be silently ignored.
	if conn := <-relay.controllerConn; conn != nil {
		_ = relay.write(conn, "2/TRACK"+rs+"whatever")
		_ = relay.write(conn, "1/TRACK"+rs+"a7Kx92Qm")
	}

	// Pressing i (broadcast INFO) must clear all providers and rebuild
	// from the responses.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if len(m.state.providers) != 0 || m.state.playingID() != "" {
		t.Fatal("providers not cleared on broadcast INFO")
	}

	// The relay answers the new INFO; the provider must reappear.
	waitFor(t, "rediscovery after INFO", func() bool {
		v, ok := m.state.view("a7Kx92Qm")
		return ok && v.State == statePlaying && v.Track != nil && v.Track.title == "Song A"
	})

	// Shut down cleanly.
	program.Quit()
	select {
	case <-programDone:
	case <-time.After(5 * time.Second):
		t.Fatal("program did not exit")
	}
}

// TestEndToEndGarbage ensures invalid input never causes a disconnect: the
// relay floods the controller with garbage, then valid state must still be
// processed over the same connection.
func TestEndToEndGarbage(t *testing.T) {
	relay := newFakeRelay(t)
	wsURL := "ws" + strings.TrimPrefix(relay.server.URL, "http")

	m := newModel(wsURL)

	program := tea.NewProgram(
		m,
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	m.ws.start(program)
	t.Cleanup(m.ws.stop)

	programDone := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(programDone)
	}()

	<-relay.gotInfo // connected; broadcast INFO sent

	conn := <-relay.controllerConn
	for i := 0; i < 50; i++ {
		// Valid UTF-8 but protocol-invalid: unknown type, wrong arg counts,
		// invalid instance ID, invalid numerics.
		_ = relay.write(conn, "garbage")
		_ = relay.write(conn, "9/NOPE")
		_ = relay.write(conn, "1/POS")
		_ = relay.write(conn, "1/TRACK"+rs+"bad!id!!"+rs+"P")
		_ = relay.write(conn, "1/POS"+rs+"a7Kx92Qm"+rs+"NaN"+rs+"100")
		_ = relay.write(conn, "1/TRACK"+rs+"a7Kx92Qm"+rs+"X"+rs+"YTM"+rs+"t"+rs+"a"+rs+"l"+rs+"")
	}

	_ = relay.write(conn,
		"1/TRACK"+rs+"q4Mn8Z2x"+rs+"S"+rs+"SOUNDCLOUD"+rs+"Song B"+rs+"Artist B"+rs+"Album B"+rs+"")

	waitFor(t, "valid TRACK after garbage", func() bool {
		p, ok := m.state.view("q4Mn8Z2x")
		return ok && p.Track != nil && p.Track.title == "Song B" && p.State == stateStopped
	})

	program.Quit()
	select {
	case <-programDone:
	case <-time.After(5 * time.Second):
		t.Fatal("program did not exit")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
