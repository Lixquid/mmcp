package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestArtResolverEndToEnd runs the full pipeline against the real relay: a
// WebSocket provider announces a TRACK with an empty art argument, the
// resolver queries a fake MusicBrainz server, and the provider receives the
// hub's TRACK.ART message through the relay. A TRACK that already carries art
// must not trigger any lookup.
func TestArtResolverEndToEnd(t *testing.T) {
	const mbid = "8e2b4d2a-1111-4c2e-9d5e-3c1f2a4b5c6d"
	var hitsMu sync.Mutex
	mbHits := 0
	mbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsMu.Lock()
		mbHits++
		hitsMu.Unlock()
		if strings.Contains(r.URL.RawQuery, "Rick+Astley") {
			_, _ = w.Write([]byte(mbResult(
				map[string]any{"id": mbid, "title": "Whenever You Need Somebody",
					"primary-type": "Album", "artist-credit": []map[string]any{
						{"name": "Rick Astley", "artist": map[string]any{"name": "Rick Astley"}},
					}},
			)))
			return
		}
		_, _ = w.Write([]byte(mbResult()))
	}))
	defer mbSrv.Close()

	relay := newRelay(19731, newMessageLog(100))
	if err := relay.Start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}

	art := newArtResolver(relay)
	art.base = mbSrv.URL + "/ws/2"
	art.minInterval = 0
	art.cacheDir = t.TempDir()
	relay.SetOnMessage(art.HandleMessage)

	// Register the hub's own local client so SendFromLocal works.
	relay.AttachLocal(func([]byte) {})
	art.SetSend(func(data []byte) {
		// The hub broadcasts resolved art and applies it to its own view.
		relay.SendFromLocal(data)
	})

	// Connect a provider.
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:19731", nil)
	if err != nil {
		t.Fatalf("provider dial: %v", err)
	}
	defer conn.Close()

	readMsg := func() string {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("provider read: %v", err)
		}
		return string(data)
	}

	// TRACK with no art: the resolver must answer with TRACK.ART.
	_ = conn.WriteMessage(websocket.TextMessage,
		[]byte("1/TRACK\x1eQn1Q56dd\x1eP\x1eSOUNDCLOUD\x1eNever Gonna Give You Up\x1eRick Astley\x1eWhenever You Need Somebody\x1e"))

	got := readMsg()
	want := "1/TRACK.ART\x1eQn1Q56dd\x1ehttps://coverartarchive.org/release-group/" + mbid + "/front-500"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Repeat announcement: served from cache, same answer.
	_ = conn.WriteMessage(websocket.TextMessage,
		[]byte("1/TRACK\x1eQn1Q56dd\x1eP\x1eSOUNDCLOUD\x1eNever Gonna Give You Up\x1eRick Astley\x1eWhenever You Need Somebody\x1e"))
	if got := readMsg(); got != want {
		t.Fatalf("cache-hit resend mismatch: %q", got)
	}
	hitsMu.Lock()
	if mbHits != 1 {
		hitsMu.Unlock()
		t.Fatalf("expected 1 MusicBrainz request, got %d", mbHits)
	}
	hitsMu.Unlock()

	// TRACK that already has art: no lookup, no message.
	_ = conn.WriteMessage(websocket.TextMessage,
		[]byte("1/TRACK\x1eQn1Q56dd\x1eP\x1eSOUNDCLOUD\x1eTogether Forever\x1eRick Astley\x1eWhenever You Need Somebody\x1ehttps://example.invalid/own.jpg"))
	time.Sleep(300 * time.Millisecond)
	hitsMu.Lock()
	defer hitsMu.Unlock()
	if mbHits != 1 {
		t.Fatalf("TRACK with art must not trigger a lookup (hits=%d)", mbHits)
	}
}
