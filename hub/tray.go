package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// setupTray installs the hub's system-tray icon and menu, and turns the
// window's close button into "minimize to tray": the relay and every
// provider keep running while the window is hidden.
//
// Tray menu callbacks are dispatched on the Fyne event loop by the driver,
// so window.Show()/app.Quit() inside them are safe.
func setupTray(a fyne.App, window fyne.Window) {
	desk, ok := a.(desktop.App)
	if !ok {
		// No desktop driver (mobile/test builds): leave close as quit.
		return
	}

	show := fyne.NewMenuItem("Show hub", nil)
	show.Action = window.Show

	quit := fyne.NewMenuItem("Quit", nil)
	quit.IsQuit = true
	quit.Action = a.Quit

	menu := fyne.NewMenu("", show, fyne.NewMenuItemSeparator(), quit)
	desk.SetSystemTrayMenu(menu)
	// The app icon (set in main) is used by the tray automatically on all
	// supported platforms, so no separate SetSystemTrayIcon call is needed.

	// Closing the window hides it instead of quitting; exiting happens via
	// the tray menu's Quit item (or the driver's own fallback Quit entry).
	window.SetCloseIntercept(window.Hide)
}
