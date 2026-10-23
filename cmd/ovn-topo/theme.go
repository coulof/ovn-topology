package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme defines the color palette for the terminal UI.
type Theme struct {
	Name      string
	TitleBg   lipgloss.Color
	TitleFg   lipgloss.Color
	Primary   lipgloss.Color // Main selection, active tab & borders
	Header    lipgloss.Color // Section headers
	Text      lipgloss.Color // Primary text
	Muted     lipgloss.Color // Subtitle & secondary info
	Border    lipgloss.Color // Box borders & lines
	BoxBg     lipgloss.Color // Background for panels and pills
	Success   lipgloss.Color // Bound ports
	Error     lipgloss.Color // Unbound ports
	Warning   lipgloss.Color // Gateways & EIPs (persimmon #fe7c3f)
	CIDR      lipgloss.Color // Network IP addresses
	PortBadge lipgloss.Color // Port name badge
}

// ThemeStyles contains the compiled Lip Gloss styles derived from a Theme.
type ThemeStyles struct {
	Title             lipgloss.Style
	TabActive         lipgloss.Style
	TabInactive       lipgloss.Style
	Header            lipgloss.Style
	VPCBox            lipgloss.Style
	DetailBox         lipgloss.Style
	InspectorBox      lipgloss.Style
	Node              lipgloss.Style
	SelectedNode      lipgloss.Style
	EdgeLine          lipgloss.Style
	CIDR              lipgloss.Style
	PortBadge         lipgloss.Style
	BadgeBound        lipgloss.Style
	BadgeUnbound      lipgloss.Style
	ActiveGateway     lipgloss.Style
	TableHeader       lipgloss.Style
	TableRow          lipgloss.Style
	TableRowSelected  lipgloss.Style
	MutedText         lipgloss.Style
	DiagBadgeActive   lipgloss.Style
	DiagBadgeDisabled lipgloss.Style
	ModalBox          lipgloss.Style

	// k9s-style shell (xray / tables)
	Border   lipgloss.Style
	Selected lipgloss.Style
	Flash    lipgloss.Style
	Key      lipgloss.Style
	Label    lipgloss.Style
	Value    lipgloss.Style
	Faint    lipgloss.Style
	Ok       lipgloss.Style
	Warn     lipgloss.Style
	Bad      lipgloss.Style
	Kind     map[string]lipgloss.Style
	Pill     lipgloss.Style
	PillAlt  lipgloss.Style
}

