package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testPlugin struct {
	manifest Manifest
	register func(Registrar) error
}

func (p testPlugin) Manifest() Manifest { return p.manifest }
func (p testPlugin) Register(registrar Registrar) error {
	return p.register(registrar)
}

func validPlugin(id string, register func(Registrar) error) Plugin {
	return testPlugin{
		manifest: Manifest{ID: id, Name: id, Version: "1.0.0", APIVersion: APIVersion},
		register: register,
	}
}

func TestRegistryListsNamespacedCommandsAndReturnsOutput(t *testing.T) {
	registry, err := NewRegistry(
		validPlugin("zeta", func(registrar Registrar) error {
			return registrar.RegisterCommand("status", "show status", func(context.Context) (string, error) {
				return "ready", nil
			})
		}),
		validPlugin("alpha", func(registrar Registrar) error {
			return registrar.RegisterCommand("about", "show about", func(context.Context) (string, error) {
				return "version 1", nil
			})
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	commands := registry.Commands()
	if len(commands) != 2 || commands[0].FullName != "alpha.about" || commands[1].FullName != "zeta.status" {
		t.Fatalf("commands = %#v, want stable namespaced order", commands)
	}
	output, err := registry.Run(context.Background(), "alpha.about")
	if err != nil || output != "version 1" {
		t.Fatalf("Run() = (%q, %v), want (%q, nil)", output, err, "version 1")
	}
}

func TestRegistryRunHandlesErrorsCancellationAndPanics(t *testing.T) {
	registry, err := NewRegistry(validPlugin("test", func(registrar Registrar) error {
		if err := registrar.RegisterCommand("fail", "return an error", func(context.Context) (string, error) {
			return "", errors.New("expected failure")
		}); err != nil {
			return err
		}
		return registrar.RegisterCommand("panic", "panic safely", func(context.Context) (string, error) {
			panic("broken plugin")
		})
	}))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.Run(ctx, "test.fail"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run() error = %v, want context.Canceled", err)
	}
	if _, err := registry.Run(context.Background(), "test.fail"); err == nil || !strings.Contains(err.Error(), "expected failure") {
		t.Fatalf("handler error = %v, want wrapped expected failure", err)
	}
	if _, err := registry.Run(context.Background(), "test.panic"); err == nil || !strings.Contains(err.Error(), "panic: broken plugin") {
		t.Fatalf("panic error = %v, want recovered plugin panic", err)
	}
	if _, err := registry.Run(context.Background(), "test.missing"); err == nil {
		t.Fatal("unknown command unexpectedly ran")
	}
}

func TestRegistryRejectsPreviousAPIVersion(t *testing.T) {
	oldAPIPlugin := testPlugin{
		manifest: Manifest{ID: "old", Name: "Old", Version: "1.0.0", APIVersion: "v1"},
		register: func(Registrar) error { return nil },
	}
	if _, err := NewRegistry(oldAPIPlugin); err == nil || !strings.Contains(err.Error(), `API version "v1" is not supported`) {
		t.Fatalf("old API registration error = %v", err)
	}
}

func TestRegistryBoundsCommandOutput(t *testing.T) {
	registry, err := NewRegistry(validPlugin("test", func(registrar Registrar) error {
		return registrar.RegisterCommand("large", "large output", func(context.Context) (string, error) {
			return strings.Repeat("x", MaxCommandOutputBytes+32), nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	output, err := registry.Run(context.Background(), "test.large")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output, strings.Repeat("x", MaxCommandOutputBytes-1)) || !strings.Contains(output, "输出已截断") {
		t.Fatalf("bounded output marker missing: len=%d output suffix=%q", len(output), output[max(0, len(output)-30):])
	}
}
