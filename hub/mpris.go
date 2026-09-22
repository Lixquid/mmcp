//go:build !windows

package main

// The MPRIS simulator is an optional, built-in simulated Provider: it talks
// to the system MPRIS D-Bus interface (the standard media-player control
// interface on Linux desktops) and behaves like a connected MMCP Provider
// for every MPRIS player it finds. It announces itself with CAPABILITIES,
// emits TRACK/POS derived from the players' state, and performs CONTROL
// commands on the underlying players.
//
// Album art is deliberately not supported: MPRIS exposes artwork as a local
// file:// URI, which is not globally accessible to other MMCP peers, so the
// simulated provider never advertises the ART capability and its TRACK art
// argument is always empty.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/gorilla/websocket"
)

const (
	// mprisNamePrefix is the D-Bus name prefix every MPRIS player
	// registers.
	mprisNamePrefix = "org.mpris.MediaPlayer2."

	// mprisIface and mprisPath identify the player interface.
	mprisIface = "org.mpris.MediaPlayer2.Player"
	mprisPath  = dbus.ObjectPath("/org/mpris/MediaPlayer2")

	// mprisPollInterval is how often the simulated provider samples the
	// MPRIS players.
	mprisPollInterval = time.Second

	// mprisReconnect is the delay before retrying the session bus.
	mprisReconnect = 2 * time.Second
)

// mprisSupported reports whether this build can talk to MPRIS. The Windows
// build has no D-Bus support (see mpris_windows.go).
func mprisSupported() bool { return true }

// mprisPlayerState is one sampled MPRIS player, in MMCP terms.
type mprisPlayerState struct {
	source    string // e.g. VLC, SPOTIFY
	playing   bool
	title     string
	artist    string
	album     string
	pos       float64
	length    float64
	hasLength bool
}

// mprisSim is the simulated provider backed by the system MPRIS players.
type mprisSim struct {
	relay *Relay

	mu      sync.Mutex
	running bool
	stop    chan struct{}
	conn    *websocket.Conn

	// lastAnnounced holds the TRACK signature sent per instance ID, so a
	// TRACK is only emitted when something changes.
	lastAnnounced map[string]string

	// announcedCaps records whether CAPABILITIES was sent per instance ID.
	announcedCaps map[string]bool

	// ids maps an MPRIS bus-name suffix to a stable MMCP instance ID for
	// as long as the simulator stays enabled.
	ids map[string]string
}

// newMPRISSim creates a simulated MPRIS provider for the given relay.
func newMPRISSim(relay *Relay) *mprisSim {
	return &mprisSim{
		relay:         relay,
		stop:          make(chan struct{}),
		lastAnnounced: make(map[string]string),
		announcedCaps: make(map[string]bool),
		ids:           make(map[string]string),
	}
}

// Start begins sampling MPRIS and impersonating a provider on the relay.
func (s *mprisSim) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stop = make(chan struct{})
	stop := s.stop
	s.mu.Unlock()

	go s.run(stop)
}

// Stop ceases all MPRIS sampling and disconnects from the relay.
func (s *mprisSim) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stop)
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	// Forget per-run state so a later restart starts clean.
	s.lastAnnounced = make(map[string]string)
	s.announcedCaps = make(map[string]bool)
}

// run is the simulator's main loop: it connects to the session bus, dials
// the relay, and polls MPRIS until stopped.
func (s *mprisSim) run(stop chan struct{}) {
	var bus *dbus.Conn
	for {
		var err error
		bus, err = dbus.SessionBus()
		if err == nil {
			break
		}
		select {
		case <-stop:
			return
		case <-time.After(mprisReconnect):
		}
	}
	defer bus.Close()

	ticker := time.NewTicker(mprisPollInterval)
	defer ticker.Stop()

	// Reader: handles CONTROL messages addressed to the simulated
	// instances.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		s.readLoop(stop)
	}()

	s.poll(bus)
	for {
		select {
		case <-stop:
			<-readerDone
			return
		case <-ticker.C:
			s.poll(bus)
		}
	}
}

// poll samples every MPRIS player on the session bus and emits MMCP
// messages for the changes.
func (s *mprisSim) poll(bus *dbus.Conn) {
	if !s.ensureConn() {
		return
	}

	seen := make(map[string]bool)
	for _, name := range listMPRISPlayers(bus) {
		suffix := strings.TrimPrefix(name, mprisNamePrefix)
		seen[suffix] = true

		st, ok := samplePlayer(bus, name)
		if !ok {
			continue
		}
		s.announce(suffix, st)
	}
	s.forgetUnseen(seen)
}

