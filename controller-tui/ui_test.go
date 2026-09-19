package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
)

// TestViewRenders ensures every view state renders without panicking.
func TestViewRenders(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*model)
		contains []string
	}{
		{
			name:     "no providers",
			contains: []string{"MMCP Controller", "No providers known yet"},
		},
		{
			name: "playing provider",
			setup: func(m *model) {
				m.state.connected = true
				m.state.handle(message{typ: msgTypeTrack, args: []string{
					"a7Kx92Qm", "P", "YTM", "Never Gonna Give You Up", "Rick Astley",
					"Whenever You Need Somebody", "https://example.invalid/art.jpg",
				}})
				m.state.handle(message{typ: msgTypePos, args: []string{"a7Kx92Qm", "37.4", "213.0"}})
				m.state.handle(message{typ: msgTypeCapabilities, args: []string{
					"a7Kx92Qm", "PLAY", "PAUSE", "NEXT", "PREV", "SEEK", "ART",
				}})
			},
			contains: []string{
				"MMCP Controller", "● connected", "PLAYING",
				"Never Gonna Give You Up", "Rick Astley",
				"YTM", "a7Kx92Qm", "PLAY", "Providers (1)",
			},
		},
		{
			name: "selected paused provider",
			setup: func(m *model) {
				m.state.handle(message{typ: msgTypeTrack, args: []string{
					"q4Mn8Z2x", "S", "SOUNDCLOUD", "Song B", "Artist B", "Album B", "",
				}})
				m.state.handle(message{typ: msgTypePos, args: []string{"q4Mn8Z2x", "12", "180"}})
				m.state.handle(message{typ: msgTypeCapabilities, args: []string{
					"q4Mn8Z2x", "PLAY", "PAUSE", "NEXT",
				}})
				// Backdate the POS sample so staleness is visible.
				m.state.providers["q4Mn8Z2x"].pos.received = time.Now().Add(-11 * time.Second)
				m.selected = "q4Mn8Z2x"
			},
			contains: []string{
				"Selected provider", "PAUSED/STOPPED", "SOUNDCLOUD", "Song B",
				"PLAY", "NEXT", "(stale)", "0:12 / 3:00",
			},
		},
		{
			name: "long artwork URL is never truncated",
			setup: func(m *model) {
				m.state.connected = true
				art := "https://example.invalid/" + strings.Repeat("x", 200) + "/cover.jpg"
				m.state.handle(message{typ: msgTypeTrack, args: []string{
					"cccccccc", "P", "YTM", "Song C", "", "", art,
				}})
			},
			contains: []string{
				"Artwork",
				"https://example.invalid/" + strings.Repeat("x", 200) + "/cover.jpg",
			},
		},
		{
			name: "multiple providers with long titles",
			setup: func(m *model) {
				m.state.connected = false
				m.state.lastErr = "connect: connection refused"
				m.state.handle(message{typ: msgTypeTrack, args: []string{
					"aaaaaaaa", "P", "YTM", strings.Repeat("Long Title ", 20), "", "", "",
				}})
				m.state.handle(message{typ: msgTypeTrack, args: []string{
					"bbbbbbbb", "S", "X", "", "", "", "",
				}})
			},
			contains: []string{"● disconnected", "Providers (2)", "connection refused"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model{
				state: newControllerState(),
				ws:    newWSClient("ws://localhost:9994"),
			}
			m.width, m.height = 100, 30
			if tc.setup != nil {
				tc.setup(&m)
			}

			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			view := updated.(model).View()
			if view == "" {
				t.Fatal("empty view")
			}
			for _, want := range tc.contains {
				if !strings.Contains(view, want) {
					t.Errorf("view does not contain %q:\n%s", want, view)
				}
			}
		})
	}
}

// TestInputViewRenders covers the go-to-timestamp overlay.
func TestInputViewRenders(t *testing.T) {
	m := model{
		state:       newControllerState(),
		ws:          newWSClient("ws://localhost:9994"),
		width:       100,
		height:      30,
		inputActive: true,
		input:       "1:2",
	}
	view := m.View()
	for _, want := range []string{"Go to Timestamp", "1:2█", "seconds, MM:SS, HH:MM:SS"} {
		if !strings.Contains(view, want) {
			t.Errorf("view does not contain %q:\n%s", want, view)
		}
	}
}
