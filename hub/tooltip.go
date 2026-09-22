package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Fyne 2.6 has no built-in tooltip support, so the hub implements its own:
// each element that wants a tip is wrapped in a hoverable proxy (see
// hubUI.withToolTip). On hover the wrapper shows a small floating label near
// the pointer; it hides again on MouseOut.

// tooltipOffset pushes the tip a little right and below the pointer so it
// does not sit under the cursor.
const tooltipOffset = 16

// tipWrapper makes any canvas object hoverable and forwards hover state to
// the UI's shared tooltip overlay.
type tipWrapper struct {
	widget.BaseWidget

	obj  fyne.CanvasObject
	u    *hubUI
	text string
}

func newTipWrapper(obj fyne.CanvasObject, u *hubUI, text string) *tipWrapper {
	w := &tipWrapper{obj: obj, u: u, text: text}
	w.ExtendBaseWidget(w)
	return w
}

func (t *tipWrapper) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.obj)
}

func (t *tipWrapper) MouseIn(e *desktop.MouseEvent) {
	t.show(e)
}

func (t *tipWrapper) MouseMoved(e *desktop.MouseEvent) {
	t.show(e)
}

func (t *tipWrapper) MouseOut() {
	t.u.hideTip()
}

// show positions the shared tooltip near the pointer. Mouse events run on
// the Fyne event loop, so direct canvas mutation is safe.
func (t *tipWrapper) show(e *desktop.MouseEvent) {
	t.u.showTip(t.text, fyne.NewPos(
		e.AbsolutePosition.X+tooltipOffset,
		e.AbsolutePosition.Y+tooltipOffset,
	))
}

// buildTipOverlay creates the floating tooltip: an inverted-background box
// with a monospace label, hidden until something is hovered. It lives in a
// container.NewWithoutLayout so its position is never reset by layout
// passes.
func buildTipOverlay() (overlay *fyne.Container, text *canvas.Text, box *fyne.Container) {
	text = canvas.NewText("", theme.Color(theme.ColorNameForeground))
	text.TextStyle = fyne.TextStyle{Monospace: true}
	text.TextSize = theme.TextSize() - 2

	box = container.NewStack(
		&canvas.Rectangle{FillColor: theme.Color(theme.ColorNameBackground)},
		container.NewPadded(text),
	)
	box.Hide()

	overlay = container.NewWithoutLayout(box)
	return overlay, text, box
}

// showTip displays the shared tooltip at the given window-relative position.
func (u *hubUI) showTip(text string, pos fyne.Position) {
	if u.tipText == nil {
		return
	}
	u.tipText.Text = text
	u.tipText.Refresh()

	size := u.tipBox.MinSize()
	u.tipBox.Resize(size)
	// Keep the tip inside the window bounds.
	maxX := u.window.Canvas().Size().Width - size.Width
	maxY := u.window.Canvas().Size().Height - size.Height
	if pos.X > maxX {
		pos.X = maxX
	}
	if pos.Y > maxY {
		pos.Y = maxY
	}
	u.tipBox.Move(pos)
	u.tipBox.Show()
	u.tipBox.Refresh()
}

// hideTip hides the shared tooltip.
func (u *hubUI) hideTip() {
	if u.tipBox == nil {
		return
	}
	u.tipBox.Hide()
}

// withToolTip wraps obj so hovering it shows text.
func (u *hubUI) withToolTip(text string, obj fyne.CanvasObject) fyne.CanvasObject {
	return newTipWrapper(obj, u, text)
}
