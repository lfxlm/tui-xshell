package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAppearanceSchemePreference(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "appearance.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if scheme, err := store.AppearanceScheme(ctx); err != nil || scheme != "" {
		t.Fatalf("default appearance scheme = %q, err=%v; want unset", scheme, err)
	}
	if err := store.SetAppearanceScheme(ctx, "catppuccin-mocha"); err != nil {
		t.Fatalf("save appearance scheme: %v", err)
	}
	if scheme, err := store.AppearanceScheme(ctx); err != nil || scheme != "catppuccin-mocha" {
		t.Fatalf("saved appearance scheme = %q, err=%v", scheme, err)
	}
	if err := store.SetAppearanceScheme(ctx, "not-a-theme"); err == nil {
		t.Fatal("unsupported appearance scheme unexpectedly saved")
	}
}
