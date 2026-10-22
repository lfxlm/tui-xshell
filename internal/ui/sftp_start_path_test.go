package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLocalStartDir(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	currentDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("read temporary working directory: %v", err)
	}
	got, err := resolveLocalStartDir("")
	if err != nil || got != currentDirectory {
		t.Fatalf("empty starting directory = %q, %v; want %q", got, err, currentDirectory)
	}
	got, err = resolveLocalStartDir("./downloads/../files")
	if err != nil || got != filepath.Join(currentDirectory, "files") {
		t.Fatalf("relative starting directory = %q, %v; want cleaned path under working directory", got, err)
	}

	home := filepath.Join(temporaryDirectory, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create fake home: %v", err)
	}
	t.Setenv("HOME", home)
	got, err = resolveLocalStartDir("~/incoming")
	if err != nil || got != filepath.Join(home, "incoming") {
		t.Fatalf("home-relative starting directory = %q, %v; want path under home", got, err)
	}
}

func TestCleanRemoteStartDir(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		configured string
		want       string
	}{
		{name: "default working directory", base: "/home/operator", want: "/home/operator"},
		{name: "relative path", base: "/home/operator", configured: "projects/../logs", want: "/home/operator/logs"},
		{name: "absolute path", base: "/home/operator", configured: "/var/log", want: "/var/log"},
		{name: "relative base", base: ".", configured: "files", want: "files"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanRemoteStartDir(test.base, test.configured); got != test.want {
				t.Fatalf("cleanRemoteStartDir(%q, %q) = %q, want %q", test.base, test.configured, got, test.want)
			}
		})
	}
}
