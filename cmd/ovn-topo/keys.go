package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func isText(k tea.KeyMsg) bool { return k.Type == tea.KeyRunes || k.Type == tea.KeySpace }

func keyText(k tea.KeyMsg) string {
	if k.Type == tea.KeySpace && len(k.Runes) == 0 {
		return " "
	}
	return string(k.Runes)
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}

	// --- modal inputs first
	if m.cmdEdit {
		switch s {
		case "esc":
			m.cmdEdit, m.cmd = false, ""
		case "enter":
			m.cmdEdit = false
			return m.runCmd(strings.TrimSpace(m.cmd))
		case "tab":
			if sug := suggest(m.cmd); sug != "" {
				m.cmd = sug
			}
		case "backspace":
			if m.cmd != "" {
				m.cmd = m.cmd[:len(m.cmd)-1]
			}
		default:
			if isText(k) {
				m.cmd += keyText(k)
			}
		}
		return m, nil
	}
	if m.filterEdit {
		switch s {
		case "esc":
			m.filterEdit, m.filter = false, ""
		case "enter":
			m.filterEdit = false
		case "backspace":
			if m.filter != "" {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if isText(k) {
				m.filter += keyText(k)
			}
		}
		m.pcur, m.poff, m.offset = 0, 0, 0
		m.rebuildRows()
		return m, nil
	}
	if m.scr == scrHelp {
		m.scr = scrMain
		return m, nil
	}
	if m.scr == scrDescribe {
		switch s {
		case "esc", "q", "d", "backspace":
			m.scr, m.desc = scrMain, nil
		case "y":
			m.desc.raw = !m.desc.raw
			m.desc.off = 0
		case "j", "down":
			m.desc.off++
		case "k", "up":
			m.desc.off = max(m.desc.off-1, 0)
		case "ctrl+d", "pgdown":
			m.desc.off += m.bodyH() / 2
		case "ctrl+u", "pgup":
			m.desc.off = max(m.desc.off-m.bodyH()/2, 0)
		case "g":
			m.desc.off = 0
		case "G":
			m.desc.off = 1 << 20
		}
		if m.desc != nil {
			m.desc.off = min(m.desc.off, max(len(m.describeLines())-m.bodyH(), 0))
		}
		return m, nil
	}

	// --- global keys
	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.scr = scrHelp
		return m, nil
	case ":":
		m.cmdEdit, m.cmd = true, ""
		return m, nil
	case "/":
		m.filterEdit, m.filter = true, ""
		m.rebuildRows()
		return m, nil
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.rebuildRows()
		}
		return m, nil
	case "r":
		m.setFlash("refreshing…", false)
		return m, m.reloadCmd()
	case "t":
		m.themeIndex = (m.themeIndex + 1) % len(AvailableThemes)
		m.theme = AvailableThemes[m.themeIndex]
		m.styles = CompileStyles(m.theme)
		m.setFlash("theme: "+m.theme.Name, false)
		return m, nil
	case "L":
		if m.fixturePath != "" {
			m.setFlash("offline fixture: no live updates", false)
			return m, nil
		}
		m.livePaused = !m.livePaused
		if m.livePaused {
			m.setFlash("live updates paused", false)
			return m, nil
		}
		m.setFlash("live updates resumed", false)
		return m, m.fetchGraphCmd()
	case "b":
		m.back()
		return m, nil
	case "x":
		return m.openDiag()
	case "1":
		return m.runCmd("vpc")
	case "2":
		return m.runCmd("chassis")
	case "3":
		return m.runCmd("workload")
	case "4":
		return m.runCmd("ports")
	}

	if m.mode == modePorts {
		if res, cmd, ok := m.portKey(s); ok {
			return res, cmd
		}
		return m, nil
	}
	return m.treeKey(s)
}

