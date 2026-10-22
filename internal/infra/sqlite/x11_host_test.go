package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestSSHHostPersistsX11ForwardingSetting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "x11", IP: "127.0.0.1", User: "tester", X11Forwarding: true,
	})
	if err != nil {
		t.Fatalf("create SSH host: %v", err)
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil || !loaded.X11Forwarding {
		t.Fatalf("loaded X11 setting = %v, err=%v", loaded.X11Forwarding, err)
	}

	loaded.X11Forwarding = false
	if err := store.UpdateHost(ctx, loaded); err != nil {
		t.Fatalf("disable X11 forwarding: %v", err)
	}
	loaded, err = store.GetHost(ctx, host.Id)
	if err != nil || loaded.X11Forwarding {
		t.Fatalf("updated X11 setting = %v, err=%v; want disabled", loaded.X11Forwarding, err)
	}
}
