package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationRegistersCoreRuntimePlugin(t *testing.T) {
	application, err := NewWithDatabase(context.Background(), filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	output, err := application.PluginRegistry().Run(context.Background(), "system.runtime")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Go 版本：") || !strings.Contains(output, "运行平台：") {
		t.Fatalf("runtime command output = %q", output)
	}
}
