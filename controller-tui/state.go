package main

import (
	"sort"
	"sync"
	"time"
)

// trackInfo holds the track metadata from the latest TRACK message.
type trackInfo struct {
	source string
	title  string
	artist string
	album  string
	art    string
}

// posInfo holds the latest POS telemetry for a provider. POS is telemetry
// only and never establishes or changes playback state. Newer samples
// supersede older ones.
type posInfo struct {
	position  float64
	length    float64
	hasLength bool
	received  time.Time
}

// provider is everything the controller knows about one provider instance.
// All state derives from TRACK (state + metadata), POS (position), and
// CAPABILITIES (available operations).
type provider struct {
	id    string
	state byte // 'P', 'S', or 0 until the first TRACK arrives
	track *trackInfo
	pos   *posInfo
	caps  []string // sorted set of advertised capabilities
}

// controllerState is the derived, eventually-consistent view of all
// providers. It is mutated from the Bubble Tea Update loop and read from
// the render goroutine, so access is guarded by an RWMutex.
type controllerState struct {
	mu sync.RWMutex

	providers map[string]*provider

	// playing is the instance that most recently announced TRACK with
	// state 'P'. Races can temporarily leave more than one provider
	// playing; the latest announcement wins.
	playing string

	connected bool
	lastErr   string
	msgCount  uint64
}

func newControllerState() *controllerState {
	return &controllerState{providers: make(map[string]*provider)}
}

func (s *controllerState) getProvider(id string) *provider {
	p, ok := s.providers[id]
	if !ok {
		p = &provider{id: id}
		s.providers[id] = p
	}
	return p
}

// applyTrack handles a valid 1/TRACK message. TRACK is the authoritative
// message for playback state and track metadata.
func (s *controllerState) applyTrack(args []string) {
	id := args[0]
	if !validInstanceID(id) {
		return
	}

	state := args[1]
	if state != string(statePlaying) && state != string(stateStopped) {
		// Unknown states must be ignored.
		return
	}

	p := s.getProvider(id)
	p.state = state[0]
	p.track = &trackInfo{
		source: args[2],
		title:  args[3],
		artist: args[4],
		album:  args[5],
		art:    args[6],
	}

	if p.state == statePlaying {
		s.playing = id
	} else if s.playing == id {
		s.playing = ""
	}
}

// applyPos handles a valid 1/POS message. It only records position
// telemetry; it never changes playback state.
func (s *controllerState) applyPos(args []string) {
	id := args[0]
	if !validInstanceID(id) {
		return
	}

	position, ok := parseSeconds(args[1])
	if !ok {
		return
	}

	var length float64
	hasLength := false
	if args[2] != "" {
		length, ok = parseSeconds(args[2])
		if !ok {
			return
		}
		hasLength = true
	}

	// Newer samples supersede older ones. Providers may emit POS while not
	// playing (e.g. in response to INFO); those are kept as telemetry too.
	s.getProvider(id).pos = &posInfo{
		position:  position,
		length:    length,
		hasLength: hasLength,
		received:  time.Now(),
	}
}

// applyCapabilities handles a valid 1/CAPABILITIES message. The latest
// announcement for an instance replaces its previous set entirely.
func (s *controllerState) applyCapabilities(args []string) {
	id := args[0]
	if !validInstanceID(id) {
		return
	}

	seen := make(map[string]bool)
	for _, cap := range args[1:] {
		if validCapability(cap) {
			seen[cap] = true
		}
	}

	caps := make([]string, 0, len(seen))
	for cap := range seen {
		caps = append(caps, cap)
	}
	sort.Strings(caps)

	s.getProvider(id).caps = caps
}

// handle applies a parsed message to the state. Unknown and malformed
// messages have already been filtered by parseMessage; CONTROL addressed to
// providers is not interesting to a controller and is ignored here.
func (s *controllerState) handle(msg message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgCount++

	switch msg.typ {
	case msgTypeTrack:
		s.applyTrack(msg.args)
	case msgTypePos:
		s.applyPos(msg.args)
	case msgTypeCapabilities:
		s.applyCapabilities(msg.args)
	}
}

// reset clears all known providers so a fresh discovery can rebuild the
// view. Used before sending a broadcast INFO.
func (s *controllerState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.providers = make(map[string]*provider)
	s.playing = ""
}

// ids returns the known instance IDs in sorted order.
func (s *controllerState) ids() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.providers))
	for id := range s.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// playingID returns the instance that most recently announced active
// playback, or "".
func (s *controllerState) playingID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.playing
}

// providerView is a consistent snapshot of a provider for rendering and
// command targeting. It is a copy, so it is safe to read while Update
// continues to mutate the underlying state.
type providerView struct {
	ID    string
	State byte // 'P', 'S', or 0
	Track *trackInfo
	Pos   *posInfo
	Caps  []string
}

// view returns a snapshot of a provider.
func (s *controllerState) view(id string) (providerView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.providers[id]
	if !ok {
		return providerView{}, false
	}

	v := providerView{ID: p.id, State: p.state, Caps: p.caps}
	if p.track != nil {
		track := *p.track
		v.Track = &track
	}
	if p.pos != nil {
		pos := *p.pos
		v.Pos = &pos
	}
	return v, true
}

// displayPosition returns the best available playback position for a
// provider: the last reported POS, advanced by elapsed wall-clock time when
// the provider is playing. Stale values are tolerated and never adjusted
// backwards.
func (v providerView) displayPosition() (position float64, length float64, hasLength bool) {
	if v.Pos == nil {
		return 0, 0, false
	}

	position = v.Pos.position
	if v.State == statePlaying {
		position += time.Since(v.Pos.received).Seconds()
	}

	if v.Pos.hasLength {
		length = v.Pos.length
		hasLength = true
		if length > 0 && position > length {
			position = length
		}
	}
	return position, length, hasLength
}
