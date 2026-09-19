package main

import (
	"context"
	"strconv"
	"time"

	"github.com/charmbracelet/bubbletea"
)

const (
	seekSmall = 5.0
	seekLarge = 30.0
	tickEvery = time.Second
)

// tickMsg drives periodic re-rendering so interpolated positions advance
// and staleness stays visible.
type tickMsg time.Time

type model struct {
	ws *wsClient

	state *controllerState

	// selected is the provider commands are addressed to. When empty,
	// commands follow the currently playing provider.
	selected string

	width, height int

	status   string
	quitting bool

	// Go-to-timestamp input mode.
	inputActive bool
	input       string
}

func newModel(relay string) model {
	return model{
		ws:    newWSClient(relay),
		state: newControllerState(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
		return m, tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })

	case wsConnectedMsg:
		m.state.connected = true
		m.state.lastErr = ""
		// The connection triggers a broadcast INFO; rebuild the provider
		// list from scratch so stale entries never linger.
		m.state.reset()
		m.selected = ""
		m.status = "Connected — discovery requested"

	case wsDisconnectedMsg:
		m.state.connected = false
		m.status = "Disconnected — reconnecting..."

	case wsErrorMsg:
		m.state.lastErr = msg.err.Error()

	case wsDataMsg:
		// Malformed and unknown messages are silently ignored and never
		// cause disconnection.
		if msg, ok := parseMessage(msg.data); ok {
			m.state.handle(msg)
		}
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inputActive {
		return m.handleInputKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.ws.stop()
		return m, tea.Quit

	case " ", "space":
		return m, m.sendCommand("PLAYPAUSE")

	case "p":
		return m, m.sendCommand("PLAY")

	case "s":
		return m, m.sendCommand("PAUSE")

	case "n":
		return m, m.sendCommand("NEXT")

	case "b":
		return m, m.sendCommand("PREV")

	case "right":
		return m, m.sendSeekDelta(seekSmall)

	case "left":
		return m, m.sendSeekDelta(-seekSmall)

	case "ctrl+right":
		return m, m.sendSeekDelta(seekLarge)

	case "ctrl+left":
		return m, m.sendSeekDelta(-seekLarge)

	case "g":
		m.inputActive = true
		m.input = ""
		m.status = ""
		return m, nil

	case "i":
		return m, m.sendBroadcastInfo()

	case "r":
		return m, m.sendCommand("INFO")

	case "tab", "down", "j":
		m.cycleSelection(1)
		return m, nil

	case "shift+tab", "up", "k":
		m.cycleSelection(-1)
		return m, nil

	case "esc":
		// Return to following the active (playing) provider.
		m.selected = ""
		m.status = "Following active player"
		return m, nil
	}

	return m, nil
}

func (m model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputActive = false
		m.input = ""
		m.status = "Seek cancelled"
		return m, nil

	case "enter":
		position, ok := parseTimestamp(m.input)
		if !ok {
			m.status = "Invalid timestamp — use seconds, MM:SS, or HH:MM:SS"
			return m, nil
		}
		m.inputActive = false
		m.input = ""
		return m, m.sendSeekTo(position)

	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	}

	if len(msg.String()) == 1 {
		if c := msg.String()[0]; c >= '0' && c <= '9' || c == ':' || c == '.' {
			m.input += string(c)
		}
	}
	return m, nil
}

// cycleSelection moves the manual selection through the known providers.
// An empty selection means "follow the active player"; the first cycle
// selects the current follow target so it is always visible where the
// selection starts.
func (m *model) cycleSelection(delta int) {
	ids := m.state.ids()
	if len(ids) == 0 {
		m.selected = ""
		return
	}

	current := m.selected
	if current == "" {
		current = m.state.playingID()
	}

	index := 0
	for i, id := range ids {
		if id == current {
			index = i
			break
		}
	}

	index = (index + delta + len(ids)) % len(ids)
	m.selected = ids[index]
}

// target returns a snapshot of the provider commands should be addressed
// to, or false if none is known.
func (m model) target() (providerView, bool) {
	if m.selected != "" {
		if v, ok := m.state.view(m.selected); ok {
			return v, true
		}
	}
	if id := m.state.playingID(); id != "" {
		return m.state.view(id)
	}
	return providerView{}, false
}

func (m model) sendCommand(command string) tea.Cmd {
	if !m.state.connected {
		m.status = "Not connected"
		return nil
	}

	target, ok := m.target()
	if !ok {
		m.status = "No provider available"
		return nil
	}

	data, ok := encodeControl(target.ID, command)
	if !ok {
		m.status = "Could not encode " + command
		return nil
	}

	if !m.ws.send(data) {
		m.status = "Send queue full"
		return nil
	}

	m.status = "Sent " + command + " → " + target.ID
	return nil
}

func (m model) sendBroadcastInfo() tea.Cmd {
	if !m.state.connected {
		m.status = "Not connected"
		return nil
	}

	// Start fresh: broadcast INFO makes every provider reannounce, so any
	// stale entries are dropped and the list is rebuilt from the responses.
	m.state.reset()
	m.selected = ""

	data, ok := encodeControl("*", "INFO")
	if !ok {
		m.status = "Could not encode INFO"
		return nil
	}

	if !m.ws.send(data) {
		m.status = "Send queue full"
		return nil
	}

	m.status = "Sent broadcast INFO — rediscovering providers"
	return nil
}

func (m model) sendSeekDelta(delta float64) tea.Cmd {
	target, ok := m.target()
	if !ok {
		m.status = "No provider available"
		return nil
	}

	position, length, hasLength := target.displayPosition()
	if target.Pos == nil {
		m.status = "Position unavailable"
		return nil
	}

	position += delta
	if position < 0 {
		position = 0
	}
	if hasLength && length > 0 && position > length {
		position = length
	}

	return m.sendSeekTo(position)
}

func (m model) sendSeekTo(position float64) tea.Cmd {
	if !m.state.connected {
		m.status = "Not connected"
		return nil
	}

	target, ok := m.target()
	if !ok {
		m.status = "No provider available"
		return nil
	}

	if position < 0 {
		position = 0
	}

	data, ok := encodeControl(target.ID, "SEEK", strconv.FormatFloat(position, 'f', -1, 64))
	if !ok {
		m.status = "Could not encode SEEK"
		return nil
	}

	if !m.ws.send(data) {
		m.status = "Send queue full"
		return nil
	}

	m.status = "Seek to " + formatDuration(position) + " → " + target.ID
	return nil
}

// runProgram builds and runs the Bubble Tea program.
func runProgram(relay string) error {
	m := newModel(relay)

	program := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithContext(context.Background()),
	)

	m.ws.start(program)

	_, err := program.Run()
	m.ws.stop()
	return err
}
