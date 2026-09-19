package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette.
const (
	colorAccent  = "#7D56F4"
	colorPlaying = "#04B575"
	colorStopped = "#FF4672"
	colorPaused  = "#FFCC00"
	colorKey     = "#FF4FD8"
	colorDim     = "#777777"
	colorText    = "#FFFFFF"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color(colorAccent)).
			Padding(0, 2)

	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim))
	textStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	accentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorDim)).
			Width(13)

	keyStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorKey))
	keyDescSt = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim))

	boxStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#444444")).
			Padding(1, 2)
)

func (m model) View() string {
	if m.quitting {
		return ""
	}

	if m.inputActive {
		return m.renderInputView()
	}

	width := m.width
	if width < 60 {
		width = 60
	}

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderHeader(),
		"",
		m.renderDetail(),
		"",
		m.renderProviderList(width-6),
		"",
		m.renderFooter(width-6),
	)

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		boxStyle.Render(content),
	)
}

func (m model) renderHeader() string {
	var connection string
	if m.state.connected {
		connection = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(colorPlaying)).Render("● connected")
	} else {
		connection = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(colorStopped)).Render("● disconnected")
	}

	return lipgloss.JoinHorizontal(lipgloss.Center, titleStyle.Render("MMCP Controller"), "  ", connection)
}

// renderDetail shows the provider that commands will be addressed to.
func (m model) renderDetail() string {
	target, ok := m.target()

	var heading string
	if m.selected != "" {
		heading = accentStyle.Render("Selected provider")
	} else {
		heading = accentStyle.Render("Active player")
	}

	if !ok {
		body := dimStyle.Render("No providers known yet.\n" +
			"A broadcast INFO has been requested; waiting for responses...")
		return lipgloss.JoinVertical(lipgloss.Left, heading, "", body)
	}

	detail := []string{heading, ""}

	if target.Track != nil && target.Track.title != "" {
		detail = append(detail,
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText)).Render(truncate(target.Track.title, 60)),
		)
		sub := []string{}
		if target.Track.artist != "" {
			sub = append(sub, target.Track.artist)
		}
		if target.Track.album != "" {
			sub = append(sub, target.Track.album)
		}
		if len(sub) > 0 {
			detail = append(detail, lipgloss.NewStyle().Foreground(lipgloss.Color("#BBBBBB")).Render(truncate(strings.Join(sub, " — "), 60)))
		}
	} else {
		detail = append(detail, dimStyle.Render("(no track announced)"))
	}

	stateLabel, stateColor := "UNKNOWN", colorDim
	switch target.State {
	case statePlaying:
		stateLabel, stateColor = "▶ PLAYING", colorPlaying
	case stateStopped:
		stateLabel, stateColor = "⏸ PAUSED/STOPPED", colorPaused
	}

	meta := labelStyle.Render("State") +
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(stateColor)).Render(stateLabel)
	detail = append(detail, meta)

	source := "—"
	if target.Track != nil && target.Track.source != "" {
		source = target.Track.source
	}
	detail = append(detail,
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Source"), accentStyle.Render(source)),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Provider"), textStyle.Render(target.ID)),
	)

	if target.Pos != nil {
		position, length, hasLength := target.displayPosition()
		posText := formatDuration(position)
		if hasLength {
			posText += " / " + formatDuration(length)
		} else {
			posText += " / --:--"
		}

		posRow := lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Position"), posText)
		if time.Since(target.Pos.received) > posFreshness {
			posRow += "  " + dimStyle.Render("(stale)")
		}
		detail = append(detail, posRow)

		if hasLength && length > 0 {
			detail = append(detail,
				strings.Repeat(" ", 13)+renderProgress(position/length, 32))
		}
	}

	if len(target.Caps) > 0 {
		detail = append(detail,
			lipgloss.JoinHorizontal(lipgloss.Top,
				labelStyle.Render("Capabilities"),
				wrapWords(strings.Join(target.Caps, ", "), 54)),
		)
	}

	if target.Track != nil && target.Track.art != "" {
		// The artwork URL is never truncated, so it can always be read or
		// copied in full.
		detail = append(detail,
			lipgloss.JoinHorizontal(lipgloss.Top,
				labelStyle.Render("Artwork"), target.Track.art),
		)
	}

	return lipgloss.JoinVertical(lipgloss.Left, detail...)
}