// Built-in color themes
var (
	ThemeDefault = Theme{
		Name:      "Default (Rancher)",
		TitleBg:   lipgloss.Color("#0070F3"),
		TitleFg:   lipgloss.Color("#FAFAFA"),
		Primary:   lipgloss.Color("#7D56F4"),
		Header:    lipgloss.Color("#00A86B"),
		Text:      lipgloss.Color("#FAFAFA"),
		Muted:     lipgloss.Color("#888888"),
		Border:    lipgloss.Color("#555555"),
		BoxBg:     lipgloss.Color("#1A1A1A"),
		Success:   lipgloss.Color("#00A86B"),
		Error:     lipgloss.Color("#FF453A"),
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#00C7BE"),
		PortBadge: lipgloss.Color("#FF9500"),
	}

	ThemeDracula = Theme{
		Name:      "Dracula",
		TitleBg:   lipgloss.Color("#bd93f9"),
		TitleFg:   lipgloss.Color("#282a36"),
		Primary:   lipgloss.Color("#bd93f9"), // Purple
		Header:    lipgloss.Color("#50fa7b"), // Green
		Text:      lipgloss.Color("#f8f8f2"),
		Muted:     lipgloss.Color("#6272a4"), // Comment grey
		Border:    lipgloss.Color("#44475a"),
		BoxBg:     lipgloss.Color("#21222c"),
		Success:   lipgloss.Color("#50fa7b"),
		Error:     lipgloss.Color("#ff5555"), // Red
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#8be9fd"), // Cyan
		PortBadge: lipgloss.Color("#ffb86c"), // Orange
	}

	ThemeNord = Theme{
		Name:      "Nord",
		TitleBg:   lipgloss.Color("#88c0d0"), // Frost blue
		TitleFg:   lipgloss.Color("#2e3440"), // Polar night
		Primary:   lipgloss.Color("#88c0d0"),
		Header:    lipgloss.Color("#a3be8c"), // Aurora green
		Text:      lipgloss.Color("#eceff4"), // Snow storm
		Muted:     lipgloss.Color("#4c566a"),
		Border:    lipgloss.Color("#434c5e"),
		BoxBg:     lipgloss.Color("#2e3440"),
		Success:   lipgloss.Color("#a3be8c"),
		Error:     lipgloss.Color("#bf616a"), // Aurora red
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#8fbcbb"), // Frost teal
		PortBadge: lipgloss.Color("#ebcb8b"), // Aurora yellow
	}

	ThemeTokyoNight = Theme{
		Name:      "Tokyo Night",
		TitleBg:   lipgloss.Color("#7aa2f7"),
		TitleFg:   lipgloss.Color("#1a1b26"),
		Primary:   lipgloss.Color("#7aa2f7"), // Blue
		Header:    lipgloss.Color("#9ece6a"), // Green
		Text:      lipgloss.Color("#c0caf5"),
		Muted:     lipgloss.Color("#565f89"),
		Border:    lipgloss.Color("#414868"),
		BoxBg:     lipgloss.Color("#16161e"),
		Success:   lipgloss.Color("#9ece6a"),
		Error:     lipgloss.Color("#f7768e"), // Red
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#7dcfff"), // Cyan
		PortBadge: lipgloss.Color("#ff9e64"), // Orange
	}

	ThemeCatppuccin = Theme{
		Name:      "Catppuccin Mocha",
		TitleBg:   lipgloss.Color("#cba6f7"), // Mauve
		TitleFg:   lipgloss.Color("#11111b"), // Crust
		Primary:   lipgloss.Color("#cba6f7"),
		Header:    lipgloss.Color("#a6e3a1"), // Green
		Text:      lipgloss.Color("#cdd6f4"), // Text
		Muted:     lipgloss.Color("#6c7086"), // Overlay
		Border:    lipgloss.Color("#45475a"), // Surface
		BoxBg:     lipgloss.Color("#181825"), // Mantle
		Success:   lipgloss.Color("#a6e3a1"),
		Error:     lipgloss.Color("#f38ba8"), // Red
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#89dceb"), // Sky
		PortBadge: lipgloss.Color("#fab387"), // Peach
	}

	ThemeSUSE = Theme{
		Name:      "SUSE",
		TitleBg:   lipgloss.Color("#30ba78"), // Jungle
		TitleFg:   lipgloss.Color("#ffffff"), // White
		Primary:   lipgloss.Color("#30ba78"), // Jungle
		Header:    lipgloss.Color("#30ba78"), // Jungle
		Text:      lipgloss.Color("#ffffff"), // White
		Muted:     lipgloss.Color("#90ebcd"), // Mint
		Border:    lipgloss.Color("#192072"), // Midnight
		BoxBg:     lipgloss.Color("#0c322c"), // Pine
		Success:   lipgloss.Color("#30ba78"), // Jungle
		Error:     lipgloss.Color("#fe7c3f"), // Persimmon
		Warning:   lipgloss.Color("#fe7c3f"), // Persimmon
		CIDR:      lipgloss.Color("#90ebcd"), // Mint
		PortBadge: lipgloss.Color("#2453ff"), // Waterhole
	}

	AvailableThemes = []Theme{
		ThemeDefault,
		ThemeSUSE,
		ThemeDracula,
		ThemeNord,
		ThemeTokyoNight,
		ThemeCatppuccin,
	}
)

