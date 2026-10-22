package desktopnotify

import (
	"strings"
	"testing"
)

func TestCleanTextNormalizesControlsAndBoundsLength(t *testing.T) {
	value := cleanText("  transfer\n\tfinished  ", 64)
	if value != "transfer finished" {
		t.Fatalf("cleaned text = %q", value)
	}

	value = cleanText(strings.Repeat("界", 80), 64)
	if got := len([]rune(value)); got != 64 {
		t.Fatalf("truncated rune length = %d, want 64", got)
	}
	if !strings.HasSuffix(value, "…") {
		t.Fatalf("truncated text lacks ellipsis: %q", value)
	}
}

func TestSystemNotifierRejectsEmptyContent(t *testing.T) {
	if err := (SystemNotifier{}).Notify("\n", "\t"); err == nil {
		t.Fatal("empty notification content was accepted")
	}
}
