package main

import (
	"strings"
	"testing"
)

func TestAppVersionInjected(t *testing.T) {
	// When a version is injected at build time it wins.
	old := version
	defer func() { version = old }()
	version = "v1.2.3-dirty"
	if got := appVersion(); got != "v1.2.3-dirty" {
		t.Fatalf("appVersion() = %q, want injected value", got)
	}
}

func TestAppVersionFallback(t *testing.T) {
	// With no injected version the fallback is non-empty (from embedded
	// VCS info when available, "dev" otherwise).
	old := version
	defer func() { version = old }()
	version = ""
	if got := appVersion(); got == "" {
		t.Fatal("appVersion() returned empty string")
	}
}

func TestUserAgentFormat(t *testing.T) {
	// userAgent is a package-level var initialized from appVersion().
	if !strings.HasPrefix(userAgent, "MMCP-Hub/") {
		t.Fatalf("userAgent %q lacks MMCP-Hub/ prefix", userAgent)
	}
	if !strings.Contains(userAgent, "(https://") {
		t.Fatalf("userAgent %q lacks contact URL", userAgent)
	}
}
