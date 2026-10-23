package main

// Layout primitives. Every screen is assembled from blocks of exactly w×h cells,
// so the frame never exceeds the terminal and never wraps.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// fit truncates (ANSI-aware) and pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return pad(s, w)
}

func pad(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// block forces exactly h lines of exactly w cells.
func block(lines []string, w, h int) []string {
	out := make([]string, max(h, 0))
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", max(w, 0))
		}
	}
	return out
}

// box draws a border with a centred title around body; returns exactly h lines of width w.
func box(body []string, w, h int, title string, border lipgloss.Style) []string {
	if h < 2 || w < 2 {
		return block(nil, w, h)
	}
	inner := w - 2
	t := ""
	if title != "" {
		t = " " + title + " "
		if lipgloss.Width(t) > inner {
			t = ansi.Truncate(t, inner, "…")
		}
	}
	tw := lipgloss.Width(t)
	left := max((inner-tw)/2, 0)
	right := max(inner-tw-left, 0)
	out := []string{border.Render("┌"+strings.Repeat("─", left)) + t + border.Render(strings.Repeat("─", right)+"┐")}
	for _, l := range block(body, inner, h-2) {
		out = append(out, border.Render("│")+l+border.Render("│"))
	}
	return append(out, border.Render("└"+strings.Repeat("─", inner)+"┘"))
}

// window returns the scroll offset that keeps cursor visible in a viewport of size h.
func window(cursor, offset, n, h int) int {
	if h <= 0 {
		return 0
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+h {
		offset = cursor - h + 1
	}
	if offset > n-h {
		offset = n - h
	}
	return max(offset, 0)
}

// solid re-renders a styled line with a single style (used for selection / flash rows).
func solid(line string, w int, st lipgloss.Style) string {
	return st.Render(fit(ansi.Strip(line), w))
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
