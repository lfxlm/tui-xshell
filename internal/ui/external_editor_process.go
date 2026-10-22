package ui

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func runExternalEditor(command *exec.Cmd, complete func(error) tea.Msg) tea.Cmd {
	if !isGUIEditorCommand(command.Path) {
		return tea.ExecProcess(command, complete)
	}
	return func() tea.Msg {
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		err := command.Run()
		if err != nil && output.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
		}
		return complete(err)
	}
}

func isGUIEditorCommand(commandPath string) bool {
	switch strings.ToLower(filepath.Base(commandPath)) {
	case "code", "code-insiders", "goland", "goland.sh", "open", "cursor", "zed", "subl":
		return true
	default:
		return false
	}
}
