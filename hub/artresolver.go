package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The Asynchronous Album Art extension (SPEC.md 5.1) lets a Provider update
// the current track's artwork out-of-band with TRACK.ART. The hub's art
// resolver extends this to providers that never supply artwork: when a TRACK
// announcement carries an empty art argument, the resolver looks the track's
// artist and album up in the MusicBrainz database, builds a Cover Art Archive
// URL from the release-group MBID, caches the result on disk (keyed by
// normalized artist+album so repeat lookups never hit the network), and
// broadcasts a TRACK.ART message through the relay. A TRACK that already has
// art is left untouched.

const (
	// defaultMBBase is the MusicBrainz Web Service root.
	defaultMBBase = "https://musicbrainz.org/ws/2"

	// defaultMBInterval is the minimum spacing between MusicBrainz
	// requests. The public API allows roughly one request per second.
	defaultMBInterval = 1100 * time.Millisecond

	// artURLSize is the Cover Art Archive front-cover size served.
	artURLSize = "front-500"

	// negativeTTL is how long a "not found" result is remembered before
	// the lookup is retried. Positive results are cached forever.
	negativeTTL = 7 * 24 * time.Hour
)

// mbidPattern matches a MusicBrainz UUID.
var mbidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// userAgent identifies the hub to MusicBrainz, per their usage policy. It
// includes the embedded application version.
var userAgent = "MMCP-Hub/" + appVersion() + " (https://github.com/mmcp/mmcp)"

// artEntry is one cached resolver result, stored as JSON on disk.
type artEntry struct {
	Artist                    string `json:"artist"`
	Album                     string `json:"album"`
	MusicbrainzReleaseGroupID string `json:"musicbrainzReleaseGroupId"`
	ArtworkURL                string `json:"artworkUrl"`
	ResolvedAt                string `json:"resolvedAt"`
}

// ArtResolver watches relay traffic and resolves missing album art.
type ArtResolver struct {
	relay *Relay

	// send delivers a TRACK.ART message: broadcast through the relay, and
	// deliver to the hub's own controller state (the relay does not echo
	// local messages back).
	send func(data []byte)

	// base is the MusicBrainz API root; overridden in tests.
	base string

	// minInterval is the minimum spacing between MusicBrainz requests.
	minInterval time.Duration

	// cacheDir stores the disk cache; empty disables the disk cache.
	cacheDir string

	// httpTimeout bounds each MusicBrainz request.
	httpTimeout time.Duration

	mu       sync.Mutex
	enabled  bool
	lastMB   time.Time
	inflight map[string]bool
	memCache map[string]artEntry

	client *http.Client
}

// newArtResolver creates a resolver for the given relay. Disk cache lives
// under the user cache directory; if that is unavailable the resolver works
// memory-only.
func newArtResolver(relay *Relay) *ArtResolver {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = ""
	} else {
		dir = dir + string(os.PathSeparator) + "mmcp-hub" + string(os.PathSeparator) + "artcache"
	}

	return &ArtResolver{
		relay:       relay,
		base:        defaultMBBase,
		minInterval: defaultMBInterval,
		cacheDir:    dir,
		httpTimeout: 10 * time.Second,
		enabled:     true,
		inflight:    make(map[string]bool),
		memCache:    make(map[string]artEntry),
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// SetEnabled turns artwork resolution on or off. When off, TRACK messages
// are ignored and in-flight lookups discard their results.
func (ar *ArtResolver) SetEnabled(on bool) {
	ar.mu.Lock()
	ar.enabled = on
	ar.mu.Unlock()
}

// Enabled reports whether artwork resolution is on.
func (ar *ArtResolver) Enabled() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.enabled
}

// SetSend installs the delivery hook for resolved TRACK.ART messages.
func (ar *ArtResolver) SetSend(send func([]byte)) {
	ar.send = send
}

// HandleMessage inspects one raw relay message. It is safe for concurrent
// use and never blocks the relay for long: network work happens in a
// goroutine.
func (ar *ArtResolver) HandleMessage(data []byte) {
	if !ar.Enabled() {
		return
	}

	msg, ok := parseMessage(data)
	if !ok || msg.typ != msgTypeTrack {
		return
	}

	// args: id, state, source, track, artist, album, art
	id, artist, album, art := msg.args[0], msg.args[4], msg.args[5], msg.args[6]

	// A provider-provided art argument wins; do nothing.
	if art != "" || artist == "" || album == "" || !utf8.ValidString(artist) {
		return
	}

	key := cacheKey(artist, album)
	if key == "/" {
		return
	}

	// Cache hit: send immediately, without touching the network.
	if entry, ok := ar.lookup(key); ok {
		if entry.ArtworkURL != "" {
			ar.sendArt(id, entry.ArtworkURL)
		}
		return
	}

	// Otherwise resolve asynchronously; never block the relay.
	go ar.resolve(key, artist, album, id)
}

