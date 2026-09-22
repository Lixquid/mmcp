//go:build windows

package main

// Windows backend for the SMTC simulator: a PowerShell 5.1 helper process
// (smtc_script.ps1, embedded) talks to System Media Transport Controls via
// WinRT and streams one JSON snapshot line per session per second on
// stdout. Control commands are forwarded as JSON lines on stdin. The hub
// keeps no window visible for the helper and respawns it if it dies.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	_ "embed"
)

// smtcSupported reports whether this build can talk to SMTC. Only the
// Windows build has the helper (see smtc_other.go).
func smtcSupported() bool { return true }

//go:embed smtc_script.ps1
var smtcScript string

// powershellBackend drives the SMTC helper process.
type powershellBackend struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
}

// newSMTCSim creates a simulated SMTC provider for the given relay.
func newSMTCSim(relay *Relay) *smtcSim {
	return newSMTCSimCore(relay, &powershellBackend{})
}

// run spawns the helper and feeds its snapshots to emit until stop is
// closed. If the helper dies it is respawned after smtcReconnect.
func (b *powershellBackend) run(stop <-chan struct{}, emit func(smtcTick)) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		if !b.spawn() {
			select {
			case <-stop:
				return
			case <-time.After(smtcReconnect):
			}
			continue
		}

		// Pump snapshot lines until the helper dies, then respawn.
		b.pump(emit)

		b.mu.Lock()
		if b.cmd != nil {
			_ = b.cmd.Process.Kill()
			_ = b.cmd.Wait()
			b.cmd = nil
		}
		b.stdin = nil
		b.out = nil
		b.mu.Unlock()

		select {
		case <-stop:
			return
		case <-time.After(smtcReconnect):
		}
	}
}

// spawn launches the helper process and remembers its handles.
func (b *powershellBackend) spawn() bool {
	// -EncodedCommand takes the script as base64 UTF-16LE, avoiding any
	// quoting pitfalls.
	enc := base64.StdEncoding.EncodeToString(utf16LEBytes(smtcScript))
	cmd := exec.Command("powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", enc)
	cmd.Stderr = io.Discard
	// Never flash a console window: CREATE_NO_WINDOW.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return false
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return false
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.out = bufio.NewReader(stdout)
	b.mu.Unlock()
	return true
}

// pump forwards every parseable snapshot line to emit until the helper's
// stdout closes.
func (b *powershellBackend) pump(emit func(smtcTick)) {
	b.mu.Lock()
	out := b.out
	b.mu.Unlock()
	if out == nil {
		return
	}

	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if tick, ok := parseSMTCTick(scanner.Bytes()); ok {
			emit(tick)
		}
	}
}

// execute writes a control command to the helper's stdin.
func (b *powershellBackend) execute(sessionID, command, arg string) {
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return
	}
	_, _ = stdin.Write(append(smtcCommandJSON(sessionID, command, arg), '\n'))
}

// utf16LEBytes encodes s as UTF-16LE for PowerShell -EncodedCommand.
func utf16LEBytes(s string) []byte {
	codes := utf16.Encode([]rune(s))
	buf := &bytes.Buffer{}
	for _, v := range codes {
		buf.WriteByte(byte(v))
		buf.WriteByte(byte(v >> 8))
	}
	return buf.Bytes()
}
