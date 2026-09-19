package main

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	maxLogLines      = 400
	debugPaneHeight  = 230
	defaultSliderMax = 100
	artDisplaySide   = 170
	httpTimeout      = 8 * time.Second
)

const defaultRelayPort = 9994

// hubUI is the Fyne front end of the hub.
type hubUI struct {
	window fyne.Window
	relay  *Relay
	state  *controllerState
	root   fyne.CanvasObject

	// Provider list.
	list    *widget.List
	listIDs []string

	// Focused provider; "" means follow the active (playing) provider.
	selected string

	// Detail panel.
	titleLabel  *widget.Label
	artistLabel *widget.Label
	stateLabel  *widget.Label
	sourceLabel *widget.Label
	idLabel     *widget.Label
	capsLabel   *widget.Label
	artImage    *canvas.Image

	// Controls.
	playBtn  *widget.Button
	prevBtn  *widget.Button
	nextBtn  *widget.Button
	slider   *widget.Slider
	posLabel *widget.Label
	lenLabel *widget.Label
	dragging bool

	// Debug panel.
	debugGrid *widget.TextGrid
	debugPane fyne.CanvasObject

	// Toolbar.
	statusLabel *widget.Label

	// Artwork fetching.
	artMu      sync.Mutex
	artShown   string
	artCache   map[string]image.Image
	artPending map[string]bool
}

func newHubUI(window fyne.Window, relay *Relay) *hubUI {
	return &hubUI{
		window:     window,
		relay:      relay,
		state:      newControllerState(),
		artCache:   make(map[string]image.Image),
		artPending: make(map[string]bool),
	}
}

// deliver is the local relay client's receiver: it updates the derived
// state and schedules a UI refresh on the Fyne event loop.
func (u *hubUI) deliver(data []byte) {
	msg, ok := parseMessage(data)
	if !ok {
		return
	}

	u.state.handle(msg)
	fyne.Do(u.refresh)
}

// start launches the periodic refresh used to interpolate positions and
// show staleness.
func (u *hubUI) start() {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			fyne.Do(u.refresh)
		}
	}()
}