// GetThemeByName returns the theme matching the given string name (case-insensitive).
func GetThemeByName(name string) (Theme, int) {
	lower := strings.ToLower(strings.TrimSpace(name))
	for i, t := range AvailableThemes {
		tName := strings.ToLower(t.Name)
		if tName == lower || strings.HasPrefix(tName, lower) {
			return t, i
		}
	}
	return ThemeDefault, 0
}

// CompileStyles generates Lip Gloss styles from a Theme palette.
func CompileStyles(t Theme) ThemeStyles {
	fg := func(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	s := baseStyles(t)
	s.Border = fg(t.Primary)
	s.Selected = lipgloss.NewStyle().Bold(true).Foreground(t.TitleFg).Background(t.Primary)
	s.Flash = lipgloss.NewStyle().Foreground(t.TitleFg).Background(t.Header)
	s.Key = fg(t.PortBadge).Bold(true)
	s.Label = fg(t.Warning).Bold(true)
	s.Value = fg(t.Text).Bold(true)
	s.Faint = fg(t.Border)
	s.Ok = fg(t.Success)
	s.Warn = fg(t.Warning)
	s.Bad = fg(t.Error)
	s.Pill = lipgloss.NewStyle().Bold(true).Foreground(t.TitleFg).Background(t.Warning)
	s.PillAlt = lipgloss.NewStyle().Bold(true).Foreground(t.TitleFg).Background(t.Primary)
	s.Kind = map[string]lipgloss.Style{
		kGroup:   fg(t.Text).Bold(true),
		kVPC:     fg(t.Primary).Bold(true),
		kLR:      fg(t.Primary),
		kLRP:     fg(t.PortBadge),
		kLS:      fg(t.CIDR),
		kLSP:     fg(t.Text),
		kChassis: fg(t.Warning),
		kExt:     fg(t.Error),
		kNS:      fg(t.Muted),
		kWL:      fg(t.Success),
	}
	return s
}

func baseStyles(t Theme) ThemeStyles {
	return ThemeStyles{
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.TitleFg).
			Background(t.TitleBg).
			Padding(0, 1),

		TabActive: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.TitleFg).
			Background(t.Primary).
			Padding(0, 1).
			MarginRight(1),

		TabInactive: lipgloss.NewStyle().
			Foreground(t.Muted).
			Background(t.BoxBg).
			Padding(0, 1).
			MarginRight(1),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Header).
			MarginBottom(1),

		VPCBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Padding(0, 1).
			Margin(0, 1, 1, 0),

		DetailBox: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(t.Border).
			Padding(0, 1).
			Width(52),

		InspectorBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Padding(0, 1).
			MarginTop(1),

		Node: lipgloss.NewStyle().
			Padding(0, 1),

		SelectedNode: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.TitleFg).
			Background(t.Primary).
			Padding(0, 1),

		EdgeLine: lipgloss.NewStyle().
			Foreground(t.Border),

		CIDR: lipgloss.NewStyle().
			Foreground(t.CIDR).
			Bold(true),

		PortBadge: lipgloss.NewStyle().
			Foreground(t.PortBadge),

		BadgeBound: lipgloss.NewStyle().
			Foreground(t.Success),

		BadgeUnbound: lipgloss.NewStyle().
			Foreground(t.Error),

		ActiveGateway: lipgloss.NewStyle().
			Foreground(t.Warning).
			Bold(true),

		TableHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Text).
			Background(t.BoxBg).
			Padding(0, 1),

		TableRow: lipgloss.NewStyle().
			Padding(0, 1),

		TableRowSelected: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.TitleFg).
			Background(t.Primary).
			Padding(0, 1),

		MutedText: lipgloss.NewStyle().
			Foreground(t.Muted),

		DiagBadgeActive: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(t.Warning). // Persimmon
			Padding(0, 1),

		DiagBadgeDisabled: lipgloss.NewStyle().
			Foreground(t.Muted).
			Background(t.BoxBg).
			Padding(0, 1),

		ModalBox: lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(t.Warning).
			Padding(1, 2),
	}
}
