package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"tui-ssh-workbench/internal/model"
)

// Design brief: primary content is the host list, secondary content is group
// navigation and the selected host's details, and tertiary content is the
// shortcut/status row. The single primary emphasis is the current selection.
// Tokens: canvas #10151B, selected #24323C, line #2B3742, primary #E7EEF4,
// secondary #BEC8D1, muted #9AA7B2, accent #58D6C2, warning #E8B36D.
// Layout uses 1-cell rhythm and 2-cell
// separation between sections; color reinforces labels and never carries state alone.

type theme struct {
	canvas    color.Color
	selected  color.Color
	line      color.Color
	primary   color.Color
	secondary color.Color
	muted     color.Color
	accent    color.Color
	warning   color.Color
	success   color.Color
	danger    color.Color
}

var (
	palette          theme
	brandStyle       lipgloss.Style
	titleStyle       lipgloss.Style
	sectionStyle     lipgloss.Style
	secondaryStyle   lipgloss.Style
	mutedStyle       lipgloss.Style
	accentStyle      lipgloss.Style
	warningStyle     lipgloss.Style
	selectedStyle    lipgloss.Style
	selectedAccent   lipgloss.Style
	selectedSubtle   lipgloss.Style
	paneFocusStyle   lipgloss.Style
	focusBadgeStyle  lipgloss.Style
	focusLineStyle   lipgloss.Style
	separatorStyle   lipgloss.Style
	helpKeyStyle     lipgloss.Style
	statusErrorStyle lipgloss.Style
	sshOnlineStyle   lipgloss.Style
	sshOfflineStyle  lipgloss.Style
)

var appearanceSchemes = map[string]theme{
	model.AppearanceDefault: {
		canvas: lipgloss.Color("#10151B"), selected: lipgloss.Color("#24323C"), line: lipgloss.Color("#2B3742"),
		primary: lipgloss.Color("#E7EEF4"), secondary: lipgloss.Color("#BEC8D1"), muted: lipgloss.Color("#9AA7B2"),
		accent: lipgloss.Color("#58D6C2"), warning: lipgloss.Color("#E8B36D"),
		success: lipgloss.Color("#3FB950"), danger: lipgloss.Color("#F85149"),
	},
	model.AppearanceNord: {
		canvas: lipgloss.Color("#2E3440"), selected: lipgloss.Color("#3B4252"), line: lipgloss.Color("#4C566A"),
		primary: lipgloss.Color("#ECEFF4"), secondary: lipgloss.Color("#D8DEE9"), muted: lipgloss.Color("#9AA3B4"),
		accent: lipgloss.Color("#88C0D0"), warning: lipgloss.Color("#EBCB8B"),
		success: lipgloss.Color("#A3BE8C"), danger: lipgloss.Color("#BF616A"),
	},
	model.AppearanceDracula: {
		canvas: lipgloss.Color("#282A36"), selected: lipgloss.Color("#44475A"), line: lipgloss.Color("#6272A4"),
		primary: lipgloss.Color("#F8F8F2"), secondary: lipgloss.Color("#D8D6DD"), muted: lipgloss.Color("#A7A6B4"),
		accent: lipgloss.Color("#8BE9FD"), warning: lipgloss.Color("#FFB86C"),
		success: lipgloss.Color("#50FA7B"), danger: lipgloss.Color("#FF5555"),
	},
	model.AppearanceSolarized: {
		canvas: lipgloss.Color("#002B36"), selected: lipgloss.Color("#073642"), line: lipgloss.Color("#586E75"),
		primary: lipgloss.Color("#FDF6E3"), secondary: lipgloss.Color("#93A1A1"), muted: lipgloss.Color("#657B83"),
		accent: lipgloss.Color("#2AA198"), warning: lipgloss.Color("#CB4B16"),
		success: lipgloss.Color("#859900"), danger: lipgloss.Color("#DC322F"),
	},
	model.AppearanceHighContrast: {
		canvas: lipgloss.Color("#000000"), selected: lipgloss.Color("#303030"), line: lipgloss.Color("#FFFFFF"),
		primary: lipgloss.Color("#FFFFFF"), secondary: lipgloss.Color("#E0E0E0"), muted: lipgloss.Color("#B8B8B8"),
		accent: lipgloss.Color("#00FFFF"), warning: lipgloss.Color("#FFFF00"),
		success: lipgloss.Color("#00FF00"), danger: lipgloss.Color("#FF0000"),
	},
	model.AppearanceCatppuccin: {
		canvas: lipgloss.Color("#1E1E2E"), selected: lipgloss.Color("#313244"), line: lipgloss.Color("#45475A"),
		primary: lipgloss.Color("#CDD6F4"), secondary: lipgloss.Color("#BAC2DE"), muted: lipgloss.Color("#9399B2"),
		accent: lipgloss.Color("#89DCEB"), warning: lipgloss.Color("#F9E2AF"),
		success: lipgloss.Color("#A6E3A1"), danger: lipgloss.Color("#F38BA8"),
	},
	model.AppearanceTokyoNight: {
		canvas: lipgloss.Color("#1A1B26"), selected: lipgloss.Color("#292E42"), line: lipgloss.Color("#3B4261"),
		primary: lipgloss.Color("#C0CAF5"), secondary: lipgloss.Color("#A9B1D6"), muted: lipgloss.Color("#7982A9"),
		accent: lipgloss.Color("#7AA2F7"), warning: lipgloss.Color("#E0AF68"),
		success: lipgloss.Color("#9ECE6A"), danger: lipgloss.Color("#F7768E"),
	},
	model.AppearanceGruvbox: {
		canvas: lipgloss.Color("#282828"), selected: lipgloss.Color("#3C3836"), line: lipgloss.Color("#504945"),
		primary: lipgloss.Color("#EBDBB2"), secondary: lipgloss.Color("#D5C4A1"), muted: lipgloss.Color("#A89984"),
		accent: lipgloss.Color("#83A598"), warning: lipgloss.Color("#FABD2F"),
		success: lipgloss.Color("#B8BB26"), danger: lipgloss.Color("#FB4934"),
	},
	model.AppearanceOneDark: {
		canvas: lipgloss.Color("#282C34"), selected: lipgloss.Color("#353B45"), line: lipgloss.Color("#4B5263"),
		primary: lipgloss.Color("#ABB2BF"), secondary: lipgloss.Color("#C8CCD4"), muted: lipgloss.Color("#7F848E"),
		accent: lipgloss.Color("#61AFEF"), warning: lipgloss.Color("#E5C07B"),
		success: lipgloss.Color("#98C379"), danger: lipgloss.Color("#E06C75"),
	},
}

