package main

import (
	"testing"
	"time"
)

func mustTrackMsg(t *testing.T, raw string) message {
	t.Helper()
	msg, ok := parseMessage([]byte(raw))
	if !ok {
		t.Fatalf("bad test message: %q", raw)
	}
	return msg
}

func TestApplyTrack(t *testing.T) {
	s := newControllerState()
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "Song A", "Artist A", "Album A", "https://art",
	}})

	p, ok := s.providers["a7Kx92Qm"]
	if !ok {
		t.Fatal("provider not created")
	}
	if p.state != statePlaying {
		t.Errorf("state = %q, want P", p.state)
	}
	if s.playing != "a7Kx92Qm" {
		t.Errorf("playing = %q", s.playing)
	}
	if p.track.title != "Song A" || p.track.artist != "Artist A" ||
		p.track.album != "Album A" || p.track.source != "YTM" ||
		p.track.art != "https://art" {
		t.Errorf("track = %+v", p.track)
	}

	// Pause.
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "S", "YTM", "Song A", "Artist A", "Album A", "",
	}})
	if p.state != stateStopped {
		t.Errorf("state = %q, want S", p.state)
	}
	if s.playing != "" {
		t.Errorf("playing should be cleared, got %q", s.playing)
	}

	// Unknown state must be ignored entirely.
	prevTrack := p.track
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "X", "YTM", "Other", "", "", "",
	}})
	if p.state != stateStopped {
		t.Errorf("unknown state changed state to %q", p.state)
	}
	if p.track != prevTrack {
		t.Error("unknown state must not replace track metadata")
	}

	// Invalid instance ID must be ignored.
	s.handle(message{typ: msgTypeTrack, args: []string{
		"bad!", "P", "YTM", "Song", "", "", "",
	}})
	if _, ok := s.providers["bad!"]; ok {
		t.Error("invalid instance ID should not create a provider")
	}
}

func TestApplyPosSupersedesAndDoesNotChangeState(t *testing.T) {
	s := newControllerState()
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "S", "YTM", "Song", "Artist", "Album", "",
	}})

	s.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", "10", "100"}})
	s.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", "20.5", "100"}})

	p := s.providers["a7Kx92Qm"]
	if p.pos == nil || p.pos.position != 20.5 {
		t.Fatalf("pos = %+v, want position 20.5", p.pos)
	}
	if !p.pos.hasLength || p.pos.length != 100 {
		t.Errorf("length = %v hasLength = %v", p.pos.length, p.pos.hasLength)
	}
	// POS must not establish playback state.
	if p.state != stateStopped {
		t.Errorf("POS changed state to %q", p.state)
	}

	// POS without length.
	s.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", "30", ""}})
	if p.pos.hasLength {
		t.Error("empty length should set hasLength = false")
	}

	// Malformed POS values must be dropped.
	for _, bad := range []struct{ pos, length string }{
		{"-1", "100"}, {"NaN", "100"}, {"Inf", "100"}, {"x", "100"},
		{"1", "-5"}, {"1", "NaN"},
	} {
		before := p.pos
		s.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", bad.pos, bad.length}})
		if p.pos != before {
			t.Errorf("POS %v/%v should have been ignored", bad.pos, bad.length)
		}
	}

	// POS for an unknown-but-valid instance is still recorded as telemetry.
	s.handle(message{typ: msgTypePos, args: []string{"q4Mn8Z2x", "5", "60"}})
	if _, ok := s.providers["q4Mn8Z2x"]; !ok {
		t.Error("expected telemetry-only provider entry")
	}
}

func TestApplyCapabilitiesReplacesSet(t *testing.T) {
	s := newControllerState()
	s.handle(message{typ: msgTypeCapabilities, args: []string{
		"a7Kx92Qm", "PLAY", "PAUSE", "NEXT", "PREV", "SEEK", "ART",
	}})
	p := s.providers["a7Kx92Qm"]
	if len(p.caps) != 6 {
		t.Fatalf("caps = %v", p.caps)
	}

	// Latest announcement replaces the previous set entirely.
	s.handle(message{typ: msgTypeCapabilities, args: []string{"a7Kx92Qm", "PLAY", "PAUSE"}})
	if len(p.caps) != 2 || p.caps[0] != "PAUSE" || p.caps[1] != "PLAY" {
		t.Fatalf("caps after replacement = %v", p.caps)
	}

	// Invalid capabilities are dropped, not fatal.
	s.handle(message{typ: msgTypeCapabilities, args: []string{"a7Kx92Qm", "PLAY", "BAD CAP", "NEXT"}})
	if len(p.caps) != 2 || p.caps[0] != "NEXT" || p.caps[1] != "PLAY" {
		t.Fatalf("caps = %v", p.caps)
	}
}

