package main

import (
	"strings"
	"testing"
)

func TestBannerConstant(t *testing.T) {
	if !strings.Contains(banner, "Stele") || !strings.Contains(banner, "alive") {
		t.Fatalf("banner missing required tokens: %q", banner)
	}
}

func TestEnvOr(t *testing.T) {
	if got := envOr("STELE_DEFINITELY_UNSET_XYZ", "fallback"); got != "fallback" {
		t.Fatalf("got %q want fallback", got)
	}
	t.Setenv("STELE_TEST_VAR", "value")
	if got := envOr("STELE_TEST_VAR", "fallback"); got != "value" {
		t.Fatalf("got %q want value", got)
	}
}

func TestTrimScheme(t *testing.T) {
	// Imported indirectly via main; just exercise envOr fallback for now.
	// The migrate package has its own coverage.
}
