package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeMeta(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Rick Astley", "rick astley"},
		{"  Rick   Astley ", "rick astley"},
		{"Au/Ra", "au ra"},
		{"E•MOTION", "e motion"},
		{"Céline Dion", "celine dion"},
		{"Beyoncé!", "beyonce"},
		{"AC/DC", "ac dc"},
		{"X  &  Y (Deluxe)", "x y deluxe"},
		{"ＴＯＫＹＯ", "ｔｏｋｙｏ"}, // full-width letters fold in width only
		{"", ""},
		{"   ", ""},
		{"!!!", ""},
	}
	for _, c := range cases {
		if got := normalizeMeta(c.in); got != c.want {
			t.Errorf("normalizeMeta(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseTrackArt(t *testing.T) {
	// Valid, with art.
	msg, ok := parseMessage([]byte("1/TRACK.ART\x1ea7Kx92Qm\x1ehttps://x/y.jpg"))
	if !ok || msg.typ != msgTypeTrackArt || msg.args[0] != "a7Kx92Qm" || msg.args[1] != "https://x/y.jpg" {
		t.Fatalf("parse art message failed: %+v ok=%v", msg, ok)
	}

	// Valid: empty art (trailing separator is preserved).
	msg, ok = parseMessage([]byte("1/TRACK.ART\x1ea7Kx92Qm\x1e"))
	if !ok || msg.args[1] != "" {
		t.Fatalf("parse empty-art message failed: %+v ok=%v", msg, ok)
	}

	// Invalid lengths.
	if _, ok := parseMessage([]byte("1/TRACK.ART\x1ea7Kx92Qm")); ok {
		t.Error("expected rejection of message with missing art field")
	}
	if _, ok := parseMessage([]byte("1/TRACK.ART\x1ea7Kx92Qm\x1eurl\x1eextra")); ok {
		t.Error("expected rejection of message with extra field")
	}
}

func TestApplyTrackArt(t *testing.T) {
	s := newControllerState()

	// TRACK.ART before any TRACK: ignored.
	s.handle(message{typ: msgTypeTrackArt, args: []string{"a7Kx92Qm", "https://x/y.jpg"}})

	// Announce a track without art, then update it.
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "Song", "Artist", "Album", "",
	}})
	s.handle(message{typ: msgTypeTrackArt, args: []string{"a7Kx92Qm", "https://x/y.jpg"}})

	s.mu.RLock()
	got := s.providers["a7Kx92Qm"].track.art
	s.mu.RUnlock()
	if got != "https://x/y.jpg" {
		t.Fatalf("art not applied: %q", got)
	}

	// A new TRACK replaces the art (TRACK is authoritative).
	s.handle(message{typ: msgTypeTrack, args: []string{
		"a7Kx92Qm", "P", "YTM", "Song", "Artist", "Album", "",
	}})
	s.mu.RLock()
	got = s.providers["a7Kx92Qm"].track.art
	s.mu.RUnlock()
	if got != "" {
		t.Fatalf("expected art reset by new TRACK, got %q", got)
	}

	// Unknown instance or track-less provider is ignored.
	s.handle(message{typ: msgTypeTrackArt, args: []string{"zzzzzzzz", "https://x/z.jpg"}})
}

func TestEncodeTrackArt(t *testing.T) {
	data, ok := encodeTrackArt("a7Kx92Qm", "https://x/y.jpg")
	if !ok || string(data) != "1/TRACK.ART\x1ea7Kx92Qm\x1ehttps://x/y.jpg" {
		t.Fatalf("unexpected encoding: %q ok=%v", data, ok)
	}
	if _, ok := encodeTrackArt("a7Kx92Qm", "bad\x1ers"); ok {
		t.Fatal("expected failure for field containing RS")
	}
}

// newTestResolver builds a resolver wired to a fake MusicBrainz server and a
// recorded send function.
type fakeMB struct {
	mu       sync.Mutex
	requests []string
	resp     string
	status   int
}

func (f *fakeMB) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.RawQuery)
	f.mu.Unlock()
	if f.status != 0 {
		http.Error(w, "boom", f.status)
		return
	}
	_, _ = w.Write([]byte(f.resp))
}

func (f *fakeMB) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// msgRecorder collects TRACK.ART deliveries from the resolver, safely for
// concurrent use.
type msgRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *msgRecorder) send(data []byte) {
	r.mu.Lock()
	r.msgs = append(r.msgs, string(data))
	r.mu.Unlock()
}

