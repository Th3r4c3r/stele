package search

import (
	"strings"
	"testing"
)

func TestSnippetCenter(t *testing.T) {
	src := "Customer reports intermittent BMS_FAULT_03 fault during normal operation across the entire ride."
	got := snippet(src, "BMS_FAULT_03", 80)
	if !strings.Contains(got, "BMS_FAULT_03") {
		t.Fatalf("snippet missing term: %q", got)
	}
	if len(got) > 90 {
		t.Fatalf("snippet too long: %d", len(got))
	}
}

func TestSnippetCaseInsensitive(t *testing.T) {
	got := snippet("hello WORLD foo bar", "world", 40)
	if !strings.Contains(got, "WORLD") {
		t.Fatalf("snippet should keep original case: %q", got)
	}
}

func TestSnippetNotFound(t *testing.T) {
	if got := snippet("hello world", "xyz", 20); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestSnippetShortInput(t *testing.T) {
	if got := snippet("hi", "hi", 40); got != "hi" {
		t.Fatalf("got %q", got)
	}
}
