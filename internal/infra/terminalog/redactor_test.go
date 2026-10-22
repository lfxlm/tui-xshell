package terminalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactorRedactsSecretsAcrossWriteBoundariesAndPreservesVT(t *testing.T) {
	redactor, err := NewRedactor([]string{`(?i)secret=[A-Z0-9]{1,16}`})
	if err != nil {
		t.Fatal(err)
	}
	var output []byte
	output = append(output, redactor.Write([]byte("before \x1b[31mSEC"))...)
	output = append(output, redactor.Write([]byte("RET=abc123\x1b[0m after\r\n"))...)
	output = append(output, redactor.Flush()...)
	if got, want := string(output), "before \x1b[31m[REDACTED]\x1b[0m after\r\n"; got != want {
		t.Fatalf("redacted output = %q, want %q", got, want)
	}
}

func TestRedactorDoesNotEmitPartialMatchWhenItCrossesTheSafeBoundary(t *testing.T) {
	redactor, err := NewRedactor([]string{`token=[a-z]{1,4}`})
	if err != nil {
		t.Fatal(err)
	}
	firstChunk := strings.Repeat("x", 25) + "token=abcd" + strings.Repeat("y", 15)
	output := append([]byte(nil), redactor.Write([]byte(firstChunk))...)
	output = append(output, redactor.Write([]byte(strings.Repeat("y", 30)))...)
	output = append(output, redactor.Flush()...)
	if got, want := string(output), strings.Repeat("x", 25)+redactionMarker+strings.Repeat("y", 45); got != want {
		t.Fatalf("boundary redaction = %q, want %q", got, want)
	}
}

func TestRedactorMergesOverlappingRulesWithoutLeakingTheRemainder(t *testing.T) {
	redactor, err := NewRedactor([]string{`secret=[a-z]{1,3}`, `et=[a-z]{1,12}`})
	if err != nil {
		t.Fatal(err)
	}
	output := append([]byte(nil), redactor.Write([]byte("secret=abcdefghijkl"))...)
	output = append(output, redactor.Flush()...)
	if got, want := string(output), redactionMarker; got != want {
		t.Fatalf("overlapping redaction result = %q, want %q", got, want)
	}
}

func TestRedactorRejectsUnboundedAndContextSensitivePatterns(t *testing.T) {
	for _, pattern := range []string{`secret=.*`, `^secret=[A-Z]{1,16}`, `a*`} {
		if _, err := NewRedactor([]string{pattern}); err == nil {
			t.Errorf("unsupported redaction pattern %q was accepted", pattern)
		}
	}
	if err := ValidatePatterns([]string{`token=[A-Z0-9]{1,64}`}); err != nil {
		t.Fatalf("valid bounded pattern rejected: %v", err)
	}
}

func TestLoggerAppliesConfiguredRedactionBeforeWriting(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	logger, err := NewWithOptions(directory, "api", Options{
		MaxBytes: 128, RetainedFiles: 3,
		RedactPatterns: []string{`(?i)token=[A-Z0-9]{1,16}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range [][]byte{[]byte("request token=ab"), []byte("c12345 complete\r\n")} {
		if err := logger.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "request [REDACTED] complete\r\n"; got != want {
		t.Fatalf("stored output = %q, want %q", got, want)
	}
	if strings.Contains(string(content), "token=abc12345") {
		t.Fatal("stored output still contains the secret token")
	}
}
