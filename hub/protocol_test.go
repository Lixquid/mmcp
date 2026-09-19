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
			name: "pos without length",
			raw:  "1/POS\x1ea7Kx92Qm\x1e37.4\x1e",
			typ:  "1/POS",
			args: []string{"a7Kx92Qm", "37.4", ""},
		},
		{
			name: "capabilities",
			raw:  "1/CAPABILITIES\x1ea7Kx92Qm\x1ePLAY\x1ePAUSE",
			typ:  "1/CAPABILITIES",
			args: []string{"a7Kx92Qm", "PLAY", "PAUSE"},
		},
		{
			name: "control with argument",
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
	for _, id := range []string{"", "a7Kx92Q", "a7Kx92Qmm", "a7Kx92Q-", "a7Kx92Q "} {
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
