package main

import (
	"strings"
	"testing"
)

func TestMessageLogClear(t *testing.T) {
	l := newMessageLog(10)
	l.Add("a", []byte("one"))
	l.Add("b", []byte("two"))
	if !strings.Contains(l.Text(), "one") || !strings.Contains(l.Text(), "two") {
		t.Fatalf("expected entries, got %q", l.Text())
	}

	// Clear must wipe everything and remain usable afterwards.
	l.Clear()
	if l.Text() != "" {
		t.Fatalf("expected empty after Clear, got %q", l.Text())
	}
	l.Add("c", []byte("three"))
	if !strings.Contains(l.Text(), "three") {
		t.Fatalf("expected new entry after Clear, got %q", l.Text())
	}
}

func TestMessageLogPause(t *testing.T) {
	l := newMessageLog(10)
	l.Add("a", []byte("one"))

	l.SetPaused(true)
	if !l.Paused() {
		t.Fatal("expected paused")
	}
	l.Add("b", []byte("dropped"))
	if strings.Contains(l.Text(), "dropped") {
		t.Fatal("paused Add must not record")
	}

	l.SetPaused(false)
	if l.Paused() {
		t.Fatal("expected resumed")
	}
	l.Add("c", []byte("kept"))
	if !strings.Contains(l.Text(), "kept") {
		t.Fatalf("expected recording to resume, got %q", l.Text())
	}
	// history is retained across the pause
	if !strings.Contains(l.Text(), "one") {
		t.Fatal("history should be retained across pause")
	}
}

func TestMessageLogPauseFiresNoCallbacks(t *testing.T) {
	l := newMessageLog(10)
	calls := 0
	l.SetOnChange(func() { calls++ })

	l.SetPaused(true)
	l.Add("a", []byte("x"))
	if calls != 0 {
		t.Fatalf("paused Add fired %d callbacks, want 0", calls)
	}

	l.SetPaused(false)
	l.Add("b", []byte("y"))
	if calls != 1 {
		t.Fatalf("resumed Add fired %d callbacks, want 1", calls)
	}

	// Clear fires the callback so the UI can refresh to empty.
	l.Clear()
	if calls != 2 {
		t.Fatalf("Clear fired %d callbacks, want 1 more", calls)
	}
}