// build constructs the whole UI.
func (u *hubUI) build() fyne.CanvasObject {
	// ---- Detail panel -------------------------------------------------
	u.titleLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	u.titleLabel.Wrapping = fyne.TextWrapWord

	u.artistLabel = widget.NewLabel("")
	u.artistLabel.Wrapping = fyne.TextWrapWord

	u.stateLabel = widget.NewLabel("")
	u.sourceLabel = widget.NewLabel("")
	u.idLabel = widget.NewLabel("")
	u.capsLabel = widget.NewLabel("")
	u.capsLabel.Wrapping = fyne.TextWrapWord

	u.artImage = canvas.NewImageFromImage(nil)
	u.artImage.FillMode = canvas.ImageFillContain

	artBox := container.NewGridWrap(fyne.NewSize(artDisplaySide, artDisplaySide), u.artImage)

	detail := container.NewVBox(
		u.titleLabel,
		u.artistLabel,
		u.stateLabel,
		u.sourceLabel,
		u.idLabel,
		u.capsLabel,
		artBox,
	)
	detailScroll := container.NewVScroll(detail)

	// ---- Provider list -------------------------------------------------
	u.list = widget.NewList(
		func() int { return len(u.listIDs) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("template")
			l.Wrapping = fyne.TextWrapWord
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if int(i) >= len(u.listIDs) {
				return
			}
			id := u.listIDs[i]
			label := o.(*widget.Label)

			v, ok := u.state.view(id)
			if !ok {
				label.SetText(id)
				return
			}

			state := "⏸"
			if v.State == statePlaying {
				state = "▶"
			}
			title := "(no track)"
			if v.Track != nil && v.Track.title != "" {
				title = v.Track.title
				if v.Track.artist != "" {
					title += " — " + v.Track.artist
				}
			}
			label.SetText(fmt.Sprintf("%s %s\n%s", state, id, title))
		},
	)
	u.list.OnSelected = func(i widget.ListItemID) {
		if int(i) < len(u.listIDs) {
			u.selected = u.listIDs[i]
			u.refresh()
		}
	}

	listHeader := widget.NewLabelWithStyle("Providers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	followBtn := widget.NewButton("Follow playing", func() {
		u.selected = ""
		u.list.UnselectAll()
		u.refresh()
	})

	listPane := container.NewBorder(listHeader, followBtn, nil, nil, u.list)

	// ---- Controls ------------------------------------------------------
	u.playBtn = widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() {
		u.sendCommand("PLAYPAUSE")
	})
	u.prevBtn = widget.NewButtonWithIcon("", theme.MediaSkipPreviousIcon(), func() {
		u.sendCommand("PREV")
	})
	u.nextBtn = widget.NewButtonWithIcon("", theme.MediaSkipNextIcon(), func() {
		u.sendCommand("NEXT")
	})

	u.posLabel = widget.NewLabel("0:00")
	u.lenLabel = widget.NewLabel("--:--")

	u.slider = widget.NewSlider(0, defaultSliderMax)
	u.slider.Step = 1
	u.slider.OnChanged = func(v float64) {
		u.dragging = true
		u.posLabel.SetText(formatDuration(v))
	}
	u.slider.OnChangeEnded = func(v float64) {
		u.dragging = false
		u.sendSeekTo(v)
	}

	rightBox := container.NewHBox(u.posLabel, widget.NewLabel("/"), u.lenLabel)
	controls := container.NewBorder(nil, nil,
		container.NewHBox(u.prevBtn, u.playBtn, u.nextBtn),
		rightBox,
		container.NewPadded(u.slider),
	)

	// ---- Debug panel ---------------------------------------------------
	u.debugGrid = widget.NewTextGrid()
	debugScroll := container.NewVScroll(u.debugGrid)
	debugScroll.SetMinSize(fyne.NewSize(0, debugPaneHeight))
	u.debugPane = container.NewVScroll(u.debugGrid)
	u.debugPane = debugScroll

	// ---- Toolbar -------------------------------------------------------
	u.statusLabel = widget.NewLabel("")

	debugCheck := widget.NewCheck("Debug messages", func(on bool) {
		if on {
			u.debugPane.Show()
		} else {
			u.debugPane.Hide()
		}
		// Show/Hide does not re-layout the ancestors; force it.
		u.root.Refresh()
	})
	debugCheck.SetChecked(false)
	u.debugPane.Hide()

	discoverBtn := widget.NewButton("Discover", func() {
		if data, ok := encodeControl(broadcastID, "INFO"); ok {
			u.relay.SendFromLocal(data)
		}
	})

	toolbar := container.NewBorder(nil, nil,
		container.NewHBox(
			widget.NewLabelWithStyle("MMCP Hub", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			discoverBtn,
			debugCheck,
		),
		nil,
		container.NewHBox(layout.NewSpacer(), u.statusLabel),
	)

	// ---- Assemble ------------------------------------------------------
	center := container.NewHSplit(listPane, detailScroll)
	center.Offset = 0.32

	root := container.NewBorder(toolbar, container.NewVBox(controls, u.debugPane), nil, nil, center)
	u.root = root

	// Wire UI-facing callbacks from the relay and the message log.
	u.relay.SetOnUpdate(func() {
		fyne.Do(func() {
			u.statusLabel.SetText(fmt.Sprintf("relay ws://localhost:%d · %d clients",
				u.relay.Port(), u.relay.ClientCount()))
		})
	})
	u.relay.log.SetOnChange(func() {
		fyne.Do(func() {
			u.debugGrid.SetText(u.relay.log.Text())
			debugScroll.ScrollToBottom()
		})
	})

	u.refresh()

	return root
}

// focus returns the provider commands and the detail panel operate on.
func (u *hubUI) focus() (providerView, bool) {
	if u.selected != "" {
		if v, ok := u.state.view(u.selected); ok {
			return v, true
		}
	}
	if id := u.state.playingID(); id != "" {
		return u.state.view(id)
	}
	return providerView{}, false
}

// refresh updates every piece of UI from the current state. It must run on
// the Fyne event loop (via fyne.Do).
func (u *hubUI) refresh() {
	ids := u.state.ids()
	if !equalStrings(ids, u.listIDs) {
		u.listIDs = ids
		u.list.Refresh()
	}

	view, ok := u.focus()
	if !ok {
		u.renderEmpty()
		u.updateControls(providerView{}, false)
		return
	}

	u.renderDetail(view)
	u.updateControls(view, true)
}

func (u *hubUI) renderEmpty() {
	u.titleLabel.SetText("No providers known")
	u.artistLabel.SetText("Providers appear here once they answer a broadcast INFO.")
	u.stateLabel.SetText("")
	u.sourceLabel.SetText("")
	u.idLabel.SetText("")
	u.capsLabel.SetText("")
	u.posLabel.SetText("0:00")
	u.lenLabel.SetText("--:--")
	u.setArt("")
}