func (r *msgRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

func (r *msgRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

// newFakeServer builds a resolver backed by a fake MusicBrainz server.
func newFakeServer(t *testing.T, fake *fakeMB) (*ArtResolver, *[]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	ar := newArtResolver(srvRelay())
	ar.base = srv.URL + "/ws/2"
	ar.minInterval = 0
	ar.cacheDir = t.TempDir()
	ar.httpTimeout = 2 * time.Second
	return ar, nil
}

// srvRelay is a minimal relay stand-in for resolver tests.
func srvRelay() *Relay { return newRelay(0, newMessageLog(10)) }

// mbResult builds a MusicBrainz release-group search response.
func mbResult(groups ...map[string]any) string {
	list := make([]map[string]any, 0, len(groups))
	list = append(list, groups...)
	b, _ := json.Marshal(map[string]any{"release-groups": list})
	return string(b)
}

func rg(id, title, artist, primaryType string) map[string]any {
	return map[string]any{
		"id":                 id,
		"title":              title,
		"primary-type":       primaryType,
		"first-release-date": "2020-01-01",
		"artist-credit": []map[string]any{
			{"name": artist, "artist": map[string]any{"name": artist}},
		},
	}
}

func waitSent(t *testing.T, sent *msgRecorder, want int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := sent.all(); len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sends; got %v", want, sent.all())
	return nil
}

func TestResolverResolvesAndCaches(t *testing.T) {
	const mbid = "4b58a770-aa5e-4f60-8c3a-8e6a23456789"
	fake := &fakeMB{resp: mbResult(
		map[string]any{"id": mbid, "title": "Whenever You Need Somebody",
			"primary-type": "Album", "artist-credit": []map[string]any{
				{"name": "Rick Astley", "artist": map[string]any{"name": "Rick Astley"}},
			}},
	)}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetSend(sent.send)

	track := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eNever Gonna Give You Up\x1eRick Astley\x1eWhenever You Need Somebody\x1e"
	ar.HandleMessage([]byte(track))

	got := waitSent(t, sent, 1)
	want := "1/TRACK.ART\x1ea7Kx92Qm\x1ehttps://coverartarchive.org/release-group/" + mbid + "/front-500"
	if got[0] != want {
		t.Fatalf("got %q, want %q", got[0], want)
	}

	// The query must have used artist and album, not the title.
	fake.mu.Lock()
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0], "Rick+Astley") ||
		!strings.Contains(fake.requests[0], "Whenever+You+Need+Somebody") ||
		strings.Contains(fake.requests[0], "Never+Gonna") {
		t.Fatalf("unexpected MB query: %q", fake.requests)
	}
	fake.mu.Unlock()

	// A second identical TRACK must be served from cache: no new HTTP
	// request, message sent again.
	ar.HandleMessage([]byte(track))
	got = waitSent(t, sent, 2)
	if got[1] != want {
		t.Fatalf("cache-hit resend mismatch: %q", got[1])
	}
	if n := fake.hitCount(); n != 1 {
		t.Fatalf("expected 1 MB request total, got %d", n)
	}

	// The disk cache holds the documented fields.
	files, _ := filepath.Glob(filepath.Join(ar.cacheDir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected 1 cache file, got %d", len(files))
	}
	raw, _ := os.ReadFile(files[0])
	var entry artEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("cache file is not valid JSON: %v", err)
	}
	if entry.MusicbrainzReleaseGroupID != mbid || entry.ArtworkURL == "" ||
		entry.Artist != "Rick Astley" || entry.Album != "Whenever You Need Somebody" ||
		entry.ResolvedAt == "" {
		t.Fatalf("unexpected cache entry: %+v", entry)
	}
}

func TestResolverSkipsWhenArtPresent(t *testing.T) {
	fake := &fakeMB{}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)

	track := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eArtist\x1eAlbum\x1ehttps://already/has.jpg"
	ar.HandleMessage([]byte(track))

	time.Sleep(200 * time.Millisecond)
	if sent.count() != 0 || fake.hitCount() != 0 {
		t.Fatalf("expected no lookup and no send, got %v / %d hits", sent.all(), fake.hitCount())
	}
}

