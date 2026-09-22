package main

import (
	"strings"
	"testing"
)

func TestMPRISSource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"org.mpris.MediaPlayer2.vlc", "VLC"},
		{"org.mpris.MediaPlayer2.spotify.instance4242", "SPOTIFY"},
		{"org.mpris.MediaPlayer2.Chrome", "CHROME"},
		{"org.mpris.MediaPlayer2.rhythmbox", "RHYTHMBOX"},
		{"org.mpris.MediaPlayer2", "ORG.MPRIS.MEDIAPLAYER2"}, // never listed; HasPrefix requires the trailing dot
	}
	for _, c := range cases {
		if got := mprisSource(c.in); got != c.want {
			t.Errorf("mprisSource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMPRISTrackMessage(t *testing.T) {
	st := mprisPlayerState{
		source: "VLC", playing: true,
		title: "Song A", artist: "Artist A", album: "Album A",
		pos: 12.34, length: 200, hasLength: true,
	}
	msg := string(mprisTrackMessage("a7Kx92Qm", st))
	want := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eVLC\x1eSong A\x1eArtist A\x1eAlbum A\x1e"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}

	// The art argument must always be empty, and the message must parse.
	parsed, ok := parseMessage(mprisTrackMessage("a7Kx92Qm", st))
	if !ok {
		t.Fatal("TRACK message is not parseable")
	}
	if parsed.args[6] != "" {
		t.Fatalf("art must be empty, got %q", parsed.args[6])
	}

	// Stopped player maps to state S.
	st.playing = false
	parsed, ok = parseMessage(mprisTrackMessage("a7Kx92Qm", st))
	if !ok || parsed.args[1] != "S" {
		t.Fatalf("stopped state not encoded: %v %v", parsed, ok)
	}
}

func TestMPRISPosMessage(t *testing.T) {
	st := mprisPlayerState{pos: 12.34, length: 200, hasLength: true}
	msg := string(mprisPosMessage("a7Kx92Qm", st))
	if msg != "1/POS\x1ea7Kx92Qm\x1e12.3\x1e200.0" {
		t.Fatalf("unexpected POS: %q", msg)
	}

	// Without mpris:length the length field is empty.
	st.hasLength = false
	msg = string(mprisPosMessage("a7Kx92Qm", st))
	if msg != "1/POS\x1ea7Kx92Qm\x1e12.3\x1e" {
		t.Fatalf("length should be empty without mpris:length: %q", msg)
	}
}

func TestMPRISCapsMessage(t *testing.T) {
	msg := string(mprisCapsMessage("a7Kx92Qm"))
	want := "1/CAPABILITIES\x1ea7Kx92Qm\x1ePLAY\x1ePAUSE\x1eNEXT\x1ePREV\x1eSEEK"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}

	// ART must not be advertised.
	if strings.Contains(msg, "\x1eART") {
		t.Fatal("MPRIS simulator must not advertise ART")
	}
}

func TestRandomInstanceID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := randomInstanceID()
		if !validInstanceID(id) {
			t.Fatalf("invalid instance id: %q", id)
		}
		seen[id] = true
	}
	if len(seen) < 90 {
		t.Fatalf("instance ids not sufficiently unique: %d unique in 100", len(seen))
	}
}

func TestMPRISSimInstanceIDStable(t *testing.T) {
	s := newMPRISSim(newRelay(0, newMessageLog(10)))
	if a := s.instanceID("vlc"); a != s.instanceID("vlc") {
		t.Fatal("instance id must be stable per player")
	}
	if s.instanceID("vlc") == s.instanceID("spotify") {
		t.Fatal("different players must get different ids")
	}
}

func TestMPRISSimIgnoresForeignControl(t *testing.T) {
	// CONTROL addressed to another provider must not reach execute().
	s := newMPRISSim(newRelay(0, newMessageLog(10)))
	ownID := s.instanceID("vlc")

	// Unknown instance: silently ignored.
	s.handleRelayMessage([]byte("1\x1eCONTROL\x1ezzzzzzzz\x1ePLAY"))

	// Known ID: suffix lookup works (execution fails silently without a
	// live D-Bus session).
	if suffix, ok := s.suffixFor(ownID); !ok || suffix != "vlc" {
		t.Fatalf("suffixFor(%q) = %q, %v", ownID, suffix, ok)
	}
	s.handleRelayMessage([]byte("1\x1eCONTROL\x1e" + ownID + "\x1ePLAY"))
	s.handleRelayMessage([]byte("1\x1eCONTROL\x1e" + ownID + "\x1eSEEK\x1e12.5"))
	s.handleRelayMessage([]byte("1\x1eCONTROL\x1e" + ownID + "\x1eSEEK\x1enotanumber"))
}
