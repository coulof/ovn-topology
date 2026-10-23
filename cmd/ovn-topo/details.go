package main

// The xray detail pane: a docked, read-only explanation of the selected object,
// centred on how it interconnects (ports, peers, gateways, the path out of a
// subnet). Enter on a ⇢ row still jumps; this pane only describes.

import (
	"fmt"
	"strings"
)

// paneMinW is the terminal width from which the detail pane docks on the right.
const paneMinW = 100

func (m model) showPane() bool { return m.pinspect && m.w() >= paneMinW }

func (m model) treeW() int {
	if !m.showPane() {
		return m.w()
	}
	return max(50, m.w()*58/100)
}

// wrapText breaks plain text on spaces so that no line exceeds w cells.
func wrapText(s string, w int) []string {
	if w < 10 {
		return []string{s}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > w {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// detailLines renders the pane body for n, w cells wide (inside the border).
func (m model) detailLines(n *xnode, w int) []string {
	st := m.styles
	if n == nil {
		return []string{"", " " + st.MutedText.Render("nothing selected")}
	}
	const kw = 15
	kv := func(k, v string) string { return " " + st.Label.Render(pad(k, kw)) + v }
	section := func(s string) string { return " " + st.Value.Render(s) }
	note := func(s string) []string {
		var out []string
		for _, l := range wrapText(s, w-3) {
			out = append(out, "   "+st.MutedText.Render(l))
		}
		return out
	}
	node := func(id string) *xnode {
		if m.tree == nil {
			return nil
		}
		return m.tree.byID[id]
	}
	nameOf := func(x *xnode) string { return m.kind(x) + " " + st.Kind[x.kind].Render(x.name) }

	head := m.glyph(n.health) + " " + nameOf(n)
	if n.kind == kGroup {
		head = st.Value.Render(n.name)
	}
	out := []string{" " + head + "  " + m.badge(n)}
	if n.summary != "" {
		for _, l := range wrapText(n.summary, w-3) {
			out = append(out, "   "+st.MutedText.Render(l))
		}
	}
	out = append(out, "")
	p := props(n.gn)

	switch n.kind {
	case kLR:
		ports := m.routerConns(n)
		out = append(out, section(fmt.Sprintf("Ports & interconnections (%d)", len(ports))))
		if len(ports) == 0 {
			out = append(out, "   "+st.MutedText.Render("(no connected ports)"))
		}
		for _, c := range ports {
			out = append(out, " • "+st.PortBadge.Render(c.PortName)+"  "+st.CIDR.Render(strings.Join(c.Networks, ", ")))
			if c.SwitchName != "" {
				peer := "→ " + m.kind(&xnode{kind: kLS}) + " " + st.Kind[kLS].Render(c.SwitchName)
				if c.IsShared {
					peer += st.MutedText.Render("  (shared interconnect)")
				}
				out = append(out, "     "+peer)
			}
			if c.MAC != "" {
				out = append(out, "     "+st.MutedText.Render("mac "+c.MAC))
			}
			if gw := m.portGateway(c); gw != "" {
				out = append(out, "     "+st.ActiveGateway.Render("Active GW on "+gw))
			}
		}
		out = append(out, "", section("L3"),
			kv("Routes", fmt.Sprint(pint(p, "routeCount")))+"   "+st.Label.Render("Policies ")+fmt.Sprint(pint(p, "policyCount")),
			kv("NAT", fmt.Sprint(pint(p, "natCount")))+"   "+st.Label.Render("LBs      ")+fmt.Sprint(pint(p, "lbCount")))
		if pint(p, "natCount") == 0 {
			out = append(out, note("NAT 0 does not rule out VPC NAT Gateway (iptables in a pod) or centralized natOutgoing (policy reroute + node iptables).")...)
		}

	case kLRP:
		c := n.conn
		if c == nil {
			break
		}
		routers, cidr, mode, explain := m.portFacts(c)
		out = append(out,
			kv("Router", st.Kind[kLR].Render(c.RouterName)+st.MutedText.Render("  VPC "+c.VPCName)),
			kv("Networks", st.CIDR.Render(firstNonEmpty(strings.Join(c.Networks, ", "), "—"))),
			kv("MAC", firstNonEmpty(c.MAC, "—")),
			kv("Peer switch", st.Kind[kLS].Render(firstNonEmpty(c.SwitchName, "—"))))
		if cidr != "" {
			out = append(out, kv("", cidr))
		}
		out = append(out,
			kv("Peer LSP", firstNonEmpty(c.PeerPortName, "—")),
			kv("Routers on LS", firstNonEmpty(strings.Join(routers, ", "), "—")),
			kv("Gateway", firstNonEmpty(m.portGateway(c), "—")),
			kv("Role", portRole(c)),
			"", section("Routing"))
		for _, l := range wrapText(mode, w-3) {
			if c.ActiveGateway != "" {
				out = append(out, "   "+st.ActiveGateway.Render(l))
			} else {
				out = append(out, "   "+st.Ok.Render(l))
			}
		}
		out = append(out, note(explain)...)

	case kLS:
		conns := m.switchPorts[n.id]
		out = append(out, section(fmt.Sprintf("Connected routers (%d)", len(conns))))
		if len(conns) == 0 {
			out = append(out, "   "+st.MutedText.Render("(standalone switch — no router port)"))
		}
		for _, c := range conns {
			out = append(out, " • "+st.Kind[kLR].Render(c.RouterName)+st.MutedText.Render("  VPC "+c.VPCName),
				"     via "+st.PortBadge.Render(c.PortName)+"  "+st.CIDR.Render(strings.Join(c.Networks, ", ")))
		}
		out = append(out, "", section("Ports"),
			kv("Total", fmt.Sprint(pint(p, "portCount"))),
			kv("Bound", st.BadgeBound.Render(fmt.Sprint(pint(p, "boundPortCount")))),
			kv("Unbound", m.countStyle(pint(p, "unboundPortCount"))))
		for _, r := range n.refs {
			if x := node(r.to); x != nil && r.label == "localnet" {
				out = append(out, "", section("Underlay"), kv("Localnet", nameOf(x)))
			}
		}

	case kLSP:
		ip, mac := lspAddress(n.gn)
		out = append(out,
			kv("Type", firstNonEmpty(pstr(p, "type"), "(workload)")),
			kv("IP", st.CIDR.Render(firstNonEmpty(ip, "—"))),
			kv("MAC", firstNonEmpty(mac, "—")))
		var subnet *xnode
		bound := ""
		for _, r := range n.refs {
			x := node(r.to)
			if x == nil {
				continue
			}
			switch r.label {
			case "subnet":
				subnet = x
			case "bound on":
				bound = st.Kind[kChassis].Render(x.name)
				if node := pstr(props(x.gn), "nodeName"); node != "" && node != x.name {
					bound += st.MutedText.Render("  node " + node)
				}
			case "workload":
				out = append(out, kv("Workload", st.Kind[kWL].Render(x.wlKind+" "+x.name)))
			}
		}
		switch {
		case bound != "":
			out = append(out, kv("Bound on", bound))
		case n.health == hNone:
			out = append(out, kv("Bound on", st.MutedText.Render("— (infrastructure port)")))
		default:
			out = append(out, kv("Bound on", st.BadgeUnbound.Render("unbound — no Port_Binding.chassis")))
		}
		if subnet != nil {
			out = append(out, "", section("Path out"),
				"   "+st.Kind[kLSP].Render(n.name),
				"   → "+nameOf(subnet)+"  "+st.CIDR.Render(strings.Join(uniq(m.switchNetworks(subnet.id)), ", ")))
			conns := m.switchPorts[subnet.id]
			if len(conns) == 0 {
				out = append(out, "   "+st.MutedText.Render("(no router on this switch: L2 only)"))
			}
			for _, c := range conns {
				gw := ""
				if len(c.Networks) > 0 {
					gw = " gw " + extractCleanIP(c.Networks[0])
				}
				out = append(out, "   → "+m.kind(&xnode{kind: kLRP})+" "+st.PortBadge.Render(c.PortName)+st.CIDR.Render(gw),
					"   → "+m.kind(&xnode{kind: kLR})+" "+st.Kind[kLR].Render(c.RouterName)+st.MutedText.Render("  VPC "+c.VPCName))
			}
		}

	case kChassis:
		// A chassis is one ovn-controller, identified by its OVS system-id; the
		// hostname is informational, so show which rule linked it to a node.
		nodeLine := firstNonEmpty(pstr(p, "nodeName"), "—")
		if how := pstr(p, "nodeMatch"); how != "" {
			nodeLine += st.MutedText.Render("  ← " + firstNonEmpty(nodeMatchLabel[how], how))
		}
		out = append(out,
			kv("System-id", firstNonEmpty(pstr(p, "name"), strings.TrimPrefix(n.id, "chassis:"))),
			kv("Hostname", firstNonEmpty(pstr(p, "hostname"), n.name)),
			kv("Node", nodeLine))
		if u := pstr(p, "uuid"); u != "" {
			out = append(out, kv("_uuid", st.MutedText.Render(u)))
		}
		var warn []string
		switch pstr(p, "nodeMatch") {
		case "hostname":
			warn = append(warn, "matched by hostname only: no node carries ovn.kubernetes.io/chassis="+pstr(p, "name"))
		case "fallback-hostname", "fallback-name":
			warn = append(warn, "no Kubernetes node matches this chassis")
		}
		if c := pint(p, "hostnameShared"); c > 1 {
			warn = append(warn, fmt.Sprintf("%d chassis share hostname %s — stale chassis after a reinstall?", c, pstr(p, "hostname")))
		}
		if b, _ := p["isInterconn"].(bool); b {
			warn = append(warn, "interconnect chassis (OVN-IC): not a host of this cluster")
		}
		for _, msg := range warn {
			for i, l := range wrapText(msg, w-5) {
				prefix := "   "
				if i == 0 {
					prefix = " ⚠ "
				}
				out = append(out, prefix+st.Warn.Render(l))
			}
		}
		encaps, _ := p["encaps"].([]interface{})
		for _, e := range encaps {
			em, _ := e.(map[string]interface{})
			out = append(out, kv("Encap", pstr(em, "type")+" "+st.CIDR.Render(pstr(em, "ip"))))
		}
		out = append(out, kv("Bound ports", fmt.Sprint(len(n.children))))
		var gws []string
		for _, r := range n.refs {
			if x := node(r.to); x != nil && r.label == "active gateway for" {
				gws = append(gws, "   "+nameOf(x)+st.MutedText.Render(gwRouter(x)))
			}
		}
		if len(gws) > 0 {
			out = append(out, "", section("Active gateway for"))
			out = append(out, gws...)
		}

	case kWL:
		out = append(out, kv("Kind", n.wlKind))
		if nic := firstChild(n); nic != nil && nic.gn != nil && nic.gn.K8s != nil {
			out = append(out, kv("Namespace", deref(nic.gn.K8s.Namespace)), kv("Node", firstNonEmpty(deref(nic.gn.K8s.Node), "—")))
		}
		out = append(out, "", section(fmt.Sprintf("NICs (%d)", len(n.children))))
		for _, c := range n.children {
			ip, _ := lspAddress(c.gn)
			line := " " + m.glyph(c.health) + " " + st.Kind[kLSP].Render(c.name) + "  " + st.CIDR.Render(ip)
			out = append(out, line)
			var where []string
			for _, r := range c.refs {
				if x := node(r.to); x != nil && (r.label == "subnet" || r.label == "bound on") {
					where = append(where, map[string]string{"subnet": "on ", "bound on": "@"}[r.label]+x.name)
				}
			}
			if len(where) > 0 {
				out = append(out, "     "+st.MutedText.Render(strings.Join(where, "  ")))
			}
		}

	case kExt:
		out = append(out, note("Provider network: the physical underlay reached through a localnet port. It is outside OVN, so only its attachment is known.")...)

	default: // group, vpc, ns
		counts := map[string]int{}
		var order []string
		for _, c := range n.children {
			if counts[c.kind] == 0 {
				order = append(order, c.kind)
			}
			counts[c.kind]++
		}
		if len(order) > 0 {
			out = append(out, section("Contains"))
		}
		for _, k := range order {
			out = append(out, "   "+plural(counts[k], kindNoun[k]))
		}
		if n.kind == kVPC {
			var gws []string
			for _, c := range n.children {
				if c.kind != kLR {
					continue
				}
				for _, pc := range m.routerConns(c) {
					if gw := m.portGateway(pc); gw != "" {
						gws = append(gws, "   "+st.PortBadge.Render(pc.PortName)+"  "+st.ActiveGateway.Render("active "+gw))
					}
				}
			}
			out = append(out, "", section("Egress"))
			if len(gws) == 0 {
				out = append(out, "   "+st.MutedText.Render("distributed only (no gateway port)"))
			}
			out = append(out, gws...)
		}
	}

	if n.gn != nil && n.gn.K8s != nil && n.kind != kWL {
		k := n.gn.K8s
		var rows []string
		for _, f := range []struct{ k, v string }{
			{"Namespace", deref(k.Namespace)}, {"Pod", deref(k.Pod)}, {"Owner", deref(k.Owner)}, {"VMI", deref(k.Vmi)},
			{"Node", deref(k.Node)}, {"Subnet", deref(k.Subnet)}, {"VPC", deref(k.Vpc)}, {"NAD", deref(k.NetworkAttachment)},
		} {
			if f.v != "" {
				rows = append(rows, kv(f.k, f.v))
			}
		}
		if len(rows) > 0 {
			out = append(out, "", section("Kubernetes"))
			out = append(out, rows...)
		}
	}

	var rels []string
	for _, r := range n.refs {
		if x := node(r.to); x != nil {
			rels = append(rels, "   "+st.MutedText.Render(pad(r.label, 19)+"⇢ ")+nameOf(x))
		}
	}
	if len(rels) > 0 {
		out = append(out, "", section("Relations"))
		out = append(out, rels...)
	}
	hint := "d describe · y raw"
	if len(rels) > 0 {
		hint = "enter on a ⇢ row jumps · " + hint
	}
	if m.diagMode {
		hint += " · x diag"
	}
	out = append(out, "")
	return append(out, note(hint+" · i hide")...)
}

// nodeMatchLabel explains the daemon's chassis → node rule (graph.NodeMapper.MatchChassis).
var nodeMatchLabel = map[string]string{
	"annotation":        "node annotation ovn.kubernetes.io/chassis",
	"hostname":          "Kubernetes node with this hostname",
	"override":          "static mapping",
	"fallback-hostname": "chassis hostname (no Kubernetes match)",
	"fallback-name":     "chassis name (no Kubernetes match)",
}

var kindNoun = map[string]string{kVPC: "VPC", kLR: "router", kLRP: "router port", kLS: "switch", kLSP: "port",
	kChassis: "chassis node", kExt: "external network", kNS: "namespace", kWL: "workload", kGroup: "group"}

// countStyle highlights a problem count only when it is non-zero.
func (m model) countStyle(n int) string {
	if n == 0 {
		return fmt.Sprint(n)
	}
	return m.styles.BadgeUnbound.Render(fmt.Sprint(n))
}

// routerConns lists an LR's ports as its xray children carry them (gateway-only ports included).
func (m model) routerConns(lr *xnode) []*RouterPortConnection {
	var out []*RouterPortConnection
	for _, c := range lr.children {
		if c.conn != nil {
			out = append(out, c.conn)
		}
	}
	return out
}

// portGateway is the active gateway node of a router port, as the LRP row shows it.
func (m model) portGateway(c *RouterPortConnection) string {
	if m.tree != nil {
		if n := m.tree.byID[lrpID(c)]; n != nil && strings.HasPrefix(n.badge, "GW active ") {
			return strings.TrimPrefix(n.badge, "GW active ")
		}
	}
	return m.resolveNodeName(c.ActiveGateway)
}

func (m model) switchNetworks(lsID string) []string {
	var nets []string
	for _, c := range m.switchPorts[lsID] {
		nets = append(nets, c.Networks...)
	}
	return nets
}

func gwRouter(lrp *xnode) string {
	if lrp.conn == nil {
		return ""
	}
	return "  " + lrp.conn.RouterName + " (VPC " + lrp.conn.VPCName + ")"
}

func firstChild(n *xnode) *xnode {
	if len(n.children) == 0 {
		return nil
	}
	return n.children[0]
}

func (m model) detailPane(h int) []string {
	n := m.selectedXNode()
	title := m.styles.Label.Render("Details")
	if n != nil {
		title += " " + m.styles.MutedText.Render(n.id)
	}
	w := m.w() - m.treeW()
	return box(m.detailLines(n, w-2), w, h, title, m.styles.Faint)
}