// cacheKey maps raw artist/album metadata to the normalized lookup key.
func cacheKey(artist, album string) string {
	return normalizeMeta(artist) + "/" + normalizeMeta(album)
}

// lookup consults the in-memory and disk caches. ok is true for both
// positive hits (entry.ArtworkURL != "") and cached negatives.
func (ar *ArtResolver) lookup(key string) (artEntry, bool) {
	ar.mu.Lock()
	entry, ok := ar.memCache[key]
	ar.mu.Unlock()
	if ok {
		return entry, true
	}

	entry, ok = ar.readCache(key)
	if !ok {
		return artEntry{}, false
	}

	ar.mu.Lock()
	ar.memCache[key] = entry
	ar.mu.Unlock()
	return entry, true
}

// readCache loads one entry from the disk cache. Expired negative entries
// are treated as a miss so the lookup is retried later.
func (ar *ArtResolver) readCache(key string) (artEntry, bool) {
	if ar.cacheDir == "" {
		return artEntry{}, false
	}

	data, err := os.ReadFile(ar.cachePath(key))
	if err != nil {
		return artEntry{}, false
	}

	var entry artEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return artEntry{}, false
	}

	if entry.ArtworkURL == "" {
		// Negative entry: honor it only while fresh.
		resolved, err := time.Parse(time.RFC3339, entry.ResolvedAt)
		if err != nil || time.Since(resolved) > negativeTTL {
			return artEntry{}, false
		}
	}
	return entry, true
}

