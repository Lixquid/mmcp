package main

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/gorilla/websocket"
)

// fakeMPRISPlayer implements the org.mpris.MediaPlayer2.Player interface on
// a real D-Bus session bus so the simulator e2e can run in CI.
type fakeMPRISPlayer struct {
	mu     sync.Mutex // guards all fields (D-Bus calls arrive on their own goroutine)
	status string     // Playing / Paused / Stopped
	title  string
	artist string
	album  string
	posUs  int64
	lenUs  int64
}

// with locks the player and applies fn to it.
func (p *fakeMPRISPlayer) with(fn func(p *fakeMPRISPlayer)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
}

func (p *fakeMPRISPlayer) PlaybackStatus() (string, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, nil
}
func (p *fakeMPRISPlayer) Metadata() (map[string]dbus.Variant, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	meta := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/mpris/track/1")),
	}
	if p.title != "" {
		meta["xesam:title"] = dbus.MakeVariant(p.title)
	}
	if p.artist != "" {
		meta["xesam:artist"] = dbus.MakeVariant([]string{p.artist})
	}
	if p.album != "" {
		meta["xesam:album"] = dbus.MakeVariant(p.album)
	}
	if p.lenUs > 0 {
		meta["mpris:length"] = dbus.MakeVariant(p.lenUs)
	}
	return meta, nil
}
func (p *fakeMPRISPlayer) Position() (int64, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.posUs, nil
}

// mprisProperties serves org.freedesktop.DBus.Properties for the fake
// player: godbus v5.1 does not synthesize that server side, so Get/GetAll
// must be exported explicitly.
type mprisProperties struct {
	p *fakeMPRISPlayer
}

func (m *mprisProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != "org.mpris.MediaPlayer2.Player" {
		return nil, dbus.MakeFailedError(fmt.Errorf("unknown interface %s", iface))
	}
	meta, _ := m.p.Metadata()
	return map[string]dbus.Variant{
		"PlaybackStatus": dbus.MakeVariant(m.p.status),
		"Metadata":       dbus.MakeVariant(meta),
		"Position":       dbus.MakeVariant(m.p.posUs),
	}, nil
}

func (m *mprisProperties) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	all, err := m.GetAll(iface)
	if err != nil {
		return dbus.Variant{}, err
	}
	v, ok := all[prop]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s", prop))
	}
	return v, nil
}

func (m *mprisProperties) Set(string, string, dbus.Variant) *dbus.Error { return nil }

func (p *fakeMPRISPlayer) Play() *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) { p.status = "Playing" })
	return nil
}
func (p *fakeMPRISPlayer) Pause() *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) { p.status = "Paused" })
	return nil
}
func (p *fakeMPRISPlayer) PlayPause() *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) {
		if p.status == "Playing" {
			p.status = "Paused"
		} else {
			p.status = "Playing"
		}
	})
	return nil
}
func (p *fakeMPRISPlayer) Next() *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) { p.title = "Next Song" })
	return nil
}
func (p *fakeMPRISPlayer) Previous() *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) { p.title = "Previous Song" })
	return nil
}

func (p *fakeMPRISPlayer) SetPosition(trackID dbus.ObjectPath, pos int64) *dbus.Error {
	p.with(func(p *fakeMPRISPlayer) { p.posUs = pos })
	return nil
}

// exportFakeMPRISPlayer exports the fake player on the given bus connection
// under org.mpris.MediaPlayer2.e2etest.
func exportFakeMPRISPlayer(conn *dbus.Conn) (*fakeMPRISPlayer, error) {
	p := &fakeMPRISPlayer{
		status: "Playing", title: "Never Gonna Give You Up",
		artist: "Rick Astley", album: "Whenever You Need Somebody",
		posUs: 37_421_000, lenUs: 213_000_000,
	}
	// Export the interface on the default MPRIS object path. The hub
	// simulator looks players up by bus name, so we must own one.
	if err := conn.Export(p, "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player"); err != nil {
		return nil, err
	}
	if err := conn.Export(&mprisProperties{p: p},
		"/org/mpris/MediaPlayer2", "org.freedesktop.DBus.Properties"); err != nil {
		return nil, err
	}
	reply, err := conn.RequestName("org.mpris.MediaPlayer2.e2etest", dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("name already owned (reply %v)", reply)
	}
	return p, nil
}

