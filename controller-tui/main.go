// Command controller-tui is a terminal controller for the Multicast Media
// Control Protocol (MMCP). It discovers providers over a stateless
// multicast WebSocket relay, displays playback state derived from TRACK and
// POS messages, and sends CONTROL commands.
package main

import (
	"fmt"
	"net/url"
	"os"
)

const defaultRelay = "ws://localhost:9994"

func relayEndpoint() (string, error) {
	relay := os.Getenv("MMCP_RELAY")
	if relay == "" && len(os.Args) > 1 {
		relay = os.Args[1]
	}
	if relay == "" {
		relay = defaultRelay
	}

	u, err := url.Parse(relay)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid relay endpoint %q", relay)
	}
	return relay, nil
}

func main() {
	relay, err := relayEndpoint()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := runProgram(relay); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