// listMPRISPlayers returns the bus names of all MPRIS players running.
func listMPRISPlayers(bus *dbus.Conn) []string {
	var names []string
	obj := bus.Object("org.freedesktop.DBus", dbus.ObjectPath("/org/freedesktop/DBus"))
	if err := obj.Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil
	}
	var players []string
	for _, n := range names {
		if strings.HasPrefix(n, mprisNamePrefix) {
			players = append(players, n)
		}
	}
	return players
}

// samplePlayer reads one MPRIS player's state over D-Bus.
func samplePlayer(bus *dbus.Conn, name string) (mprisPlayerState, bool) {
	obj := bus.Object(name, mprisPath)

	status, err := obj.GetProperty(mprisIface + ".PlaybackStatus")
	if err != nil {
		return mprisPlayerState{}, false
	}
	playing := status.Value() == "Playing"

	metaVar, err := obj.GetProperty(mprisIface + ".Metadata")
	if err != nil {
		return mprisPlayerState{}, false
	}

	st := mprisPlayerState{
		source:  mprisSource(name),
		playing: playing,
	}
	if meta, ok := metaVar.Value().(map[string]dbus.Variant); ok {
		if v, ok := meta["xesam:title"]; ok {
			st.title, _ = v.Value().(string)
		}
		if v, ok := meta["xesam:artist"]; ok {
			if artists, ok := v.Value().([]string); ok {
				st.artist = strings.Join(artists, ", ")
			}
		}
		if v, ok := meta["xesam:album"]; ok {
			st.album, _ = v.Value().(string)
		}
		if v, ok := meta["mpris:length"]; ok {
			if us, ok := v.Value().(int64); ok && us > 0 {
				st.length = float64(us) / 1e6
				st.hasLength = true
			}
		}
	}

	if posVar, err := obj.GetProperty(mprisIface + ".Position"); err == nil {
		if us, ok := posVar.Value().(int64); ok && us >= 0 {
			st.pos = float64(us) / 1e6
		}
	}
	return st, true
}

// mprisSource derives the MMCP source label from an MPRIS bus name:
// "org.mpris.MediaPlayer2.vlc.instance7" becomes "VLC".
func mprisSource(busName string) string {
	suffix := strings.TrimPrefix(busName, mprisNamePrefix)
	if i := strings.Index(suffix, ".instance"); i >= 0 {
		suffix = suffix[:i]
	}
	return strings.ToUpper(suffix)
}

// mprisTrackSignature identifies a TRACK-worthy change of a player.
func mprisTrackSignature(st mprisPlayerState) string {
	return fmt.Sprintf("%c|%s|%s|%s|%s", stateOf(st),
		st.source, st.title, st.artist, st.album)
}

// mprisTrackMessage builds the raw TRACK message for a simulated provider.
// The art argument is always empty: MPRIS art is not globally accessible.
func mprisTrackMessage(id string, st mprisPlayerState) []byte {
	state := byte(stateStopped)
	if st.playing {
		state = statePlaying
	}
	fields := []string{msgTypeTrack, id, string(state), st.source,
		st.title, st.artist, st.album, ""}
	return []byte(strings.Join(fields, string(rsByte)))
}

// mprisPosMessage builds the raw POS message for a simulated provider.
func mprisPosMessage(id string, st mprisPlayerState) []byte {
	length := ""
	if st.hasLength {
		length = strconv.FormatFloat(st.length, 'f', 1, 64)
	}
	fields := []string{msgTypePos, id,
		strconv.FormatFloat(st.pos, 'f', 1, 64), length}
	return []byte(strings.Join(fields, string(rsByte)))
}

// mprisCapsMessage builds the raw CAPABILITIES message. ART is deliberately
// not advertised: MPRIS artwork is a local file URI, not globally
// accessible.
func mprisCapsMessage(id string) []byte {
	fields := []string{msgTypeCapabilities, id, "PLAY", "PAUSE", "NEXT", "PREV", "SEEK"}
	return []byte(strings.Join(fields, string(rsByte)))
}

// stateOf maps a sampled state to the MMCP playback state.
func stateOf(st mprisPlayerState) byte {
	if st.playing {
		return statePlaying
	}
	return stateStopped
}

// announce sends CAPABILITIES (once), TRACK (on change), and POS (while
// playing) for one MPRIS player.
func (s *mprisSim) announce(suffix string, st mprisPlayerState) {
	id := s.instanceID(suffix)

	if !s.announcedCaps[id] {
		s.send(mprisCapsMessage(id))
		s.announcedCaps[id] = true
	}

	sig := mprisTrackSignature(st)
	if s.lastAnnounced[id] != sig {
		s.send(mprisTrackMessage(id, st))
		s.lastAnnounced[id] = sig
	}

	if st.playing {
		s.send(mprisPosMessage(id, st))
	}
}

