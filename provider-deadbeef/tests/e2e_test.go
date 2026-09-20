// End-to-end test for the MMCP DeaDBeeF provider plugin.
//
// Runs a stateless relay in-process, launches the mock provider binary
// (tests/test_provider.c) as a subprocess, and drives a scripted
// controller conversation. Prints "RESULT PASS" or "RESULT FAIL".
//
// Usage: e2e-test <path-to-test_provider-binary> <event-log-path>
package main

import (
	"io"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const rs = "\x1e"

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

var (
	mu           sync.Mutex
	clients      = map[*client]bool{}
	controller   *client
	providerMsgs []string
)

type client struct {
	conn *websocket.Conn
}

func (c *client) write(text string) {
	c.conn.WriteMessage(websocket.TextMessage, []byte(text))
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: e2e-test <provider-binary> <event-log>")
		os.Exit(1)
	}
	providerBin, eventLog := os.Args[1], os.Args[2]
	eventLogPath = eventLog

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		c := &client{conn: conn}
		mu.Lock()
		clients[c] = true
		if req.URL.Query().Get("role") == "controller" {
			controller = c
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(clients, c)
			mu.Unlock()
			conn.Close()
		}()

		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			text := string(data)

			mu.Lock()
			isController := controller == c
			var targets []*client
			for other := range clients {
				if other != c {
					targets = append(targets, other)
				}
			}
			mu.Unlock()

			for _, t := range targets {
				t.write(text)
			}
			if !isController {
				mu.Lock()
				providerMsgs = append(providerMsgs, text)
				mu.Unlock()
			}
		}
	})

	go http.ListenAndServe("127.0.0.1:9995", nil)
	time.Sleep(200 * time.Millisecond)

	// launch the mock provider
	cmd := exec.Command(providerBin, eventLog)
	cmdStdin, stdinErr := cmd.StdinPipe()
	if stdinErr != nil {
		fmt.Println("FAIL stdin pipe:", stdinErr)
		os.Exit(1)
	}
	_ = stdinErr
	if err := cmd.Start(); err != nil {
		fmt.Println("FAIL starting provider:", err)
		os.Exit(1)
	}
	mockStdin = cmdStdin
	defer mockStdin.Close()
	defer cmd.Process.Kill()
	go func() {
		_ = cmd.Wait()
	}()

	pass := runChecks()

	time.Sleep(300 * time.Millisecond) // let final messages drain
	if pass {
		fmt.Println("RESULT PASS")
	} else {
		fmt.Println("RESULT FAIL")
	}
}

func countWithPrefix(prefix string) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, m := range providerMsgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

func waitFor(what string, timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	fmt.Printf("FAIL %s (timed out; got %d messages: %q)\n", what, len(providerMsgs), providerMsgs)
	mu.Unlock()
	return false
}

var mockStdin io.WriteCloser

func setEnable(v int) {
	fmt.Fprintf(mockStdin, "enable %d\n", v)
}

