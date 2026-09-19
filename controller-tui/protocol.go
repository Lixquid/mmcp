package main

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MMCP message framing: fields are separated by ASCII Record Separator
// (RS, U+001E). There is no trailing RS and no escaping mechanism.
const rsByte = '\x1e'

// Version 1 message types. The version prefix is part of the type.
const (
	msgTypeTrack        = "1/TRACK"
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

// parseMessage parses a raw WebSocket text payload. Per the specification,
// unknown or malformed messages are rejected; rejection must never by itself
// cause a disconnection.
func parseMessage(data []byte) (message, bool) {
	if !utf8.Valid(data) {
		return message{}, false
	}

	fields := strings.Split(string(data), string(rsByte))
	if len(fields) == 0 || fields[0] == "" {
		return message{}, false
	}

	// Validate argument counts. Arguments may be empty, so empty trailing
	// fields are legitimate (e.g. TRACK with no artwork).
	switch fields[0] {
	case msgTypeTrack:
		// id, state, source, track, artist, album, art
		if len(fields) != 8 {
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
		// Unknown type or incompatible version: silently ignore.
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
// '_', or '/'. Unknown but well-formed capabilities are kept by callers but
// not acted upon.
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

// parseTimestamp parses a user-entered position in plain decimal seconds
// ("92.5"), MM:SS, or HH:MM:SS form. Minutes may exceed 59 for long media.
func parseTimestamp(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	parts := strings.Split(value, ":")
	if len(parts) > 3 {
		return 0, false
	}

	values := make([]float64, len(parts))
	for i, part := range parts {
		if part == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return 0, false
		}
		values[i] = n
	}

	switch len(values) {
	case 1:
		return values[0], true
	case 2:
		if values[1] >= 60 {
			return 0, false
		}
		return values[0]*60 + values[1], true
	default:
		if values[1] >= 60 || values[2] >= 60 {
			return 0, false
		}
		return values[0]*3600 + values[1]*60 + values[2], true
	}
}
