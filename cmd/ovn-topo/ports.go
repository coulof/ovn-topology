package main

// ":ports" — the Port Matrix as a k9s-style resource table: sortable, filterable,
// with a docked inspector. Enter opens the port in the xray tree.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var portCols = []string{"VPC", "ROUTER PORT (LRP)", "NETWORKS", "PEER SWITCH", "MAC", "GATEWAY", "ROLE"}

// portColDropOrder lists columns hidden first on narrow terminals.
var portColDropOrder = []int{4, 6, 2}

const inspectorH = 11

type portRow struct {
	conn  *RouterPortConnection
	cells []string
	role  string
}

func portRole(c *RouterPortConnection) string {
	switch {
	case c.ActiveGateway != "":
		return "external gateway"
	case c.IsShared:
		return "interconnect"
	case c.SwitchName == "join":
		return "node transit (join)"
	default:
		return "subnet gateway"
	}
}

func (m model) portRows() []portRow {
	var rows []portRow
	conns := make([]*RouterPortConnection, 0, len(m.portList)+len(m.gatewayPorts))
	for i := range m.portList {
		conns = append(conns, &m.portList[i])
	}
	conns = append(conns, m.gatewayPorts...)
	for _, c := range conns {
		gw := "distributed"
		if c.ActiveGateway != "" {
			gw = "active " + m.resolveNodeName(c.ActiveGateway)
		}
		sw := c.SwitchName
		if c.IsShared {
			sw += " *"
		}
		r := portRow{conn: c, role: portRole(c)}
		r.cells = []string{c.VPCName, c.PortName, strings.Join(c.Networks, ", "), sw, c.MAC, gw, r.role}
		if m.filter != "" && !containsFold(strings.Join(r.cells, " "), m.filter) {
			continue
		}
		rows = append(rows, r)
	}
	col := m.psort % len(portCols)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].cells[col], rows[j].cells[col]
		if a == b {
			return rows[i].cells[1] < rows[j].cells[1]
		}
		if m.pdesc {
			return a > b
		}
		return a < b
	})
	return rows
}

// showInspector docks the inspector only when the table keeps at least 6 rows.
func (m model) showInspector() bool { return m.pinspect && m.areaH() >= inspectorH+10 }

func (m model) tableH() int {
	h := m.bodyH()
	if m.showInspector() {
		h -= inspectorH
	}
	return max(h-2, 1) // header row + legend
}

func (m *model) portKey(s string) (tea.Model, tea.Cmd, bool) {
	rows := m.portRows()
	n := len(rows)
	var sel *portRow
	if n > 0 {
		sel = &rows[min(m.pcur, n-1)]
	}
	var cmd tea.Cmd
	switch s {
	case "j", "down":
		m.pcur++
	case "k", "up":
		m.pcur--
	case "g", "home":
		m.pcur = 0
	case "G", "end":
		m.pcur = n - 1
	case "ctrl+d", "pgdown":
		m.pcur += m.tableH() / 2
	case "ctrl+u", "pgup":
		m.pcur -= m.tableH() / 2
	case "s":
		m.psort = (m.psort + 1) % len(portCols)
		m.setFlash("sorted by "+portCols[m.psort], false)
	case "S":
		m.pdesc = !m.pdesc
	case "i":
		if !m.pinspect || m.showInspector() {
			m.pinspect = !m.pinspect
		} else {
			m.setFlash("terminal too short for the inspector — use d to describe the port", false)
		}
	case "enter":
		if sel != nil {
			m.gotoNode(lrpID(sel.conn))
			return *m, nil, true
		}
	case "d", "y":
		if sel != nil && m.tree != nil {
			cmd = m.openDescribe(m.tree.byID[lrpID(sel.conn)], s == "y")
		}
	default:
		return *m, nil, false
	}
	m.pcur = max(min(m.pcur, n-1), 0)
	m.poff = window(m.pcur, m.poff, n, m.tableH())
	return *m, cmd, true
}

