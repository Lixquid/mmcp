//go:build windows

package main

// The Windows build has no D-Bus support, so the MPRIS simulator is stubbed
// out entirely: the UI never shows its checkbox and no D-Bus code is
// compiled.

// mprisSupported reports whether this build can talk to MPRIS.
func mprisSupported() bool { return false }

// mprisSim is a no-op stand-in for the MPRIS simulator on Windows.
type mprisSim struct{}

// newMPRISSim returns the no-op simulator.
func newMPRISSim(relay *Relay) *mprisSim { return &mprisSim{} }

// Start is a no-op.
func (*mprisSim) Start() {}

// Stop is a no-op.
func (*mprisSim) Stop() {}
