package main

// The SMTC simulator is the Windows counterpart of the MPRIS simulator: it
// presents every Windows System Media Transport Controls session as its own
// MMCP Provider on the relay. It announces itself with CAPABILITIES, emits
// TRACK/POS derived from the sessions' state, and performs CONTROL commands
// on the underlying media sessions.
//
// This file is platform neutral: it contains the simulator core, which is
// testable everywhere. The platform backend that actually talks to SMTC
// lives in smtc_windows.go (a PowerShell helper); other platforms get a
// stub in smtc_other.go.
//
// Album art is deliberately not supported: SMTC exposes artwork as a local
// thumbnail stream reference, which is not globally accessible to other
// MMCP peers, so the simulated provider never advertises the ART capability
// and its TRACK art argument is always empty.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// smtcReconnect is how long the backend waits before respawning the SMTC
// helper after it dies.
const smtcReconnect = 2 * time.Second

// smtcSnapshot is one SMTC session sampled by the helper.
type smtcSnapshot struct {
	ID     string  `json:"id"`     // SMTC session id (stable GUID)
	App    string  `json:"app"`    // SourceAppUserModelId
	Status string  `json:"status"` // e.g. Playing, Paused
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Album  string  `json:"album"`
	Pos    float64 `json:"pos"`
	Len    float64 `json:"len"`
}

// smtcTick is one batch of snapshots, emitted once per second.
type smtcTick struct {
	T        string         `json:"t"`
	Sessions []smtcSnapshot `json:"sessions"`
}

// smtcBackend supplies session snapshots and performs control commands on
// the underlying system media sessions.
type smtcBackend interface {
	// run emits tick batches via emit until stop is closed. It must
	// return promptly once stop is closed. Backend deaths (e.g. the
	// helper process crashing) are the backend's problem: it retries
	// internally until stop is closed.
	run(stop <-chan struct{}, emit func(smtcTick))
	// execute performs an MMCP CONTROL command on the session.
	execute(sessionID, command, arg string)
}

// smtcPlayerState is one SMTC session, in MMCP terms.
type smtcPlayerState struct {
	source    string // e.g. SPOTIFY, ZUNEMUSIC
	playing   bool
	title     string
	artist    string
	album     string
	pos       float64
	length    float64
	hasLength bool
}

// smtcSim is the simulated provider backed by system SMTC sessions.
type smtcSim struct {
	relay   *Relay
	backend smtcBackend

	mu      sync.Mutex
	running bool
	stop    chan struct{}
	conn    *websocket.Conn

	// lastAnnounced holds the TRACK signature sent per instance ID, so a
	// TRACK is only emitted when something changes.
	lastAnnounced map[string]string

	// announcedCaps records whether CAPABILITIES was sent per instance ID.
	announcedCaps map[string]bool

	// ids maps an SMTC session id to a stable MMCP instance ID for as long
	// as the simulator stays enabled.
	ids map[string]string
}

// newSMTCSimCore wires a simulated SMTC provider to the given backend.
func newSMTCSimCore(relay *Relay, backend smtcBackend) *smtcSim {
	return &smtcSim{
		relay:         relay,
		backend:       backend,
		stop:          make(chan struct{}),
		lastAnnounced: make(map[string]string),
		announcedCaps: make(map[string]bool),
		ids:           make(map[string]string),
	}
}

// Start begins consuming SMTC snapshots and impersonating a provider on the
// relay.
func (s *smtcSim) Start() {
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

// Stop ceases all SMTC monitoring and disconnects from the relay.
func (s *smtcSim) Stop() {
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
	s.ids = make(map[string]string)
}

// run is the simulator's main loop: the backend produces snapshots while
// the read loop handles CONTROL messages addressed to the simulated
// instances.
func (s *smtcSim) run(stop chan struct{}) {
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		s.readLoop(stop)
	}()
	s.backend.run(stop, s.onTick)
	<-readerDone
}

// onTick processes one batch of SMTC snapshots: announce every session and
// drop announce state for sessions that disappeared.
func (s *smtcSim) onTick(tick smtcTick) {
	if !s.ensureConn() {
		return
	}

	seen := make(map[string]bool)
	for _, snap := range tick.Sessions {
		st, ok := s.stateFromSnapshot(snap)
		if !ok {
			continue
		}
		id := s.instanceID(snap.ID)
		seen[id] = true
		s.announce(id, st)
	}
	s.forgetUnseen(seen)
}

// stateFromSnapshot converts an SMTC snapshot into MMCP terms. It reports
// false for unusable snapshots.
func (s *smtcSim) stateFromSnapshot(snap smtcSnapshot) (smtcPlayerState, bool) {
	if snap.ID == "" {
		return smtcPlayerState{}, false
	}
	return smtcPlayerState{
		source:    smtcSource(snap.App),
		playing:   snap.Status == "Playing",
		title:     snap.Title,
		artist:    snap.Artist,
		album:     snap.Album,
		pos:       snap.Pos,
		length:    snap.Len,
		hasLength: snap.Len > 0,
	}, true
}

