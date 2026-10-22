package ui

import (
	"image/color"
	"math"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestNewModelAppliesConfiguredAppearance(t *testing.T) {
	t.Cleanup(func() { applyAppearance(model.DefaultConfig().Appearance) })
	config := model.DefaultConfig()
	config.Appearance = model.AppearanceConfig{
		Scheme: model.AppearanceNord,
		Colors: model.AppearanceColorConfig{Accent: "#FF00AA"},
	}
	_ = NewModelWithConfig(nil, config)
	if got := rgb(palette.accent); got != [3]uint8{255, 0, 170} {
		t.Fatalf("accent palette color = %v, want custom override [255 0 170]", got)
	}
	if got := rgb(palette.canvas); got != [3]uint8{46, 52, 64} {
		t.Fatalf("canvas palette color = %v, want nord scheme [46 52 64]", got)
	}
	if got, want := rgb(titleStyle.GetForeground()), rgb(palette.primary); got != want {
		t.Fatalf("title style foreground = %v, want %v", got, want)
	}
	if got, want := rgb(paneFocusStyle.GetBackground()), rgb(palette.selected); got != want {
		t.Fatalf("focused pane background = %v, want selected background %v", got, want)
	}
	if got, want := rgb(paneFocusStyle.GetForeground()), rgb(palette.accent); got != want {
		t.Fatalf("focused pane foreground = %v, want accent foreground %v", got, want)
	}
}

func TestDefaultAppearanceTextContrast(t *testing.T) {
	t.Cleanup(func() { applyAppearance(model.DefaultConfig().Appearance) })
	applyAppearance(model.AppearanceConfig{Scheme: model.AppearanceDefault})

	if ratio := contrastRatio(palette.muted, palette.canvas); ratio < 7 {
		t.Errorf("muted text contrast = %.2f:1, want at least 7:1 for compact TUI text", ratio)
	}
	if ratio := contrastRatio(palette.secondary, palette.canvas); ratio < 10 {
		t.Errorf("secondary text contrast = %.2f:1, want at least 10:1", ratio)
	}
}

func contrastRatio(foreground, background color.Color) float64 {
	return (math.Max(relativeLuminance(foreground), relativeLuminance(background)) + 0.05) /
		(math.Min(relativeLuminance(foreground), relativeLuminance(background)) + 0.05)
}

func relativeLuminance(value color.Color) float64 {
	red, green, blue, _ := value.RGBA()
	linear := func(channel uint32) float64 {
		component := float64(channel>>8) / 255
		if component <= 0.04045 {
			return component / 12.92
		}
		return math.Pow((component+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(red) + 0.7152*linear(green) + 0.0722*linear(blue)
}

func rgb(value color.Color) [3]uint8 {
	red, green, blue, _ := value.RGBA()
	return [3]uint8{uint8(red >> 8), uint8(green >> 8), uint8(blue >> 8)}
}
