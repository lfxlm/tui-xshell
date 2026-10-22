//go:build windows

package localterminal

import (
	"strings"
	"testing"
	"time"
)

func TestWindowsShellSelection(t *testing.T) {
	t.Setenv("SHELL", `C:\Program Files\Git\bin\bash.exe`)
	t.Setenv("COMSPEC", `C:\Windows\System32\cmd.exe`)
	shell, args := windowsShell()
	if shell != `C:\Program Files\Git\bin\bash.exe` || strings.Join(args, " ") != "-l" {
		t.Fatalf("bash selection = %q %v", shell, args)
	}

	t.Setenv("SHELL", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`)
	shell, args = windowsShell()
	if shell != `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` || strings.Join(args, " ") != "-NoLogo" {
		t.Fatalf("PowerShell selection = %q %v", shell, args)
	}

	t.Setenv("SHELL", "")
	shell, args = windowsShell()
	if shell != `C:\Windows\System32\cmd.exe` || len(args) != 0 {
		t.Fatalf("cmd selection = %q %v", shell, args)
	}
}

func TestWindowsConPTYInteractiveShell(t *testing.T) {
	session, err := Start(80, 24)
	if err != nil {
		t.Fatalf("start local shell: %v", err)
	}
	defer session.Close()
	if err := session.Resize(100, 36); err != nil {
		t.Fatalf("resize local shell: %v", err)
	}
	if err := session.Write([]byte("echo TUI_SSH_WORKBENCH_PTY_OK\r\nexit\r\n")); err != nil {
		t.Fatalf("write local shell: %v", err)
	}
	readResult := make(chan string, 1)
	readError := make(chan error, 1)
	go func() {
		var output strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := session.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				if strings.Count(output.String(), "TUI_SSH_WORKBENCH_PTY_OK") >= 2 {
					readResult <- output.String()
					return
				}
			}
			if err != nil {
				readError <- err
				return
			}
		}
	}()
	select {
	case output := <-readResult:
		if !strings.Contains(output, "TUI_SSH_WORKBENCH_PTY_OK") {
			t.Fatalf("shell output = %q", output)
		}
	case err := <-readError:
		t.Fatalf("read local shell: %v", err)
	case <-time.After(15 * time.Second):
		_ = session.Close()
		t.Fatal("timed out waiting for interactive shell output")
	}
}
