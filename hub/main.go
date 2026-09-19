// Command hub is a graphical MMCP hub: a Fyne desktop application that
// hosts a stateless multicast WebSocket relay, acts as a controller for
// connected providers, and shows a live debug feed of every message.
package main

import (
	"flag"
	"log"

	_ "embed"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

//go:embed icon.png
var iconData []byte

func main() {
	port := flag.Int("port", defaultRelayPort, "TCP port for the MMCP relay")
	flag.Parse()

	a := app.NewWithID("io.mmcp.hub")
	a.SetIcon(&fyne.StaticResource{
		StaticName:    "icon.png",
		StaticContent: iconData,
	})

	window := a.NewWindow("MMCP Hub")

	msgLog := newMessageLog(maxLogLines)
	relay := newRelay(*port, msgLog)

	ui := newHubUI(window, relay)

	if err := relay.Start(); err != nil {
		log.Fatal(err)
	}

	// The hub UI acts as a controller connected to its own relay.
	relay.AttachLocal(ui.deliver)

	// Discover providers that are already connected.
	if data, ok := encodeControl(broadcastID, "INFO"); ok {
		relay.SendFromLocal(data)
	}

	window.SetContent(ui.build())
	window.Resize(fyne.NewSize(1000, 680))

	ui.start()

	window.ShowAndRun()
}