func TestResolverDisabled(t *testing.T) {
	fake := &fakeMB{resp: mbResult(rg("55555555-5555-4555-8555-555555555555", "Album", "Artist", "Album"))}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetEnabled(false)

	track := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eArtist\x1eAlbum\x1e"
	ar.HandleMessage([]byte(track))
	time.Sleep(200 * time.Millisecond)
	if sent.count() != 0 || fake.hitCount() != 0 {
		t.Fatalf("disabled resolver must do nothing, got %v / %d hits", sent.all(), fake.hitCount())
	}

	// Enabling mid-run lets lookups proceed.
	ar.SetEnabled(true)
	ar.HandleMessage([]byte(track))
	waitSent(t, sent, 1)
}

func TestResolverNegativeCache(t *testing.T) {
	fake := &fakeMB{resp: mbResult()}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetSend(sent.send)

	track := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eNobody\x1eNothing\x1e"
	ar.HandleMessage([]byte(track))
	time.Sleep(150 * time.Millisecond)
	ar.HandleMessage([]byte(track))
	time.Sleep(150 * time.Millisecond)

	if sent.count() != 0 {
		t.Fatalf("expected no sends, got %v", sent.all())
	}
	if n := fake.hitCount(); n != 1 {
		t.Fatalf("expected negative result to be cached (1 request), got %d", n)
	}
}

func TestResolverPicksBestMatch(t *testing.T) {
	// First result is a live collection by a different artist; second is
	// the exact studio album. The resolver must not blindly take the
	// first.
	good := map[string]any{
		"id": "11111111-1111-4111-8111-111111111111", "title": "The Album",
		"primary-type": "Album", "artist-credit": []map[string]any{
			{"name": "Some Band", "artist": map[string]any{"name": "Some Band"}},
		},
	}
	better := map[string]any{
		"id": "22222222-2222-4222-8222-222222222222", "title": "The Album",
		"primary-type": "Album", "first-release-date": "1999-01-01",
		"artist-credit": []map[string]any{
			{"name": "Want Ed Artist", "artist": map[string]any{"name": "Want Ed Artist"}},
		},
	}
	fake := &fakeMB{resp: mbResult(good, better)}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetSend(sent.send)

	ar.HandleMessage([]byte("1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eWant Ed Artist\x1eThe Album\x1e"))
	got := waitSent(t, sent, 1)
	if !strings.HasSuffix(got[0], "/release-group/22222222-2222-4222-8222-222222222222/front-500") {
		t.Fatalf("expected best match to win, got %q", got[0])
	}
}

func TestResolverUnconvincingMatchRejected(t *testing.T) {
	// A release group whose title and artist share nothing with the
	// query must be rejected even if it is the only result.
	fake := &fakeMB{resp: mbResult(map[string]any{
		"id": "33333333-3333-4333-8333-333333333333", "title": "Completely Different",
		"primary-type": "Album", "artist-credit": []map[string]any{
			{"name": "Other People", "artist": map[string]any{"name": "Other People"}},
		},
	})}
	ar, _ := newFakeServer(t, fake)

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetSend(sent.send)

	ar.HandleMessage([]byte("1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eSome Artist\x1eSome Album\x1e"))
	time.Sleep(200 * time.Millisecond)
	if sent.count() != 0 {
		t.Fatalf("unconvincing match must not be sent: %v", sent.all())
	}
}

func TestResolverDiacriticsMatch(t *testing.T) {
	// Metadata with diacritics/punctuation must hit the same cache entry
	// as its normalized form.
	fake := &fakeMB{}
	ar, _ := newFakeServer(t, fake)

	// Prime the cache with the plain form via a direct write.
	ar.writeCache(cacheKey("Beyoncé", "I Am... Sasha Fierce"), artEntry{
		Artist: "Beyoncé", Album: "I Am... Sasha Fierce",
		MusicbrainzReleaseGroupID: "44444444-4444-4444-8444-444444444444",
		ArtworkURL:                "https://coverartarchive.org/release-group/44444444-4444-4444-8444-444444444444/front-500",
		ResolvedAt:                time.Now().UTC().Format(time.RFC3339),
	})

	sent := &msgRecorder{}
	ar.SetSend(sent.send)
	ar.SetSend(sent.send)

	// Slightly different rendering of the same names.
	ar.HandleMessage([]byte("1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eBeyoncé \x1eI Am… Sasha Fierce\x1e"))
	got := waitSent(t, sent, 1)
	if !strings.HasSuffix(got[0], "/front-500") {
		t.Fatalf("expected cache hit across normalized forms, got %q", got[0])
	}
	if n := fake.hitCount(); n != 0 {
		t.Fatalf("expected no network traffic, got %d", n)
	}
}
