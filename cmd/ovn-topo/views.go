package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	scrMain screen = iota
	scrDescribe
	scrHelp
)

const flashFor = 3 * time.Second

func (m model) w() int { return m.width }

func (m model) headerH() int {
	if m.height < 18 {
		return 1
	}
	return 5
}

// areaH is the height of the main box (borders included).
func (m model) areaH() int {
	h := m.height - m.headerH() - 1
	if m.cmdEdit {
		h -= 3
	}
	return max(h, 3)
}

// bodyH is the number of content rows inside the main box.
func (m model) bodyH() int { return max(m.areaH()-2, 1) }

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	var out []string
	out = append(out, m.header()...)
	if m.cmdEdit {
		out = append(out, m.prompt()...)
	}
	switch {
	case m.diagModal:
		modal := strings.Split(m.renderDiagModal(), "\n")
		out = append(out, block(modal, m.w(), m.areaH())...)
	case m.scr == scrHelp:
		out = append(out, m.helpView()...)
	case m.scr == scrDescribe:
		out = append(out, m.describeView()...)
	case m.graphResp == nil:
		out = append(out, m.stateView()...)
	case m.mode == modePorts:
		out = append(out, m.portsView(m.areaH())...)
	default:
		out = append(out, m.treeView()...)
	}
	out = append(out, m.crumbs())
	return strings.Join(block(out, m.w(), m.height), "\n")
}

func (m model) liveLabel() string {
	st := m.styles
	switch {
	case m.fixturePath != "":
		return st.MutedText.Render("○ offline fixture")
	case m.livePaused:
		return st.Warn.Render("‖ paused (L)")
	case m.live == liveSSE:
		return st.Ok.Render("● live")
	case m.live == livePolling:
		return st.Warn.Render("◌ polling")
	}
	return st.MutedText.Render("… connecting")
}

func (m model) diagLabel() string {
	st := m.styles
	switch {
	case !m.diagMode:
		return st.MutedText.Render("off (--diag)")
	case m.diagAvailable:
		return st.DiagBadgeActive.Render("DIAG") + " " + st.Ok.Render("ready")
	case m.diagExecutor == nil:
		return st.DiagBadgeDisabled.Render("DIAG") + " " + st.Bad.Render(m.diagReason)
	}
	return st.DiagBadgeDisabled.Render("DIAG") + " " + st.Warn.Render(firstNonEmpty(m.diagReason, "probing…"))
}

func (m model) hotkeys() [][2]string {
	keys := [][2]string{{"<:>", "command"}, {"</>", "filter"}, {"<1-4>", "vpc/ch/wl/ports"}}
	if m.mode == modePorts {
		keys = append(keys, [2]string{"<s/S>", "sort"}, [2]string{"<i>", "inspector"}, [2]string{"<enter>", "open in xray"})
	} else {
		keys = append(keys, [2]string{"<h/l>", "fold/unfold"}, [2]string{"<enter>", "toggle/goto"}, [2]string{"<e/E>", "expand/fold"},
			[2]string{"<i>", "details pane"})
	}
	keys = append(keys, [2]string{"<d>", "describe"}, [2]string{"<y>", "raw"}, [2]string{"<b>", "back"})
	if m.diagMode {
		keys = append(keys, [2]string{"<x>", "diagnostics"})
	}
	keys = append(keys, [2]string{"<r>", "refresh"}, [2]string{"<L>", "live on/off"}, [2]string{"<t>", "theme"},
		[2]string{"<?>", "help"}, [2]string{"<q>", "quit"})
	return keys
}