func (m model) treeKey(s string) (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	switch s {
	case "j", "down":
		m.cursor++
	case "k", "up":
		m.cursor--
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.rows) - 1
	case "ctrl+d", "pgdown":
		m.cursor += m.bodyH() / 2
	case "ctrl+u", "pgup":
		m.cursor -= m.bodyH() / 2
	case "l", "right":
		if r != nil && r.hasKids && !r.ref {
			m.toggle(*r, true)
		}
	case "h", "left":
		if r == nil {
			break
		}
		if r.hasKids && !r.ref && m.isExpanded(*r) {
			m.toggle(*r, false)
			break
		}
		parent := r.key[:max(strings.LastIndex(r.key, "/"), 0)]
		for i, x := range m.rows {
			if x.key == parent {
				m.cursor = i
			}
		}
	case " ", "space":
		if r != nil && r.hasKids && !r.ref {
			m.toggle(*r, !m.isExpanded(*r))
		}
	case "enter":
		switch {
		case r == nil:
		case r.ref:
			m.gotoNode(r.n.id)
		case r.hasKids:
			m.toggle(*r, !m.isExpanded(*r))
		default:
			return m, m.openDescribe(r.n, false)
		}
	case "e", "E":
		for _, x := range m.rows {
			if x.hasKids && !x.ref {
				m.touched[x.key], m.expanded[x.key] = true, s == "e"
			}
		}
		m.rebuildRows()
	case "i":
		m.pinspect = !m.pinspect
		if m.w() < paneMinW {
			m.setFlash(fmt.Sprintf("the details pane needs a terminal at least %d columns wide", paneMinW), false)
		}
	case "d":
		if r != nil {
			return m, m.openDescribe(r.n, false)
		}
	case "y":
		if r != nil {
			return m, m.openDescribe(r.n, true)
		}
	}
	m.cursor = max(min(m.cursor, len(m.rows)-1), 0)
	m.offset = window(m.cursor, m.offset, len(m.rows), m.bodyH())
	return m, nil
}

func (m model) runCmd(c string) (tea.Model, tea.Cmd) {
	c = strings.TrimPrefix(c, "xray ")
	switch {
	case c == "":
		return m, nil
	case c == "vpc" || c == "l3":
		m.mode = modeVPC
	case c == "chassis" || c == "ch" || c == "node" || c == "nodes":
		m.mode = modeChassis
	case c == "workload" || c == "wl" || c == "ns" || c == "vm" || c == "pod" || c == "pods":
		m.mode = modeWorkload
	case c == "ports" || c == "port" || c == "lrp" || c == "matrix":
		m.mode = modePorts
		m.pcur, m.poff = 0, 0
	case c == "q" || c == "quit":
		return m, tea.Quit
	case strings.HasPrefix(c, "theme"):
		name := strings.TrimSpace(strings.TrimPrefix(c, "theme"))
		if name == "" {
			m.setFlash("themes: default, suse, dracula, nord, tokyonight, catppuccin", false)
			return m, nil
		}
		m.theme, m.themeIndex = GetThemeByName(name)
		m.styles = CompileStyles(m.theme)
		return m, nil
	default:
		m.setFlash(fmt.Sprintf("unknown command %q — try :xray vpc|chassis|workload, :ports, :theme <name>", c), true)
		return m, nil
	}
	m.filter, m.scr = "", scrMain
	m.cursor, m.offset = 0, 0
	m.rebuildRows()
	return m, nil
}

func (m model) openDiag() (tea.Model, tea.Cmd) {
	switch {
	case !m.diagMode:
		m.setFlash("Tier 1 diagnostics are off — run ovn-topo --diag (ADR-0001)", true)
	case !m.diagAvailable:
		m.setFlash("diagnostics unavailable: "+m.diagReason, true)
	default:
		t := m.resolveDiagTarget(m.selectedXNode())
		if !t.anyEnabled() {
			_, why := t.can(actTrace)
			m.setFlash("diagnostics: "+why, true)
			break
		}
		m.diagTgt, m.nicIdx = t, 0
		m.diagModal = true
		m.diagStep = diagStepMenu
		m.diagRawOutput = ""
		m.diagTraceRes = nil
		m.diagError = nil
		m.diagRunning = false
		m.diagRawView = false
		m.diagScrollOffset = 0
	}
	return m, nil
}

// reloadCmd refetches everything (graph, every switch's ports, chassis).
func (m model) reloadCmd() tea.Cmd {
	m.graphResp = nil // force a full fetch (no If-None-Match)
	return m.fetchGraphCmd()
}