func (m model) renderProviderList(width int) string {
	ids := m.state.ids()

	header := accentStyle.Render(fmt.Sprintf("Providers (%d)", len(ids)))
	if len(ids) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, header, dimStyle.Render("  none"))
	}

	followID := m.state.playingID()
	if m.selected != "" {
		followID = m.selected
	}

	rows := make([]string, 0, len(ids)+1)
	rows = append(rows, header)

	for _, id := range ids {
		p, ok := m.state.view(id)
		if !ok {
			continue
		}

		marker := " "
		if id == followID {
			marker = ">"
		}

		state := "?"
		stateStyle := dimStyle
		switch p.State {
		case statePlaying:
			state = "▶"
			stateStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPlaying))
		case stateStopped:
			state = "⏸"
			stateStyle = dimStyle
		}

		title := "(no track)"
		if p.Track != nil && p.Track.title != "" {
			title = p.Track.title
			if p.Track.artist != "" {
				title += " — " + p.Track.artist
			}
		}

		pos := ""
		if p.Pos != nil {
			position, length, hasLength := p.displayPosition()
			pos = formatDuration(position)
			if hasLength {
				pos += "/" + formatDuration(length)
			}
			if time.Since(p.Pos.received) > posFreshness {
				pos += "*"
			}
		}

		line := fmt.Sprintf(" %s %s %s  %-10s %s",
			marker,
			stateStyle.Render(state),
			dimStyle.Render(id),
			truncate(strings.ToUpper(p.trackSource()), 10),
			truncate(title, maxInt(20, width-32)),
		)
		if pos != "" {
			line += "  " + dimStyle.Render(pos)
		}

		if id == m.selected {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}

		rows = append(rows, line)
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m model) renderFooter(width int) string {
	status := dimStyle.Render("Ready")
	switch {
	case m.state.lastErr != "" && !m.state.connected:
		status = lipgloss.NewStyle().Foreground(lipgloss.Color(colorStopped)).Render(truncate(m.state.lastErr, width))
	case m.status != "":
		status = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAAAAA")).Render(truncate(m.status, width))
	}

	keybind := func(key, desc string) string {
		return keyStyle.Render(key) + " " + keyDescSt.Render(desc)
	}
	sep := keyDescSt.Render(" · ")

	help := lipgloss.JoinHorizontal(
		lipgloss.Bottom,
		keybind("SPACE", "play/pause"), sep,
		keybind("P", "play"), sep,
		keybind("S", "pause"), sep,
		keybind("N/B", "next/prev"), sep,
		keybind("←/→", "±5s"), sep,
		keybind("G", "goto"), sep,
		keybind("TAB", "select"), sep,
		keybind("I", "discover"), sep,
		keybind("R", "refresh"), sep,
		keybind("Q", "quit"),
	)

	return lipgloss.JoinVertical(lipgloss.Left, status, help)
}

// -----------------------------------------------------------------------------
// View helpers
// -----------------------------------------------------------------------------

const posFreshness = 10 * time.Second

// trackSource safely returns the source type ("" when no TRACK received).
func (v providerView) trackSource() string {
	if v.Track == nil {
		return ""
	}
	return v.Track.source
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m model) renderInputView() string {
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render("Go to Timestamp"),
		"",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText)).Render("Position"),
		keyStyle.Render(m.input+"█"),
		"",
		dimStyle.Render("Enter to seek · Esc to cancel"),
		dimStyle.Render("Formats: seconds, MM:SS, HH:MM:SS"),
	)

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		boxStyle.Render(content),
	)
}

func renderProgress(ratio float64, width int) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	filled := int(float64(width) * ratio)
	fill := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).
		Render(strings.Repeat("━", filled))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")).
		Render(strings.Repeat("━", width-filled))
	return fill + empty
}

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds)
	if hours := total / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, (total%3600)/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// truncate shortens s so that it occupies at most width cells.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// wrapWords wraps a single-line string to the given width.
func wrapWords(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if lipgloss.Width(line)+1+lipgloss.Width(word) <= width {
			line += " " + word
		} else {
			lines = append(lines, line)
			line = word
		}
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n")
}