var logo = []string{
	` ___ _ _ _ _    _____ ___  ___  ___ `,
	`/ _ \ | | ' \  |_   _/ _ \| _ \/ _ \`,
	`\___/\_/|_||_|   |_| \___/|  _/\___/`,
	`                          |_|       `,
	``,
}

func (m model) header() []string {
	st := m.styles
	daemon := m.daemonURL
	if m.fixturePath != "" {
		daemon = "fixture: " + m.fixturePath
	}
	rev, topo := "—", "loading…"
	if m.graphResp != nil {
		rev = fmt.Sprintf("%d", m.graphResp.Revision)
	}
	if m.tree != nil {
		s := m.tree.stats
		topo = fmt.Sprintf("%d VPC · %d LS · %d LSP · %d chassis", s.vpcs, s.switches, s.ports, s.chassis)
	}
	if m.height < 18 { // compact single-line header
		return []string{" " + st.Label.Render("ovn-topo") + " " + st.MutedText.Render(daemon) + "  rev " + rev + " " + m.liveLabel() + "  " + st.MutedText.Render("? help")}
	}
	info := [][2]string{
		{"Daemon:", daemon},
		{"Revision:", rev + "  " + m.liveLabel()},
		{"Topology:", topo},
		{"Diag:", m.diagLabel()},
		{"Theme:", m.theme.Name},
	}
	const infoW = 46
	keys := m.hotkeys()
	cols := max((m.w()-infoW-2)/24, 1)
	logoFits := m.w() >= infoW+2+cols*24+38
	if logoFits {
		cols = max((m.w()-infoW-2-38)/24, 1)
	}
	var out []string
	for i := 0; i < 5; i++ {
		line := " " + st.Label.Render(pad(info[i][0], 10)) + fit(st.Value.Render(info[i][1]), infoW-11)
		line = pad(line, infoW) + "  "
		for c := 0; c < cols; c++ {
			idx := c*5 + i
			if idx < len(keys) {
				line += st.Key.Render(pad(keys[idx][0], 8)) + st.MutedText.Render(pad(keys[idx][1], 16))
			}
		}
		if logoFits {
			line = pad(line, m.w()-38) + st.Label.Render(logo[i])
		}
		out = append(out, line)
	}
	return out
}

func (m model) crumbs() string {
	st := m.styles
	pill := func(s string, style lipgloss.Style) string { return style.Render(" " + s + " ") }
	first := "xray"
	if m.mode == modePorts {
		first = "table"
	}
	c := pill(first, st.Pill) + " " + pill(string(m.mode), st.PillAlt)
	switch {
	case m.diagModal:
		c += " " + pill("diagnostics", st.DiagBadgeActive)
	case m.scr == scrDescribe:
		c += " " + pill("describe", st.PillAlt)
	case m.scr == scrHelp:
		c += " " + pill("help", st.PillAlt)
	}
	if n := m.selectedXNode(); n != nil && m.scr == scrMain && !m.diagModal {
		c += " " + st.MutedText.Render(n.id)
	}
	if m.flash != "" && time.Since(m.flashAt) < 6*time.Second {
		fs := st.CIDR
		if m.flashErr {
			fs = st.Bad
		}
		room := m.w() - lipgloss.Width(c) - 2
		if room > 10 {
			c = pad(c, m.w()-min(lipgloss.Width(m.flash), room)-1) + fit(fs.Render(m.flash), min(lipgloss.Width(m.flash), room))
		}
	}
	return c
}

func (m model) prompt() []string {
	st := m.styles
	sug := suggest(m.cmd)
	ghost := ""
	if sug != "" {
		ghost = st.Faint.Render(strings.TrimPrefix(sug, m.cmd))
	}
	body := []string{" " + st.CIDR.Render("▶ ") + m.cmd + ghost + lipgloss.NewStyle().Reverse(true).Render(" ") +
		st.MutedText.Render("   tab: complete · enter: run · esc: cancel")}
	return box(body, m.w(), 3, "", st.Border)
}

func (m model) filterTitle() string {
	if m.filter == "" && !m.filterEdit {
		return ""
	}
	cur := ""
	if m.filterEdit {
		cur = "▏"
	}
	return " " + m.styles.Warn.Render("</"+m.filter+cur+">")
}

func (m model) glyph(h health) string {
	switch h {
	case hOK:
		return m.styles.Ok.Render("●")
	case hWarn:
		return m.styles.Warn.Render("!")
	case hBad:
		return m.styles.Bad.Render("✗")
	}
	return m.styles.Faint.Render("·")
}

var kindTag = map[string]string{
	kVPC: "VPC", kLR: "LR", kLRP: "LRP", kLS: "LS", kLSP: "LSP", kChassis: "CH", kExt: "EXT", kNS: "NS",
}

func (m model) kind(n *xnode) string {
	tag := kindTag[n.kind]
	if n.kind == kWL {
		tag = map[string]string{"VirtualMachine": "VM", "Pod": "POD", "Node": "NODE"}[n.wlKind]
		if tag == "" {
			tag = strings.ToUpper(n.wlKind)
		}
	}
	return m.styles.Kind[n.kind].Bold(true).Render(pad(tag, 4))
}

func (m model) badge(n *xnode) string {
	if n.badge == "" {
		return ""
	}
	st := m.styles.Ok
	switch {
	case strings.HasPrefix(n.badge, "GW"), n.badge == "gateway":
		st = m.styles.ActiveGateway
	case strings.HasPrefix(n.badge, "shared"), n.badge == "default":
		st = m.styles.CIDR
	case n.health == hBad:
		st = m.styles.Bad
	case n.health == hWarn:
		st = m.styles.Warn
	}
	return st.Render("[" + n.badge + "]")
}

func (m model) treeView() []string {
	st := m.styles
	tw := m.treeW()
	inner := tw - 2
	h := m.bodyH()
	off := window(m.cursor, m.offset, len(m.rows), h)
	var body []string
	for i := off; i < min(off+h, len(m.rows)); i++ {
		r := m.rows[i]
		var line string
		if r.ref {
			line = " " + st.Faint.Render(r.prefix) + st.MutedText.Render(r.relLabel+" ⇢ ") + m.kind(r.n) + " " +
				st.Kind[r.n.kind].Render(r.n.name) + " " + m.badge(r.n)
		} else {
			fold := "  "
			if r.hasKids {
				fold = st.MutedText.Render("▸ ")
				if m.isExpanded(r) || m.filter != "" && len(r.n.children) > 0 {
					fold = st.MutedText.Render("▾ ")
				}
			}
			name := st.Value.Render(r.n.name)
			if r.n.kind == kGroup {
				name = st.Value.Render(strings.ToUpper(r.n.name)) + st.MutedText.Render(fmt.Sprintf(" (%d)", len(r.n.children)))
			}
			g := m.glyph(r.n.health) + " "
			if r.n.kind == kGroup {
				g = ""
			}
			summary := r.n.summary
			if m.showPane() && (r.n.kind == kLSP || r.n.kind == kLRP) {
				summary = "" // the detail pane carries it; keep the tree calm
			}
			line = " " + st.Faint.Render(r.prefix) + fold + g + m.kind(r.n) + " " + name + "  " +
				st.MutedText.Render(summary) + " " + m.badge(r.n)
		}
		if t, ok := m.changed[r.n.id]; ok && time.Since(t) < flashFor {
			line = solid(line, inner, st.Flash)
		}
		if i == m.cursor {
			line = solid(line, inner, st.Selected)
		}
		body = append(body, line)
	}
	if len(m.rows) == 0 {
		msg := "   nothing to show in this xray yet"
		if m.filter != "" {
			msg = "   nothing matches “" + m.filter + "”  (esc clears the filter)"
		} else if m.mode == modeChassis {
			msg = "   no chassis reported by the daemon (view=chassis is empty)"
		} else if m.mode == modeWorkload {
			msg = "   no Kubernetes workloads resolved from LSP external_ids"
		}
		body = append(body, "", st.MutedText.Render(msg))
	}
	title := st.Label.Render("Xray") + st.CIDR.Render("("+string(m.mode)+")") + st.MutedText.Render(fmt.Sprintf("[%d]", len(m.rows))) + m.filterTitle()
	tree := box(body, tw, m.areaH(), title, st.Border)
	if !m.showPane() {
		return tree
	}
	pane := m.detailPane(m.areaH())
	for i := range tree {
		tree[i] += pane[i]
	}
	return tree
}

// stateView covers loading and "daemon unreachable" before the first graph arrives.
func (m model) stateView() []string {
	st := m.styles
	var body []string
	title := st.Label.Render("Loading")
	if m.err != nil {
		title = st.Bad.Render("Daemon unreachable")
		body = []string{""}
		for i, l := range strings.Split(m.err.Error(), "\n") {
			if i == 0 {
				body = append(body, "  "+st.Bad.Render("✗ ")+l)
			} else {
				body = append(body, "    "+st.Warn.Render(l))
			}
		}
		body = append(body, "",
			"  Check that ovn-topologyd is running and reachable at "+st.CIDR.Render(m.daemonURL)+", e.g.:",
			"    kubectl -n kube-system port-forward svc/ovn-topologyd 8080:8080",
			"  or explore the bundled dataset offline with "+st.CIDR.Render("ovn-topo --fixture")+".", "",
			"  "+st.Key.Render("<r>")+" retry   "+st.Key.Render("<q>")+" quit",
		)
	} else {
		body = []string{"", "  Loading topology from " + st.CIDR.Render(firstNonEmpty(m.fixturePath, m.daemonURL)) + " …"}
	}
	return box(body, m.w(), m.areaH(), title, st.Border)
}

func (m model) helpView() []string {
	st := m.styles
	b := st.Value.Render
	left := []string{
		" " + b("XRAY ROOTS"),
		"   1  :xray vpc       VPC → LR → LRP · LS → LSP",
		"   2  :xray chassis   chassis → bound ports",
		"   3  :xray workload  namespace → VM/Pod → NICs",
		"   4  :ports          router-port matrix (table)",
		"",
		" " + b("INSPECT"),
		"   i  details pane (xray) / inspector (:ports)",
		"   d  describe (/node detail, external_ids)",
		"   y  raw columns",
		"   x  Tier 1 diagnostics (--diag, ADR-0001)",
	}
	right := []string{
		" " + b("NAVIGATION"),
		"   j/k ↑/↓   move            g/G    top/bottom",
		"   h/l ←/→   fold / unfold   ^d/^u  half page",
		"   space     toggle          e/E    expand/fold all",
		"   enter     toggle · on a ⇢ relation: jump there",
		"   b         back after a jump",
		" " + b("FIND & TABLE"),
		"   /text     filter (keeps ancestors) · esc clears",
		"   s / S     sort column / reverse",
		"   r refresh · L live on/off · t theme · q quit",
	}
	var l []string
	if m.w() >= 110 {
		for i := range max(len(left), len(right)) {
			a, c := "", ""
			if i < len(left) {
				a = left[i]
			}
			if i < len(right) {
				c = right[i]
			}
			l = append(l, pad(a, 52)+c)
		}
	} else {
		l = append(append(left, ""), right...)
	}
	l = append(l, "",
		" "+b("LEGEND")+"  "+st.Ok.Render("●")+" ok  "+st.Warn.Render("!")+" degraded  "+st.Bad.Render("✗")+" unbound  "+
			st.Faint.Render("·")+" infra port  ├╌ ⇢ related object (graph edge)",
		" "+st.Warn.Render("By design not shown: VPC NAT Gateway (Iptables*) NAT runs in a pod, and default-VPC"),
		" "+st.Warn.Render("natOutgoing (centralized) is an LR policy reroute + node iptables — neither is NB NAT."),
		st.MutedText.Render(" any key closes help"))
	return box(l, m.w(), m.areaH(), st.Label.Render("Help"), st.Border)
}

var commands = []string{"xray vpc", "xray chassis", "xray workload", "ports", "lrp", "matrix", "vpc", "chassis", "workload", "theme", "quit"}

func suggest(prefix string) string {
	if prefix == "" {
		return ""
	}
	for _, c := range commands {
		if strings.HasPrefix(c, prefix) && c != prefix {
			return c
		}
	}
	return ""
}
