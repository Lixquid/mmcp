package main

import (
	"crypto/rand"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version 1 message types. The version prefix is part of the type.
const (
	msgTypeTrack        = "1/TRACK"
	msgTypeTrackArt     = "1/TRACK.ART"
	msgTypePos          = "1/POS"
	msgTypeCapabilities = "1/CAPABILITIES"
	msgTypeControl      = "1/CONTROL"
)

// Playback states carried by TRACK.
const (
	statePlaying = 'P'
	stateStopped = 'S'
)

// message is a parsed MMCP message. typ is the full message type
// (e.g. "1/TRACK") and args are the remaining RS-separated fields.
type message struct {
	typ  string
	args []string
}

// parseMessage parses a raw relay payload. Unknown or malformed messages
// are rejected; rejection must never cause a disconnect.
func parseMessage(data []byte) (message, bool) {
	if !utf8.Valid(data) {
		return message{}, false
	}

	fields := strings.Split(string(data), string(rsByte))
	if len(fields) == 0 || fields[0] == "" {
		return message{}, false
	}

	switch fields[0] {
	case msgTypeTrack:
		// id, state, source, track, artist, album, art
		if len(fields) != 8 {
			return message{}, false
		}
	case msgTypeTrackArt:
		// id, art (art may be empty)
		if len(fields) != 3 {
			return message{}, false
		}
	case msgTypePos:
		// id, position, length (length may be empty)
		if len(fields) != 4 {
			return message{}, false
		}
	case msgTypeCapabilities:
		// id, capability... (at least one capability)
		if len(fields) < 3 {
			return message{}, false
		}
	case msgTypeControl:
		// id, command[, argument...]
		if len(fields) < 3 {
			return message{}, false
		}
	default:
		return message{}, false
	}

	return message{typ: fields[0], args: fields[1:]}, true
}

// encodeControl encodes a CONTROL message. It fails if any field would
// contain RS, since the protocol defines no escaping.
func encodeControl(id, command string, args ...string) ([]byte, bool) {
	fields := append([]string{msgTypeControl, id, command}, args...)
	for _, field := range fields {
		if strings.ContainsRune(field, rsByte) {
			return nil, false
		}
	}
	return []byte(strings.Join(fields, string(rsByte))), true
}

// encodeTrackArt encodes a TRACK.ART message (Asynchronous Album Art
// extension). It fails if any field would contain RS.
func encodeTrackArt(id, art string) ([]byte, bool) {
	for _, field := range []string{id, art} {
		if strings.ContainsRune(field, rsByte) {
			return nil, false
		}
	}
	return []byte(strings.Join([]string{msgTypeTrackArt, id, art}, string(rsByte))), true
}

// randomInstanceID returns 8 random lowercase alphanumeric characters.
// It is shared by the built-in simulated providers (MPRIS, SMTC).
func randomInstanceID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	n := big.NewInt(int64(len(chars)))
	for i := range b {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			b[i] = 'm'
			continue
		}
		b[i] = chars[v.Int64()]
	}
	return string(b)
}

// validInstanceID reports whether id is exactly eight ASCII alphanumeric
// characters. The reserved broadcast ID "*" is handled separately.
func validInstanceID(id string) bool {
	if len(id) != 8 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// validCapability reports whether cap is a syntactically valid capability
// name: at least one ASCII alphanumeric character, optionally with '-',
// '_', or '/'.
func validCapability(cap string) bool {
	if cap == "" {
		return false
	}
	alnum := false
	for i := 0; i < len(cap); i++ {
		c := cap[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			alnum = true
		case c == '-' || c == '_' || c == '/':
		default:
			return false
		}
	}
	return alnum
}

// parseSeconds parses a non-negative decimal number of seconds. Negative
// values, NaN, and Infinity are invalid.
func parseSeconds(value string) (float64, bool) {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	return n, true
}