func (m model) portsView(h int) []string {
	st := m.styles
	rows := m.portRows()
	th := m.tableH()
	inner := m.w() - 2

	widths := make([]int, len(portCols))
	for i, c := range portCols {
		widths[i] = len(c) + 3
	}
	for _, r := range rows {
		for i, c := range r.cells {
			widths[i] = max(widths[i], lipgloss.Width(c)+2)
		}
	}
	hidden := map[int]bool{}
	total := func() int {
		t := 1
		for i, w := range widths {
			if !hidden[i] {
				t += w
			}
		}
		return t
	}
	for _, c := range portColDropOrder {
		if total() > inner {
			hidden[c] = true
		}
	}

	head := " "
	for i, c := range portCols {
		if hidden[i] {
			continue
		}
		if i == m.psort {
			c += map[bool]string{false: "↑", true: "↓"}[m.pdesc]
		}
		head += st.Value.Render(pad(c, widths[i]))
	}
	body := []string{head}
	off := window(m.pcur, m.poff, len(rows), th)
	for i := off; i < min(off+th, len(rows)); i++ {
		r := rows[i]
		line := " "
		for c, v := range r.cells {
			if hidden[c] {
				continue
			}
			cell := pad(v, widths[c])
			switch c {
			case 0:
				cell = st.Kind[kVPC].Render(cell)
			case 1:
				cell = st.Kind[kLRP].Render(cell)
			case 2:
				cell = st.CIDR.Render(cell)
			case 3:
				cell = st.Kind[kLS].Render(cell)
			case 4, 6:
				cell = st.MutedText.Render(cell)
			case 5:
				if r.conn.ActiveGateway != "" {
					cell = st.ActiveGateway.Render(cell)
				} else {
					cell = st.Ok.Render(cell)
				}
			}
			line += cell
		}
		id := lrpID(r.conn)
		if t, ok := m.changed[id]; ok && time.Since(t) < flashFor {
			line = solid(line, inner, st.Flash)
		}
		if i == min(m.pcur, len(rows)-1) {
			line = solid(line, inner, st.Selected)
		}
		body = append(body, line)
	}
	if len(rows) == 0 {
		msg := "   no router ports in this graph"
		if m.filter != "" {
			msg = "   no port matches “" + m.filter + "”  (esc clears the filter)"
		}
		body = append(body, "", st.MutedText.Render(msg))
	}
	body = append(body, "")
	for len(body) < th+1 {
		body = append(body, "")
	}
	body = append(body, st.MutedText.Render("  * shared switch (attached to more than one router)"))

	title := st.Label.Render("Ports") + st.CIDR.Render("(lrp)") + st.MutedText.Render(fmt.Sprintf("[%d]", len(rows)))
	title += m.filterTitle()
	tableBox := h
	showInspector := m.showInspector()
	if showInspector {
		tableBox = h - inspectorH
	}
	out := box(body, m.w(), tableBox, title, st.Border)
	if showInspector {
		var sel *portRow
		if len(rows) > 0 {
			sel = &rows[min(m.pcur, len(rows)-1)]
		}
		out = append(out, m.inspector(sel)...)
	}
	return out
}

func (m model) inspector(r *portRow) []string {
	st := m.styles
	if r == nil {
		return box(nil, m.w(), inspectorH, "Inspector", st.Faint)
	}
	c := r.conn
	k := func(s string) string { return st.Label.Render(pad(s, 15)) }
	colW := (m.w() - 5) / 2
	two := func(a, b string) string { return " " + fit(a, colW) + "  " + fit(b, colW) }
	routers, cidr, mode, explain := m.portFacts(c)
	if c.ActiveGateway != "" {
		mode = st.ActiveGateway.Render(mode)
	} else {
		mode = st.Ok.Render(mode)
	}
	body := []string{
		two(k("Router")+st.Kind[kLR].Render(c.RouterName)+st.MutedText.Render("  VPC "+c.VPCName),
			k("Peer switch")+st.Kind[kLS].Render(c.SwitchName)+"  "+cidr),
		two(k("Networks")+st.CIDR.Render(strings.Join(c.Networks, ", ")), k("Peer LSP")+c.PeerPortName),
		two(k("MAC")+c.MAC, k("Routers on LS")+strings.Join(routers, ", ")),
		two(k("Role")+r.role, k("Gateway")+firstNonEmpty(m.resolveNodeName(c.ActiveGateway), "—")),
		" " + k("Routing") + mode,
		" " + k("") + st.MutedText.Render(explain),
		"",
		" " + st.MutedText.Render("enter: open in xray · d: describe · y: raw · s/S: sort · i: hide inspector · x: diag"),
	}
	return box(body, m.w(), inspectorH, st.Label.Render("Inspector")+" "+c.PortName, st.Faint)
}

// portFacts explains a router port: the routers sharing its peer switch, the
// switch summary, the routing mode and a one-line role explanation (plain text).
// Shared by the :ports inspector and the xray detail pane.
func (m model) portFacts(c *RouterPortConnection) (routers []string, cidr, mode, explain string) {
	st := m.styles
	for _, p := range m.switchPorts[c.SwitchID] {
		routers = append(routers, p.RouterName)
	}
	if m.tree != nil {
		if ls := m.tree.byID[c.SwitchID]; ls != nil {
			cidr = st.MutedText.Render(ls.summary)
		}
	}
	ip := ""
	if len(c.Networks) > 0 {
		ip = extractCleanIP(c.Networks[0])
	}
	mode = "Distributed — every chassis routes this subnet locally (no redirect port)"
	explain = fmt.Sprintf("Workloads on '%s' use %s as their default gateway.", c.SwitchName, ip)
	switch {
	case c.ActiveGateway != "":
		mode = "Centralized — traffic egresses via the active gateway chassis " + m.resolveNodeName(c.ActiveGateway)
		explain = "External gateway port (EIP / SNAT); the cr-" + c.PortName + " redirect port is bound on the active chassis."
	case c.IsShared:
		explain = fmt.Sprintf("Interconnect: '%s' is attached to %d routers (%s).", c.SwitchName, len(routers), strings.Join(routers, ", "))
	case c.SwitchName == "join":
		explain = "Node transit: connects the VPC router to every node's ovn0 interface over the join switch."
	}
	return routers, cidr, mode, explain
}
