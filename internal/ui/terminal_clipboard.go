package ui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type systemClipboardCommand struct {
	name string
	args []string
}

func systemClipboardCommands(goos string) []systemClipboardCommand {
	switch goos {
	case "darwin":
		return []systemClipboardCommand{{name: "pbcopy"}}
	case "windows":
		return []systemClipboardCommand{{name: "clip"}}
	case "linux":
		return []systemClipboardCommand{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard", "-in"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	default:
		return nil
	}
}

func terminalClipboardCopyLabel(goos string) string {
	if goos == "darwin" {
		return "⌘⇧C"
	}
	return "Ctrl+Shift+C"
}

func terminalClipboardCopyShortcutLabel() string {
	return terminalClipboardCopyLabel(runtime.GOOS)
}

func terminalClipboardPasteShortcutLabel() string {
	if runtime.GOOS == "darwin" {
		return "⌘V"
	}
	return "Ctrl+Shift+V"
}

func writeTerminalSystemClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		return terminalClipboardCopyCompletedMsg{
			text: text,
			err:  writeSystemClipboard(runtime.GOOS, text, runSystemClipboardCommand),
		}
	}
}

func writeSystemClipboard(goos, text string, run func(context.Context, systemClipboardCommand, string) error) error {
	commands := systemClipboardCommands(goos)
	if len(commands) == 0 {
		return fmt.Errorf("native clipboard is unsupported on %s", goos)
	}

	var failures []error
	for _, command := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := run(ctx, command, text)
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", command.name, err))
	}
	return errors.Join(failures...)
}

func runSystemClipboardCommand(ctx context.Context, command systemClipboardCommand, text string) error {
	process := exec.CommandContext(ctx, command.name, command.args...)
	process.Stdin = strings.NewReader(text)
	var stderr strings.Builder
	process.Stderr = &stderr
	if err := process.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%w (%s)", err, detail)
		}
		return err
	}
	return nil
}
