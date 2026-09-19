package main

import (
	"testing"
)

func TestParseMessageValid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		typ  string
		args []string
	}{
		{
			name: "track with art",
			raw:  "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eSong\x1eArtist\x1eAlbum\x1ehttps://x/y.jpg",
			typ:  "1/TRACK",
			args: []string{"a7Kx92Qm", "P", "YTM", "Song", "Artist", "Album", "https://x/y.jpg"},
		},
		{
			name: "track with empty art",
			raw:  "1/TRACK\x1ea7Kx92Qm\x1eS\x1eYTM\x1eSong\x1eArtist\x1eAlbum\x1e",
			typ:  "1/TRACK",
			args: []string{"a7Kx92Qm", "S", "YTM", "Song", "Artist", "Album", ""},
		},
		{
			name: "track with empty middle fields",
			raw:  "1/TRACK\x1ea7Kx92Qm\x1eS\x1eYTM\x1e\x1e\x1e\x1e",
			typ:  "1/TRACK",
			args: []string{"a7Kx92Qm", "S", "YTM", "", "", "", ""},
		},
		{
			name: "pos with length",
			raw:  "1/POS\x1ea7Kx92Qm\x1e37.4\x1e213.0",
			typ:  "1/POS",
			args: []string{"a7Kx92Qm", "37.4", "213.0"},
		},
		{
			name: "pos without length",
			raw:  "1/POS\x1ea7Kx92Qm\x1e37.4\x1e",
			typ:  "1/POS",
			args: []string{"a7Kx92Qm", "37.4", ""},
		},
		{
			name: "capabilities",
			raw:  "1/CAPABILITIES\x1ea7Kx92Qm\x1ePLAY\x1ePAUSE\x1eSEEK",
			typ:  "1/CAPABILITIES",
			args: []string{"a7Kx92Qm", "PLAY", "PAUSE", "SEEK"},
		},
		{
			name: "control",
			raw:  "1/CONTROL\x1ea7Kx92Qm\x1eSEEK\x1e92.5",
			typ:  "1/CONTROL",
			args: []string{"a7Kx92Qm", "SEEK", "92.5"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := parseMessage([]byte(tc.raw))
			if !ok {
				t.Fatalf("expected valid message")
			}
			if msg.typ != tc.typ {
				t.Errorf("type = %q, want %q", msg.typ, tc.typ)
			}
			if len(msg.args) != len(tc.args) {
				t.Fatalf("args = %v, want %v", msg.args, tc.args)
			}
			for i := range tc.args {
				if msg.args[i] != tc.args[i] {
					t.Errorf("arg[%d] = %q, want %q", i, msg.args[i], tc.args[i])
				}
			}
		})
	}
}

func TestParseMessageInvalid(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"empty type":        "\x1earg",
		"unknown type":      "2/TRACK\x1eabc\x1eP",
		"track too few":     "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM",
		"track too many":    "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1et\x1ea\x1el\x1ez\x1eextra",
		"pos too few":       "1/POS\x1ea7Kx92Qm\x1e1.0",
		"pos too many":      "1/POS\x1ea7Kx92Qm\x1e1.0\x1e2.0\x1eextra",
		"caps one field":    "1/CAPABILITIES\x1ea7Kx92Qm",
		"control one field": "1/CONTROL\x1ea7Kx92Qm",
		"invalid utf8":      "1/POS\x1e\xff\xfe\x1e1\x1e2",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseMessage([]byte(raw)); ok {
				t.Errorf("expected %q to be rejected", raw)
			}
		})
	}
}

// RS occurring in source text is a provider bug: it changes the field
// count, so the TRACK is either rejected outright or parsed at face value —
// never a crash or disconnection.
func TestParseMessageTrackEmbeddedRS(t *testing.T) {
	// 9 fields: wrong count, rejected.
	raw := "1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eBad\x1eRS\x1eTitle\x1eAlbum\x1eextra"
	if _, ok := parseMessage([]byte(raw)); ok {
		t.Fatal("expected rejection of 9-field TRACK")
	}

	// 8 fields: parsed at face value with shifted fields, which is the
	// best available interpretation.
	msg, ok := parseMessage([]byte("1/TRACK\x1ea7Kx92Qm\x1eP\x1eYTM\x1eTi\x1etle\x1eA\x1eB"))
	if !ok {
		t.Fatal("8-field TRACK should still parse")
	}
	if msg.args[3] != "Ti" || msg.args[4] != "tle" {
		t.Errorf("args = %v", msg.args)
	}
}

func TestEncodeControl(t *testing.T) {
	data, ok := encodeControl("a7Kx92Qm", "SEEK", "92.5")
	if !ok {
		t.Fatal("expected ok")
	}
	if got, want := string(data), "1/CONTROL\x1ea7Kx92Qm\x1eSEEK\x1e92.5"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if _, ok := encodeControl("*", "INFO"); !ok {
		t.Error("broadcast INFO should encode")
	}

	if _, ok := encodeControl("a7Kx92Qm", "BAD\x1eCMD"); ok {
		t.Error("RS in a field must be rejected")
	}
}

func TestValidInstanceID(t *testing.T) {
	if !validInstanceID("a7Kx92Qm") {
		t.Error("expected valid")
	}
	for _, id := range []string{"", "a7Kx92Q", "a7Kx92Qmm", "a7Kx92Q-", "a7Kx92Q ", "Ａ２３４５６７８"} {
		if validInstanceID(id) {
			t.Errorf("%q should be invalid", id)
		}
	}
}

func TestValidCapability(t *testing.T) {
	for _, cap := range []string{"PLAY", "SEEK-x", "a_b", "a/b", "x"} {
		if !validCapability(cap) {
			t.Errorf("%q should be valid", cap)
		}
	}
	for _, cap := range []string{"", "-", "_", "/", "PL AY", "PLAY!"} {
		if validCapability(cap) {
			t.Errorf("%q should be invalid", cap)
		}
	}
}

func TestParseSeconds(t *testing.T) {
	for _, good := range []string{"0", "37.4", "1e2", "0.5"} {
		if _, ok := parseSeconds(good); !ok {
			t.Errorf("%q should parse", good)
		}
	}
	for _, bad := range []string{"", "-1", "NaN", "Inf", "-Inf", "Infinity", "abc"} {
		if _, ok := parseSeconds(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"92.5", 92.5, true},
		{"1:30", 90, true},
		{"1:00:05", 3605, true},
		{"0:59", 59, true},
		{"62:00", 3720, true}, // long media: minutes may exceed 59
		{"1:60", 0, false},
		{"1:2:60", 0, false},
		{"1:2:3:4", 0, false},
		{"", 0, false},
		{"-5", 0, false},
		{"abc", 0, false},
		{"1:", 0, false},
		{"NaN", 0, false},
	}

	for _, tc := range cases {
		got, ok := parseTimestamp(tc.in)
		if ok != tc.ok {
			t.Errorf("parseTimestamp(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseTimestamp(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