// writeCache stores one entry on disk, keyed by the normalized lookup key.
func (ar *ArtResolver) writeCache(key string, entry artEntry) {
	if ar.cacheDir == "" {
		return
	}
	if err := os.MkdirAll(ar.cacheDir, 0o755); err != nil {
		return
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = os.WriteFile(ar.cachePath(key), data, 0o644)

	ar.mu.Lock()
	ar.memCache[key] = entry
	ar.mu.Unlock()
}

// cachePath maps a normalized lookup key to a filename-safe path.
func (ar *ArtResolver) cachePath(key string) string {
	sum := sha1.Sum([]byte(key))
	return ar.cacheDir + string(os.PathSeparator) + hex.EncodeToString(sum[:]) + ".json"
}

// resolve queries MusicBrainz for the release group matching artist+album,
// caches the outcome, and broadcasts TRACK.ART on success. Runs in its own
// goroutine.
func (ar *ArtResolver) resolve(key, artist, album, instanceID string) {
	ar.mu.Lock()
	if ar.inflight[key] {
		ar.mu.Unlock()
		return
	}
	ar.inflight[key] = true
	ar.mu.Unlock()
	defer func() {
		ar.mu.Lock()
		delete(ar.inflight, key)
		ar.mu.Unlock()
	}()

	mbid, artworkURL := ar.resolveReleaseGroup(artist, album)

	// Cache the outcome, positive or negative, so repeat TRACK
	// announcements never re-query the network.
	ar.writeCache(key, artEntry{
		Artist:                    artist,
		Album:                     album,
		MusicbrainzReleaseGroupID: mbid,
		ArtworkURL:                artworkURL,
		ResolvedAt:                time.Now().UTC().Format(time.RFC3339),
	})

	// Respect the enable switch: it may have flipped while we were
	// querying.
	if !ar.Enabled() {
		return
	}
	if artworkURL != "" {
		ar.sendArt(instanceID, artworkURL)
	}
}

// resolveReleaseGroup performs the (rate-limited) MusicBrainz release-group
// search and returns the MBID plus the built Cover Art Archive URL. On no
// convincing match both return values are "".
func (ar *ArtResolver) resolveReleaseGroup(artist, album string) (string, string) {
	mbid := ar.searchReleaseGroup(artist, album)
	if mbid == "" {
		return "", ""
	}
	return mbid, "https://coverartarchive.org/release-group/" + mbid + "/" + artURLSize
}

// searchReleaseGroup queries MusicBrainz and picks the best-matching release
// group for the given artist and album. It returns "" when nothing matches
// convincingly.
func (ar *ArtResolver) searchReleaseGroup(artist, album string) string {
	query := fmt.Sprintf("artistname:%q AND releasegroup:%q", artist, album)
	endpoint := fmt.Sprintf("%s/release-group?query=%s&fmt=json&limit=5",
		ar.base, url.QueryEscape(query))

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	// MusicBrainz allows roughly one request per second; serialize all
	// requests behind the interval gate.
	ar.rateLimit()

	resp, err := ar.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		ReleaseGroups []struct {
			ID               string   `json:"id"`
			Title            string   `json:"title"`
			PrimaryType      string   `json:"primary-type"`
			FirstReleaseDate string   `json:"first-release-date"`
			SecondaryTypes   []string `json:"secondary-types"`
			ArtistCredit     []struct {
				Name   string `json:"name"`
				Artist struct {
					Name string `json:"name"`
				} `json:"artist"`
			} `json:"artist-credit"`
		} `json:"release-groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}

	artistNorm := normalizeMeta(artist)
	albumNorm := normalizeMeta(album)

	bestScore := 0
	bestID := ""
	for _, rg := range result.ReleaseGroups {
		if !mbidPattern.MatchString(rg.ID) {
			continue
		}

		score := 0
		for _, credit := range rg.ArtistCredit {
			name := credit.Name
			if name == "" {
				name = credit.Artist.Name
			}
			score = maxInt(score, matchScore(artistNorm, normalizeMeta(name)))
		}
		titleScore := matchScore(albumNorm, normalizeMeta(rg.Title))
		if titleScore == 0 && len(rg.SecondaryTypes) > 0 {
			// Album titles sometimes carry the type, e.g. "X (EP)".
			for _, st := range rg.SecondaryTypes {
				trimmed := normalizeMeta(strings.TrimSuffix(rg.Title, " ("+st+")"))
				titleScore = maxInt(titleScore, matchScore(albumNorm, trimmed))
			}
		}
		score += titleScore

		if rg.PrimaryType == "Album" && len(rg.SecondaryTypes) == 0 {
			score += 10
		}

		if score > bestScore {
			bestScore = score
			bestID = rg.ID
		}
	}

	// Require a convincing match: exact artist or exact album plus some
	// corroborating signal. Below the threshold, treat as not found.
	if bestScore < 30 {
		return ""
	}
	return bestID
}

// rateLimit enforces the minimum spacing between MusicBrainz requests.
func (ar *ArtResolver) rateLimit() {
	for {
		ar.mu.Lock()
		wait := ar.minInterval - time.Since(ar.lastMB)
		if wait <= 0 {
			ar.lastMB = time.Now()
			ar.mu.Unlock()
			return
		}
		ar.mu.Unlock()
		time.Sleep(wait)
	}
}

// sendArt encodes and delivers one TRACK.ART message.
func (ar *ArtResolver) sendArt(instanceID, artworkURL string) {
	data, ok := encodeTrackArt(instanceID, artworkURL)
	if !ok {
		return
	}
	ar.mu.Lock()
	send := ar.send
	ar.mu.Unlock()
	if send != nil {
		send(data)
	}
}

// matchScore compares two normalized strings for metadata matching.
// Exact match scores 40, prefix/suffix match 20, containment 15.
func matchScore(want, got string) int {
	switch {
	case want == "" || got == "":
		return 0
	case want == got:
		return 40
	case strings.HasPrefix(got, want) || strings.HasSuffix(got, want),
		strings.HasPrefix(want, got) || strings.HasSuffix(want, got):
		return 20
	case strings.Contains(want, got) || strings.Contains(got, want):
		return 15
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// normalizeMeta reduces free-form artist/album metadata to a canonical form
// used for cache keys and matching: case-folded, diacritics stripped,
// punctuation removed, and whitespace collapsed to single spaces.
func normalizeMeta(s string) string {
	// Decompose so that diacritics become separate combining marks, which
	// are dropped below.
	decomposed := foldDiacritics(s)

	var b strings.Builder
	space := false
	for _, r := range decomposed {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteRune(' ')
			}
			space = false
			b.WriteRune(unicode.ToLower(r))
		default:
			space = true
		}
	}
	return b.String()
}

// foldDiacritics returns s decomposed (NFD) with combining marks removed,
// so "é" becomes "e". The x/text norm package handles the decomposition;
// this only strips the resulting marks.
func foldDiacritics(s string) string {
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
