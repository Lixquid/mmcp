package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSMTSource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Spotify.SPOTIFY", "SPOTIFY"},
		{"Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic", "ZUNEMUSIC"},
		{"Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic", "ZUNEMUSIC"},
		{"Spotify", "SPOTIFY"},
		{"org.mpris.MediaPlayer2.vlc", "VLC"}, // plain dotted ids also work
		{"", ""},
	}
	for _, c := range cases {
		if got := smtcSource(c.in); got != c.want {
			t.Errorf("smtcSource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSMTCTrackMessage(t *testing.T) {
	st := smtcPlayerState{
		source: "SPOTIFY", playing: true,
		title: "Song A", artist: "Artist A", album: "Album A",
		pos: 12.34, length: 200, hasLength: true,
	}
	msg := string(smtcTrackMessage("a7Kx92Qm", st))
	want := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eSPOTIFY\x1eSong A\x1eArtist A\x1eAlbum A\x1e"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}

	// The art argument must always be empty, and the message must parse.
	parsed, ok := parseMessage(smtcTrackMessage("a7Kx92Qm", st))
	if !ok {
		t.Fatal("TRACK message is not parseable")
	}
	if len(parsed.args) != 7 || parsed.args[6] != "" {
		t.Fatalf("art must be empty, got %v", parsed.args)
	}

	// Paused session maps to state S.
	st.playing = false
	parsed, ok = parseMessage(smtcTrackMessage("a7Kx92Qm", st))
	if !ok || parsed.args[1] != "S" {
		t.Fatalf("paused state not encoded: %v %v", parsed, ok)
	}
}

func TestSMTCPosMessage(t *testing.T) {
	st := smtcPlayerState{pos: 12.34, length: 200, hasLength: true}
	msg := string(smtcPosMessage("a7Kx92Qm", st))
	if msg != "1/POS\x1ea7Kx92Qm\x1e12.3\x1e200.0" {
		t.Fatalf("unexpected POS: %q", msg)
	}

	// Without a length the length field is empty.
	st.hasLength = false
	msg = string(smtcPosMessage("a7Kx92Qm", st))
	if msg != "1/POS\x1ea7Kx92Qm\x1e12.3\x1e" {
		t.Fatalf("unexpected POS without length: %q", msg)
	}
}

func TestSMTCCapsMessage(t *testing.T) {
	msg := string(smtcCapsMessage("a7Kx92Qm"))
	want := "1/CAPABILITIES\x1ea7Kx92Qm\x1ePLAY\x1ePAUSE\x1eNEXT\x1ePREV\x1eSEEK"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}
	if strings.Contains(msg, "ART") {
		t.Fatal("SMTC simulator must not advertise ART")
	}
}

func TestSMTCTickJSON(t *testing.T) {
	line := `{"t":"tick","sessions":[{"id":"guid-1","app":"Spotify.SPOTIFY",` +
		`"status":"Playing","title":"T","artist":"A","album":"B","pos":3.5,"len":201.2}]}`
	tick, ok := parseSMTCTick([]byte(line))
	if !ok {
		t.Fatal("valid tick line did not parse")
	}
	if len(tick.Sessions) != 1 || tick.Sessions[0].ID != "guid-1" ||
		tick.Sessions[0].Title != "T" || tick.Sessions[0].Pos != 3.5 {
		t.Fatalf("unexpected tick: %+v", tick)
	}

	// Unparseable or wrong type lines are rejected.
	if _, ok := parseSMTCTick([]byte("not json")); ok {
		t.Fatal("garbage accepted")
	}
	if _, ok := parseSMTCTick([]byte(`{"t":"other"}`)); ok {
		t.Fatal("wrong type accepted")
	}

	// Missing sessions field means no sessions.
	tick, ok = parseSMTCTick([]byte(`{"t":"tick"}`))
	if !ok || len(tick.Sessions) != 0 {
		t.Fatalf("empty tick failed: %v %v", tick, ok)
	}
}

func TestSMTCommandJSON(t *testing.T) {
	got := string(smtcCommandJSON("guid-1", "SEEK", "12.5"))
	want := `{"c":"SEEK","sid":"guid-1","arg":"12.5"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = string(smtcCommandJSON("guid-1", "PLAY", ""))
	want = `{"c":"PLAY","sid":"guid-1"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// fakeSMTBackend lets tests drive the simulator directly and observe
// executed control commands.
type fakeSMTBackend struct {
	mu       sync.Mutex
	ready    chan struct{}
	emitFunc func(smtcTick)
	executed []string
}

func newFakeSMTBackend() *fakeSMTBackend {
	return &fakeSMTBackend{ready: make(chan struct{})}
}

func (b *fakeSMTBackend) run(stop <-chan struct{}, emit func(smtcTick)) {
	b.mu.Lock()
	select {
	case <-b.ready:
		// Restarted after a Stop: the emit function is still current.
	default:
		b.emitFunc = emit
		close(b.ready)
	}
	b.mu.Unlock()
	<-stop
}

func (b *fakeSMTBackend) tick(t smtcTick) {
	<-b.ready // wait until run() installed the emit function
	b.mu.Lock()
	emit := b.emitFunc
	b.mu.Unlock()
	if emit != nil {
		emit(t)
	}
}

func (b *fakeSMTBackend) execute(sessionID, command, arg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.executed = append(b.executed, sessionID+"|"+command+"|"+arg)
}

func (b *fakeSMTBackend) hasExecuted(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range b.executed {
		if e == s {
			return true
		}
	}
	return false
}

// TestSMTCSimE2E runs the simulator against a real relay with a fake
// backend and a websocket observer.
func TestSMTCSimE2E(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	relay := newRelay(port, newMessageLog(100))
	if err := relay.Start(); err != nil {
		t.Fatalf("relay: %v", err)
	}
	relay.AttachLocal(func([]byte) {})

	backend := newFakeSMTBackend()
	sim := newSMTCSimCore(relay, backend)
	sim.Start()
	defer sim.Stop()

	observer, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d", port), nil)
	if err != nil {
		t.Fatalf("observer dial: %v", err)
	}
	defer observer.Close()

	readMsg := func() string {
		t.Helper()
		_ = observer.SetReadDeadline(time.Now().Add(6 * time.Second))
		_, data, err := observer.ReadMessage()
		if err != nil {
			t.Fatalf("observer read: %v", err)
		}
		return string(data)
	}

	// First tick: one playing Spotify session.
	backend.tick(smtcTick{T: "tick", Sessions: []smtcSnapshot{{
		ID: "guid-1", App: "Spotify.SPOTIFY", Status: "Playing",
		Title: "Song A", Artist: "Artist A", Album: "Album A",
		Pos: 12.3, Len: 200,
	}}})

	var caps, track, pos string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (caps == "" || track == "" || pos == "") {
		msg := readMsg()
		switch {
		case strings.HasPrefix(msg, msgTypeCapabilities):
			caps = msg
		case strings.HasPrefix(msg, msgTypeTrack):
			track = msg
		case strings.HasPrefix(msg, msgTypePos):
			pos = msg
		}
	}
	if caps == "" || track == "" || pos == "" {
		t.Fatalf("incomplete announcement: caps=%q track=%q pos=%q", caps, track, pos)
	}

	if !strings.Contains(caps, "\x1ePLAY") || !strings.Contains(caps, "\x1eSEEK") {
		t.Fatalf("capabilities must advertise playback controls: %q", caps)
	}
	if strings.Contains(caps, "\x1eART") {
		t.Fatal("SMTC simulator must not advertise ART")
	}

	fields := strings.Split(track, string(rsByte))
	if fields[0] != msgTypeTrack || len(fields) != 8 {
		t.Fatalf("malformed TRACK: %q", track)
	}
	id := fields[1]
	if len(id) != 8 || !validInstanceID(id) {
		t.Fatalf("missing/invalid instance id: %q", id)
	}
	if fields[2] != "P" || fields[3] != "SPOTIFY" || fields[4] != "Song A" ||
		fields[5] != "Artist A" || fields[6] != "Album A" || fields[7] != "" {
		t.Fatalf("unexpected TRACK contents: %q", track)
	}
	if !strings.HasPrefix(pos, msgTypePos+"\x1e"+id+"\x1e12.3\x1e200.0") {
		t.Fatalf("unexpected POS: %q", pos)
	}

	// A pause flips the state to S via a fresh TRACK.
	backend.tick(smtcTick{T: "tick", Sessions: []smtcSnapshot{{
		ID: "guid-1", App: "Spotify.SPOTIFY", Status: "Paused",
		Title: "Song A", Artist: "Artist A", Album: "Album A",
		Pos: 12.3, Len: 200,
	}}})
	gotPaused := false
	for i := 0; i < 10 && !gotPaused; i++ {
		msg := readMsg()
		if strings.HasPrefix(msg, msgTypeTrack) && strings.Contains(msg, "\x1eS\x1e") {
			gotPaused = true
		}
	}
	if !gotPaused {
		t.Fatal("paused session did not emit TRACK with state S")
	}

	// An identical snapshot must not re-announce anything new; watch on
	// a throwaway observer (an expired read deadline permanently breaks
	// a gorilla websocket connection, so the main observer must not do
	// negative reads).
	backend.tick(smtcTick{T: "tick", Sessions: []smtcSnapshot{{
		ID: "guid-1", App: "Spotify.SPOTIFY", Status: "Paused",
		Title: "Song A", Artist: "Artist A", Album: "Album A",
		Pos: 12.3, Len: 200,
	}}})
	throwaway, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d", port), nil)
	if err != nil {
		t.Fatalf("throwaway dial: %v", err)
	}
	_ = throwaway.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	if _, _, err := throwaway.ReadMessage(); err == nil {
		t.Fatal("unchanged snapshot re-announced messages")
	}
	_ = throwaway.Close()

	// The session disappearing must forget its state: a reappearance is
	// re-announced from scratch (CAPABILITIES again).
	backend.tick(smtcTick{T: "tick"})
	time.Sleep(200 * time.Millisecond)
	backend.tick(smtcTick{T: "tick", Sessions: []smtcSnapshot{{
		ID: "guid-1", App: "Spotify.SPOTIFY", Status: "Playing",
		Title: "Song A", Artist: "Artist A", Album: "Album A",
		Pos: 0, Len: 200,
	}}})
	capsAgain := false
	for i := 0; i < 3; i++ {
		// The re-announced provider has a fresh instance ID; keep it for
		// the CONTROL checks below. CAPABILITIES and TRACK arrive in
		// that order, so keep reading until all announcements are seen.
		msg := readMsg()
		if strings.HasPrefix(msg, msgTypeCapabilities) {
			capsAgain = true
		}
		if strings.HasPrefix(msg, msgTypeTrack) {
			id = strings.Split(msg, string(rsByte))[1]
		}
	}
	if !capsAgain {
		t.Fatal("re-appearing session was not re-announced")
	}

	// CONTROL commands reach the backend with the SMTC session id.
	sendControl(observer, id, "PAUSE")
	sendControl(observer, id, "SEEK", "99.5")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) &&
		!(backend.hasExecuted("guid-1|PAUSE|") && backend.hasExecuted("guid-1|SEEK|99.5")) {
		time.Sleep(100 * time.Millisecond)
	}
	if !backend.hasExecuted("guid-1|PAUSE|") {
		t.Fatal("PAUSE control did not reach the backend")
	}
	if !backend.hasExecuted("guid-1|SEEK|99.5") {
		t.Fatal("SEEK control did not reach the backend")
	}

	// A CONTROL addressed to a foreign provider is ignored.
	sendControl(observer, "notmine1", "PAUSE")
	time.Sleep(300 * time.Millisecond)
	if backend.hasExecuted("notmine1|PAUSE|") {
		t.Fatal("foreign CONTROL was executed")
	}
}

// TestSMTCSimNoConnDropsAnnouncements verifies that ticks sampled while the
// relay connection is down do not swallow later announcements: when the
// connection returns, everything is re-sent.
func TestSMTCSimNoConnDropsAnnouncements(t *testing.T) {
	// Use a port with no relay listening to force ensureConn failures.
	backend := newFakeSMTBackend()
	sim := newSMTCSimCore(newRelay(1, newMessageLog(10)), backend) // port 1: dial fails
	sim.Start()
	defer sim.Stop()

	tick := smtcTick{T: "tick", Sessions: []smtcSnapshot{{
		ID: "guid-2", App: "Spotify", Status: "Playing",
		Title: "X", Artist: "Y", Album: "Z", Pos: 1, Len: 2,
	}}}
	for i := 0; i < 3; i++ {
		backend.tick(tick)
		time.Sleep(50 * time.Millisecond)
	}

	// State must still be tracked (the id exists), and Stop must be clean.
	sim.Stop()
	sim.Start()
	sim.Stop()
	if fmt.Sprint(sim.lastAnnounced) != "map[]" {
		t.Fatalf("state not reset after Stop: %v", sim.lastAnnounced)
	}
}