func init() {
	applyAppearance(model.DefaultConfig().Appearance)
}

func applyAppearance(config model.AppearanceConfig) {
	config = config.WithDefaults()
	selected, ok := appearanceSchemes[config.Scheme]
	if !ok {
		selected = appearanceSchemes[model.AppearanceDefault]
	}
	colors := config.Colors
	if colors.Canvas != "" {
		selected.canvas = lipgloss.Color(colors.Canvas)
	}
	if colors.Selected != "" {
		selected.selected = lipgloss.Color(colors.Selected)
	}
	if colors.Line != "" {
		selected.line = lipgloss.Color(colors.Line)
	}
	if colors.Primary != "" {
		selected.primary = lipgloss.Color(colors.Primary)
	}
	if colors.Secondary != "" {
		selected.secondary = lipgloss.Color(colors.Secondary)
	}
	if colors.Muted != "" {
		selected.muted = lipgloss.Color(colors.Muted)
	}
	if colors.Accent != "" {
		selected.accent = lipgloss.Color(colors.Accent)
	}
	if colors.Warning != "" {
		selected.warning = lipgloss.Color(colors.Warning)
	}
	palette = selected
	brandStyle = lipgloss.NewStyle().Foreground(palette.accent).Bold(true)
	titleStyle = lipgloss.NewStyle().Foreground(palette.primary).Bold(true)
	sectionStyle = lipgloss.NewStyle().Foreground(palette.secondary).Bold(true)
	secondaryStyle = lipgloss.NewStyle().Foreground(palette.secondary)
	mutedStyle = lipgloss.NewStyle().Foreground(palette.muted)
	accentStyle = lipgloss.NewStyle().Foreground(palette.accent)
	warningStyle = lipgloss.NewStyle().Foreground(palette.warning)
	selectedStyle = lipgloss.NewStyle().Foreground(palette.primary).Background(palette.selected)
	selectedAccent = lipgloss.NewStyle().Foreground(palette.accent).Background(palette.selected).Bold(true)
	selectedSubtle = lipgloss.NewStyle().Foreground(palette.secondary).Background(palette.selected)
	paneFocusStyle = lipgloss.NewStyle().Foreground(palette.accent).Background(palette.selected).Bold(true)
	focusBadgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F6C343")).Bold(true)
	focusLineStyle = lipgloss.NewStyle().Foreground(palette.accent).Bold(true)
	sshOnlineStyle = lipgloss.NewStyle().Foreground(palette.success).Bold(true)
	sshOfflineStyle = lipgloss.NewStyle().Foreground(palette.danger).Bold(true)
	separatorStyle = lipgloss.NewStyle().Foreground(palette.line)
	helpKeyStyle = lipgloss.NewStyle().Foreground(palette.accent).Bold(true)
	statusErrorStyle = lipgloss.NewStyle().Foreground(palette.warning)
}