// forgetUnseen drops all announce state for players that disappeared,
// guaranteeing that a re-appearing player is re-announced from scratch.
func (s *mprisSim) forgetUnseen(seen map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.lastAnnounced {
		if !idBelongsToSeen(s, id, seen) {
			delete(s.lastAnnounced, id)
			delete(s.announcedCaps, id)
		}
	}
}

// idBelongsToSeen reports whether an instance ID belongs to one of the
// currently seen MPRIS players.
func idBelongsToSeen(s *mprisSim, id string, seen map[string]bool) bool {
	for suffix := range seen {
		if s.ids[suffix] == id {
			return true
		}
	}
	return false
}

// instanceID returns a stable 8-character instance ID for an MPRIS player
// suffix, creating one on first use.
func (s *mprisSim) instanceID(suffix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.ids[suffix]; ok {
		return id
	}
	id := randomInstanceID()
	s.ids[suffix] = id
	return id
}

// ensureConn dials the relay if not connected. It reports whether a usable
// connection exists.
func (s *mprisSim) ensureConn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return true
	}
	url := fmt.Sprintf("ws://127.0.0.1:%d", s.relay.Port())
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return false
	}
	s.conn = conn
	return true
}

// send writes one raw message to the relay, or drops it on error.
func (s *mprisSim) send(data []byte) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		// Connection died: drop it so ensureConn can re-dial.
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.mu.Unlock()
	}
}

// readLoop consumes relay messages and performs CONTROL commands on the
// underlying MPRIS players.
func (s *mprisSim) readLoop(stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn == nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			// Connection died: drop it so ensureConn can re-dial.
			s.mu.Lock()
			if s.conn == conn {
				s.conn = nil
			}
			s.mu.Unlock()
			continue
		}

		s.handleRelayMessage(data)
	}
}

// handleRelayMessage executes CONTROL commands addressed to the simulated
// providers.
func (s *mprisSim) handleRelayMessage(data []byte) {
	msg, ok := parseMessage(data)
	if !ok || msg.typ != msgTypeControl {
		return
	}

	id := msg.args[0]
	command := msg.args[1]
	var arg string
	if len(msg.args) > 2 {
		arg = msg.args[2]
	}

	suffix, ok := s.suffixFor(id)
	if !ok {
		return // not addressed to this simulated provider
	}
	s.execute(suffix, id, command, arg)
}

// suffixFor maps one of the simulator's instance IDs back to its MPRIS
// player suffix.
func (s *mprisSim) suffixFor(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for suffix, sid := range s.ids {
		if sid == id {
			return suffix, true
		}
	}
	return "", false
}

// execute performs an MMCP CONTROL command on the underlying MPRIS player.
func (s *mprisSim) execute(suffix string, id, command, arg string) {
	bus, err := dbus.SessionBus()
	if err != nil {
		return
	}
	name := mprisNamePrefix + suffix
	obj := bus.Object(name, mprisPath)

	switch command {
	case "PLAY":
		obj.Call(mprisIface+".Play", 0)
	case "PAUSE":
		obj.Call(mprisIface+".Pause", 0)
	case "NEXT":
		obj.Call(mprisIface+".Next", 0)
	case "PREV":
		obj.Call(mprisIface+".Previous", 0)
	case "SEEK":
		// SEEK carries the target position in seconds; MPRIS wants
		// microseconds on the current track.
		pos, err := strconv.ParseFloat(arg, 64)
		if err != nil || pos < 0 {
			return
		}
		trackID := s.currentTrackID(bus, name)
		if trackID == "" {
			return
		}
		obj.Call(mprisIface+".SetPosition", 0, dbus.ObjectPath(trackID), int64(pos*1e6))
	}
	_ = id
}

// currentTrackID reads the current mpris:trackid of a player.
func (s *mprisSim) currentTrackID(bus *dbus.Conn, name string) dbus.ObjectPath {
	obj := bus.Object(name, mprisPath)
	metaVar, err := obj.GetProperty(mprisIface + ".Metadata")
	if err != nil {
		return ""
	}
	meta, ok := metaVar.Value().(map[string]dbus.Variant)
	if !ok {
		return ""
	}
	if v, ok := meta["mpris:trackid"]; ok {
		switch id := v.Value().(type) {
		case dbus.ObjectPath:
			return id
		case string:
			return dbus.ObjectPath(id)
		}
	}
	return ""
}
