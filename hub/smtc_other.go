//go:build !windows

package main

// Non-Windows builds have no System Media Transport Controls, so the SMTC
// simulator is stubbed out entirely: the UI never shows its checkbox and no
// Windows-specific code is compiled.

// smtcSupported reports whether this build can talk to SMTC.
func smtcSupported() bool { return false }

// newSMTCSim returns the no-op simulator on platforms without SMTC.
func newSMTCSim(relay *Relay) *smtcSim {
	return newSMTCSimCore(relay, nopSMTBackend{})
}

// nopSMTBackend never produces snapshots.
type nopSMTBackend struct{}

func (nopSMTBackend) run(stop <-chan struct{}, emit func(smtcTick)) {
	<-stop
}

func (nopSMTBackend) execute(sessionID, command, arg string) {}