func TestDisplayPositionInterpolation(t *testing.T) {
	v := providerView{
		ID:    "a7Kx92Qm",
		State: statePlaying,
		Pos: &posInfo{
			position:  10,
			length:    100,
			hasLength: true,
			received:  time.Now().Add(-2 * time.Second),
		},
	}

	position, length, hasLength := v.displayPosition()
	if position < 11.9 || position > 12.5 {
		t.Errorf("interpolated position = %v, want ~12", position)
	}
	if length != 100 || !hasLength {
		t.Errorf("length = %v, hasLength = %v", length, hasLength)
	}

	// Never reports beyond the track length.
	v.Pos = &posInfo{position: 99.5, length: 100, hasLength: true, received: time.Now().Add(-5 * time.Second)}
	position, _, _ = v.displayPosition()
	if position != 100 {
		t.Errorf("position = %v, want clamped to 100", position)
	}

	// Not playing: no interpolation.
	v.State = stateStopped
	v.Pos = &posInfo{position: 10, received: time.Now().Add(-5 * time.Second)}
	position, _, hasLength = v.displayPosition()
	if position != 10 || hasLength {
		t.Errorf("position = %v hasLength = %v, want 10 false", position, hasLength)
	}

	// No POS at all.
	v.Pos = nil
	if _, _, hasLength := v.displayPosition(); hasLength {
		t.Error("hasLength should be false without POS")
	}
}

func TestLatestPlayingWins(t *testing.T) {
	s := newControllerState()
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "A", "", "", "",
	}})
	s.handle(message{typ: msgTypeTrack, args: []string{
		"q4Mn8Z2x", "P", "SOUNDCLOUD", "B", "", "", "",
	}})
	if s.playing != "q4Mn8Z2x" {
		t.Errorf("playing = %q, want latest P announcement", s.playing)
	}
	// A announcing S again must not clear q4Mn8Z2x's playing status.
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "S", "YTM", "A", "", "", "",
	}})
	if s.playing != "q4Mn8Z2x" {
		t.Errorf("playing = %q, want q4Mn8Z2x", s.playing)
	}
}

func TestProviderOrdering(t *testing.T) {
	s := newControllerState()
	for _, id := range []string{"zzzzzzzz", "aaaa1111", "mmmm2222"} {
		s.handle(message{typ: msgTypeCapabilities, args: []string{id, "PLAY"}})
	}
	ids := s.ids()
	if len(ids) != 3 || ids[0] != "aaaa1111" || ids[1] != "mmmm2222" || ids[2] != "zzzzzzzz" {
		t.Errorf("ids = %v", ids)
	}
}

func TestReset(t *testing.T) {
	s := newControllerState()
	s.connected = true
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "Song", "", "", "",
	}})
	s.handle(message{typ: msgTypeTrack, args: []string{
		"q4Mn8Z2x", "S", "X", "Other", "", "", "",
	}})

	s.reset()

	if len(s.providers) != 0 {
		t.Errorf("providers not cleared: %v", s.providers)
	}
	if s.playing != "" {
		t.Errorf("playing not cleared: %q", s.playing)
	}

	// State remains usable afterwards.
	s.handle(message{typ: msgTypeTrack, args: []string{
		"mmmm3333", "P", "YTM", "New", "", "", "",
	}})
	if s.playing != "mmmm3333" {
		t.Errorf("playing = %q after reset + TRACK", s.playing)
	}
}

func TestViewSnapshot(t *testing.T) {
	s := newControllerState()
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "Song", "Artist", "Album", "art",
	}})
	s.handle(message{typ: msgTypeCapabilities, args: []string{"a7Kx92Qm", "PLAY", "SEEK"}})
	s.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", "5", ""}})

	v, ok := s.view("a7Kx92Qm")
	if !ok {
		t.Fatal("expected view")
	}
	if v.ID != "a7Kx92Qm" || v.State != statePlaying || v.Track == nil ||
		v.Track.title != "Song" || len(v.Caps) != 2 || v.Pos == nil || v.Pos.hasLength {
		t.Fatalf("view = %+v", v)
	}

	if _, ok := s.view("missing1"); ok {
		t.Error("unknown instance should not have a view")
	}
}