func runChecks() (pass bool) {
	pass = true

	// controller connection; the handler registers it via role=controller
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:9995?role=controller", nil)
	if err != nil {
		fmt.Println("FAIL controller dial:", err)
		return false
	}
	defer conn.Close()
	controller = &client{conn: conn}

	// 1. initial announce: CAPABILITIES, TRACK, POS
	ok := waitFor("initial announce", 5*time.Second, func() bool {
		return countWithPrefix("1/CAPABILITIES") > 0 &&
			countWithPrefix("1/TRACK") > 0 &&
			countWithPrefix("1/POS") > 0
	})
	report("initial announce (capabilities, track, pos)", ok)
	if !ok {
		return false
	}

	mu.Lock()
	caps, track := "", ""
	for _, m := range providerMsgs {
		if strings.HasPrefix(m, "1/CAPABILITIES") {
			caps = m
		}
		if strings.HasPrefix(m, "1/TRACK") {
			track = m
		}
	}
	mu.Unlock()

	instanceID := ""
	if f := strings.Split(caps, rs); len(f) > 1 {
		instanceID = f[1]
	}
	fmt.Println("OK provider instance id:", instanceID)

	if strings.Contains(caps, rs+"ART") {
		report("capabilities do not include ART", false)
		return false
	}
	report("capabilities do not include ART", true)

	if strings.Contains(track, rs+"P"+rs) &&
		strings.Contains(track, "Song A") &&
		strings.Contains(track, "Artist A") &&
		strings.Contains(track, "Album A") {
		report("track message has state P and full metadata", true)
	} else {
		fmt.Printf("FAIL track message: %q\n", track)
		return false
	}

	// 2. broadcast INFO -> TRACK, POS, CAPABILITIES in order
	controller.write("1/CONTROL" + rs + "*" + rs + "INFO")
	ok = waitFor("broadcast INFO response", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for j := 0; j+2 < len(providerMsgs); j++ {
			if strings.HasPrefix(providerMsgs[j], "1/TRACK") &&
				strings.HasPrefix(providerMsgs[j+1], "1/POS") &&
				strings.HasPrefix(providerMsgs[j+2], "1/CAPABILITIES") {
				return true
			}
		}
		return false
	})
	report("broadcast INFO answered with TRACK, POS, CAPABILITIES", ok)
	pass = pass && ok

	// 3. targeted INFO
	controller.write("1/CONTROL" + rs + instanceID + rs + "INFO")
	ok = waitFor("targeted INFO response", 3*time.Second, func() bool {
		return countWithPrefix("1/TRACK") >= 3 // announce + broadcast INFO + targeted INFO
	})
	report("targeted INFO answered", ok)
	pass = pass && ok

	// 3b. disable: the provider must disconnect and never reconnect.
	// Let any in-flight responses from the INFO stages drain first so the
	// count is stable.
	time.Sleep(700 * time.Millisecond)
	countBefore := countWithPrefix("1/TRACK")
	setEnable(0)
	time.Sleep(4 * time.Second)
	tracksAfterDisable := countWithPrefix("1/TRACK")
	silent := tracksAfterDisable == countBefore
	report("disable: no traffic while disabled", silent)
	pass = pass && silent

	// 3c. re-enable: connection and announce come back
	setEnable(1)
	ok = waitFor("re-enable: fresh announce", 10*time.Second, func() bool {
		return countWithPrefix("1/CAPABILITIES") >= 2 && countWithPrefix("1/TRACK") >= countBefore+1
	})
	report("re-enable: provider reconnects and reannounces", ok)
	pass = pass && ok

	// 4. SEEK: event logged and new position reported
	seekEventIdx := eventLogCount()
	controller.write("1/CONTROL" + rs + instanceID + rs + "SEEK" + rs + "92.5")
	ok = waitFor("SEEK event logged", 3*time.Second, func() bool {
		return eventLogCount() > seekEventIdx && eventLogContains("EVENT 19 92500")
	})
	report("SEEK logged DB_EV_SEEK with 92500 ms", ok)
	pass = pass && ok

	ok = waitFor("SEEK position report (~92.5s)", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range providerMsgs {
			if strings.HasPrefix(m, "1/POS") {
				f := strings.Split(m, rs)
				if len(f) < 3 {
					continue
				}
				var pos float64
				fmt.Sscanf(f[2], "%g", &pos)
				// only POS samples taken after the seek matter; the mock
				// rebases its clock, so anything >= 92.5 is the new position
				if pos >= 92.5 && pos < 100 {
					return true
				}
			}
		}
		return false
	})
	report("SEEK reported the new position (~92.5s)", ok)
	pass = pass && ok

	// 5. arbitration: another provider announces P -> we pause + announce S
	controller.write("1/TRACK" + rs + "q4Mn8Z2x" + rs + "P" + rs + "SOUNDCLOUD" + rs +
		"Another" + rs + "Artist B" + rs + "Album B" + rs + "")
	ok = waitFor("arbitration TRACK S", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		seenOtherP := false
		for _, m := range providerMsgs {
			if strings.HasPrefix(m, "1/TRACK") && strings.HasPrefix(m, "1/TRACK"+rs+"q4Mn8Z2x") {
				seenOtherP = true
			}
			if seenOtherP && strings.HasPrefix(m, "1/TRACK") &&
				strings.Contains(m, rs+"S"+rs) && strings.Contains(m, "Song A") {
				return true
			}
		}
		return false
	})
	report("arbitration: paused and announced TRACK S", ok)
	pass = pass && ok
	pauseLogged := eventLogContains("EVENT 6 ")
	report("arbitration logged DB_EV_PAUSE", pauseLogged)
	pass = pass && pauseLogged

	// 6. malformed input is ignored, connection stays up
	controller.write("garbage")
	controller.write("9/NOPE")
	controller.write("1/TRACK" + rs + "bad!id!!" + rs + "P")
	controller.write("1/TRACK" + rs + "q4Mn8Z2x" + rs + "X" + rs + "X" + rs + "t" + rs + "a" + rs + "l" + rs + "")
	time.Sleep(300 * time.Millisecond)
	controller.write("1/CONTROL" + rs + "*" + rs + "INFO")
	tracksBefore := countWithPrefix("1/TRACK")
	ok = waitFor("still alive after garbage", 3*time.Second, func() bool {
		return countWithPrefix("1/TRACK") > tracksBefore
	})
	report("malformed messages ignored, connection alive", ok)
	pass = pass && ok

	// 7. PLAYPAUSE while paused -> resumes -> TRACK P
	controller.write("1/CONTROL" + rs + instanceID + rs + "PLAYPAUSE")
	ok = waitFor("PLAYPAUSE resumes playback", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range providerMsgs {
			if strings.HasPrefix(m, "1/TRACK") && strings.Contains(m, rs+"P"+rs) {
				// a TRACK P announced after the mock was paused by arbitration
				if eventLogContains("EVENT 6 ") && eventLogContains("EVENT 3 ") {
					return true
				}
			}
		}
		return false
	})
	report("PLAYPAUSE resumed playback", ok)
	pass = pass && ok
	playLogged := eventLogContains("EVENT 3 ")
	report("PLAYPAUSE logged DB_EV_PLAY_CURRENT", playLogged)
	pass = pass && playLogged

	return pass
}

func report(what string, ok bool) {
	if ok {
		fmt.Println("OK " + what)
	} else {
		fmt.Println("FAIL " + what)
	}
}

// event log helpers

var eventMu sync.Mutex

func eventLogCount() int {
	eventMu.Lock()
	defer eventMu.Unlock()
	data, err := os.ReadFile(eventLogPath)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func eventLogContains(s string) bool {
	eventMu.Lock()
	defer eventMu.Unlock()
	data, err := os.ReadFile(eventLogPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), s)
}

var eventLogPath string