func (u *hubUI) renderDetail(v providerView) {
	if v.Track != nil {
		title := v.Track.title
		if title == "" {
			title = "(untitled)"
		}
		u.titleLabel.SetText(title)

		sub := make([]string, 0, 2)
		if v.Track.artist != "" {
			sub = append(sub, v.Track.artist)
		}
		if v.Track.album != "" {
			sub = append(sub, v.Track.album)
		}
		u.artistLabel.SetText(strings.Join(sub, " — "))

		source := v.Track.source
		if source == "" {
			source = "unknown"
		}
		u.sourceLabel.SetText("Source: " + source)
		u.setArt(v.Track.art)
	} else {
		u.titleLabel.SetText("(no track announced)")
		u.artistLabel.SetText("")
		u.sourceLabel.SetText("")
		u.setArt("")
	}

	switch v.State {
	case statePlaying:
		u.stateLabel.SetText("▶ Playing")
	case stateStopped:
		u.stateLabel.SetText("⏸ Paused / stopped")
	default:
		u.stateLabel.SetText("State unknown")
	}

	u.idLabel.SetText("Provider: " + v.ID)
	if len(v.Caps) > 0 {
		u.capsLabel.SetText("Capabilities: " + strings.Join(v.Caps, ", "))
	} else {
		u.capsLabel.SetText("Capabilities: none advertised")
	}
}

// updateControls enables or disables each control according to the focused
// provider's advertised capabilities.
func (u *hubUI) updateControls(v providerView, hasProvider bool) {
	setEnabled := func(b *widget.Button, enabled bool) {
		if enabled {
			b.Enable()
		} else {
			b.Disable()
		}
	}

	// PLAYPAUSE and INFO have no separate capability in MMCP.
	setEnabled(u.playBtn, hasProvider)
	setEnabled(u.nextBtn, hasProvider && v.HasCap("NEXT"))
	setEnabled(u.prevBtn, hasProvider && v.HasCap("PREV"))

	seekable := hasProvider && v.HasCap("SEEK")
	if seekable {
		u.slider.Enable()
	} else {
		u.slider.Disable()
	}

	if hasProvider && v.State == statePlaying {
		u.playBtn.SetIcon(theme.MediaPauseIcon())
	} else {
		u.playBtn.SetIcon(theme.MediaPlayIcon())
	}

	// Position and seek bar.
	pos, length, hasLength := v.displayPosition()

	if hasLength {
		if u.slider.Max != length {
			u.slider.Max = length
		}
		u.lenLabel.SetText(formatDuration(length))
	} else {
		if u.slider.Max != defaultSliderMax {
			u.slider.Max = defaultSliderMax
		}
		u.lenLabel.SetText("--:--")
	}

	if !u.dragging {
		if v.Pos != nil {
			u.slider.Value = pos
			u.posLabel.SetText(formatDuration(pos))
		} else {
			u.slider.Value = 0
			u.posLabel.SetText("0:00")
		}
	}
	u.slider.Refresh()
}

// sendCommand sends a CONTROL message addressed to the focused provider.
func (u *hubUI) sendCommand(command string) {
	view, ok := u.focus()
	if !ok {
		return
	}

	data, ok := encodeControl(view.ID, command)
	if !ok {
		return
	}

	u.relay.SendFromLocal(data)
}

// sendSeekTo sends a SEEK command to the focused provider.
func (u *hubUI) sendSeekTo(position float64) {
	if position < 0 {
		position = 0
	}

	view, ok := u.focus()
	if !ok {
		return
	}

	data, ok := encodeControl(view.ID, "SEEK", strconv.FormatFloat(position, 'f', -1, 64))
	if !ok {
		return
	}

	u.relay.SendFromLocal(data)
}

// setArt shows the artwork for url, fetching it off the event loop.
func (u *hubUI) setArt(url string) {
	u.artMu.Lock()
	if url == u.artShown {
		u.artMu.Unlock()
		return
	}
	u.artShown = url
	u.artMu.Unlock()

	if url == "" {
		u.artImage.Image = nil
		u.artImage.Refresh()
		return
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		// Not a fetchable URL; leave the image empty.
		u.artImage.Image = nil
		u.artImage.Refresh()
		return
	}

	go func() {
		img := u.fetchArt(url)

		fyne.Do(func() {
			u.artMu.Lock()
			current := u.artShown
			u.artMu.Unlock()

			// A newer selection may have taken over meanwhile.
			if current != url {
				return
			}

			u.artImage.Image = img
			u.artImage.Refresh()
		})
	}()
}

func (u *hubUI) fetchArt(url string) image.Image {
	u.artMu.Lock()
	if img, ok := u.artCache[url]; ok {
		u.artMu.Unlock()
		return img
	}
	if u.artPending[url] {
		u.artMu.Unlock()
		return nil
	}
	u.artPending[url] = true
	u.artMu.Unlock()

	defer func() {
		u.artMu.Lock()
		delete(u.artPending, url)
		u.artMu.Unlock()
	}()

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}

	img, _, err := image.Decode(strings.NewReader(string(data)))
	if err != nil {
		return nil
	}

	u.artMu.Lock()
	u.artCache[url] = img
	u.artMu.Unlock()

	return img
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