func TestMPRISSimulatorEndToEnd(t *testing.T) {
	// Private D-Bus session bus for this test.
	addr, cleanup := startSessionBus(t)
	defer cleanup()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	// SessionBus picks up the private bus from the env set above and
	// performs the bus Hello handshake.
	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("session bus: %v", err)
	}
	defer conn.Close()

	player, err := exportFakeMPRISPlayer(conn)
	if err != nil {
		t.Fatalf("export player: %v", err)
	}

	relay := newRelay(19733, newMessageLog(100))
	if err := relay.Start(); err != nil {
		t.Fatalf("relay: %v", err)
	}
	relay.AttachLocal(func([]byte) {})

	sim := newMPRISSim(relay)
	sim.Start()
	defer sim.Stop()

	observer, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:19733", nil)
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

	// Expect: CAPABILITIES (no ART), TRACK with empty art, POS.
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
		t.Fatal("MPRIS simulator must not advertise ART")
	}

	fields := strings.Split(track, string(rsByte))
	if fields[0] != msgTypeTrack || len(fields) != 8 {
		t.Fatalf("malformed TRACK: %q", track)
	}
	id := fields[1]
	if len(id) != 8 || !validInstanceID(id) {
		t.Fatalf("missing/invalid instance id: %q", id)
	}
	// Source: bus suffix "e2etest" uppercased.
	if fields[2] != "P" || fields[3] != "E2ETEST" || fields[4] != "Never Gonna Give You Up" ||
		fields[5] != "Rick Astley" || fields[6] != "Whenever You Need Somebody" || fields[7] != "" {
		t.Fatalf("unexpected TRACK contents: %q", track)
	}
	if !strings.HasPrefix(pos, msgTypePos+"\x1e"+id+"\x1e37.4") {
		t.Fatalf("unexpected POS: %q", pos)
	}

	// CONTROL PAUSE: the simulated provider must flip the MPRIS player to
	// stopped, which shows up as a TRACK with state S.
	sendControl(observer, id, "PAUSE")
	gotPaused := false
	for i := 0; i < 30 && !gotPaused; i++ {
		msg := readMsg()
		if strings.HasPrefix(msg, msgTypeTrack) && strings.Contains(msg, "\x1eS\x1e") {
			gotPaused = true
		}
	}
	if !gotPaused {
		t.Fatal("PAUSE control did not flip the player to stopped")
	}
	player.mu.Lock()
	st := player.status
	player.mu.Unlock()
	if st != "Paused" {
		t.Fatalf("MPRIS player status = %q, want Paused", st)
	}

	// CONTROL PLAY resumes.
	sendControl(observer, id, "PLAY")
	gotPlaying := false
	for i := 0; i < 30 && !gotPlaying; i++ {
		msg := readMsg()
		if strings.HasPrefix(msg, msgTypeTrack) && strings.Contains(msg, "\x1eP\x1e") {
			gotPlaying = true
		}
	}
	if !gotPlaying {
		t.Fatal("PLAY control did not resume the player")
	}
	player.mu.Lock()
	st = player.status
	player.mu.Unlock()
	if st != "Playing" {
		t.Fatalf("PLAY control failed: status=%q", st)
	}

	// CONTROL SEEK moves the MPRIS position (microseconds).
	sendControl(observer, id, "SEEK", "99.5")
	time.Sleep(500 * time.Millisecond)
	player.mu.Lock()
	posUs := player.posUs
	player.mu.Unlock()
	if posUs != 99_500_000 {
		t.Fatalf("SEEK not applied to MPRIS player, posUs=%d", posUs)
	}
}

func sendControl(conn *websocket.Conn, id, command string, args ...string) {
	data, _ := encodeControl(id, command, args...)
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

// startSessionBus launches a private D-Bus session bus and returns its
// address plus a cleanup function.
func startSessionBus(t *testing.T) (string, func()) {
	t.Helper()
	cmd := exec.Command("dbus-daemon", "--session", "--print-address", "--nofork", "--nopidfile", "--nosyslog")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon unavailable: %v", err)
	}

	var sb strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := stdout.Read(buf)
		sb.Write(buf[:n])
		if err != nil || strings.Contains(sb.String(), "unix:") {
			break
		}
	}
	addr := strings.TrimSpace(sb.String())
	if addr == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("dbus-daemon printed no address")
	}
	return addr, func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