// smtcSource derives the MMCP source label from an SMTC
// SourceAppUserModelId: "Spotify.SPOTIFY" becomes "SPOTIFY", and
// "Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic" becomes
// "ZUNEMUSIC" (the publisher hash is stripped first).
func smtcSource(aumid string) string {
	if i := strings.IndexByte(aumid, '_'); i >= 0 {
		aumid = aumid[:i]
	}
	if i := strings.IndexByte(aumid, '!'); i >= 0 {
		aumid = aumid[:i]
	}
	if i := strings.LastIndexByte(aumid, '.'); i >= 0 {
		aumid = aumid[i+1:]
	}
	return strings.ToUpper(aumid)
}

// smtcTrackSignature identifies a TRACK-worthy change of a session.
func smtcTrackSignature(st smtcPlayerState) string {
	state := byte(stateStopped)
	if st.playing {
		state = statePlaying
	}
	return fmt.Sprintf("%c|%s|%s|%s|%s", state,
		st.source, st.title, st.artist, st.album)
}

// smtcTrackMessage builds the raw TRACK message for a simulated provider.
// The art argument is always empty: SMTC art is not globally accessible.
func smtcTrackMessage(id string, st smtcPlayerState) []byte {
	state := byte(stateStopped)
	if st.playing {
		state = statePlaying
	}
	fields := []string{msgTypeTrack, id, string(state), st.source,
		st.title, st.artist, st.album, ""}
	return []byte(strings.Join(fields, string(rsByte)))
}

// smtcPosMessage builds the raw POS message for a simulated provider.
func smtcPosMessage(id string, st smtcPlayerState) []byte {
	length := ""
	if st.hasLength {
		length = strconv.FormatFloat(st.length, 'f', 1, 64)
	}
	fields := []string{msgTypePos, id,
		strconv.FormatFloat(st.pos, 'f', 1, 64), length}
	return []byte(strings.Join(fields, string(rsByte)))
}

// smtcCapsMessage builds the raw CAPABILITIES message. ART is deliberately
// not advertised: SMTC artwork is a local thumbnail, not globally
// accessible.
func smtcCapsMessage(id string) []byte {
	fields := []string{msgTypeCapabilities, id, "PLAY", "PAUSE", "NEXT", "PREV", "SEEK"}
	return []byte(strings.Join(fields, string(rsByte)))
}

// announce sends CAPABILITIES (once), TRACK (on change), and POS (while
// playing) for one SMTC session.
func (s *smtcSim) announce(id string, st smtcPlayerState) {
	if !s.announcedCaps[id] {
		s.send(smtcCapsMessage(id))
		s.announcedCaps[id] = true
	}

	sig := smtcTrackSignature(st)
	if s.lastAnnounced[id] != sig {
		s.send(smtcTrackMessage(id, st))
		s.lastAnnounced[id] = sig
	}

	if st.playing {
		s.send(smtcPosMessage(id, st))
	}
}

// forgetUnseen drops all announce state for sessions that disappeared,
// guaranteeing that a re-appearing session is re-announced from scratch.
func (s *smtcSim) forgetUnseen(seen map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.lastAnnounced {
		if !seen[id] {
			delete(s.lastAnnounced, id)
			delete(s.announcedCaps, id)
			delete(s.ids, instanceKeyFor(s, id))
		}
	}
}

// instanceKeyFor finds the SMTC session id an MMCP instance id belongs to.
func instanceKeyFor(s *smtcSim, id string) string {
	for key, iid := range s.ids {
		if iid == id {
			return key
		}
	}
	return ""
}

// instanceID returns a stable instance ID for an SMTC session, creating one
// on first use. The IDs are the same random 8-character lowercase
// alphanumeric strings the MPRIS simulator uses.
func (s *smtcSim) instanceID(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.ids[sessionID]; ok {
		return id
	}
	id := randomInstanceID()
	s.ids[sessionID] = id
	return id
}

// ensureConn dials the relay if not connected. It reports whether a usable
// connection exists.
func (s *smtcSim) ensureConn() bool {
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
func (s *smtcSim) send(data []byte) {
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
// underlying SMTC sessions.
func (s *smtcSim) readLoop(stop chan struct{}) {
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
func (s *smtcSim) handleRelayMessage(data []byte) {
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

	sessionID, ok := s.sessionFor(id)
	if !ok {
		return // not addressed to this simulated provider
	}
	s.backend.execute(sessionID, command, arg)
}

// sessionFor maps one of the simulator's instance IDs back to its SMTC
// session id.
func (s *smtcSim) sessionFor(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID, iid := range s.ids {
		if iid == id {
			return sessionID, true
		}
	}
	return "", false
}

// smtcCommand is a control command sent to the SMTC helper on stdin.
type smtcCommand struct {
	C   string `json:"c"`             // PLAY / PAUSE / NEXT / PREV / SEEK
	Sid string `json:"sid"`           // SMTC session id
	Arg string `json:"arg,omitempty"` // SEEK target in seconds
}

// smtcCommandJSON encodes a control command for the helper.
func smtcCommandJSON(sessionID, command, arg string) []byte {
	data, _ := json.Marshal(smtcCommand{C: command, Sid: sessionID, Arg: arg})
	return data
}

// parseSMTCTick decodes one JSON line from the helper.
func parseSMTCTick(line []byte) (smtcTick, bool) {
	var tick smtcTick
	if err := json.Unmarshal(line, &tick); err != nil || tick.T != "tick" {
		return smtcTick{}, false
	}
	return tick, true
}
