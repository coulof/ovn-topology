package main

// The xray model: one typed node per object, with structural children and
// cross-links (refs) that follow graph edges. Built from the same indexes the
// diagnostics use (nodeMap, routerPorts, switchPorts, switchWorkloads, chassisNodes).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/graph"
)

const (
	kGroup   = "group"
	kVPC     = "vpc"
	kLR      = "lr"
	kLRP     = "lrp"
	kLS      = "ls"
	kLSP     = "lsp"
	kChassis = "chassis"
	kExt     = "ext"
	kNS      = "ns"
	kWL      = "wl"
)

type health int

const (
	hNone health = iota
	hOK
	hWarn
	hBad
)

type xref struct{ label, to string }

type xnode struct {
	id, kind, name string
	summary        string // inline dim text
	badge          string // short status text, e.g. "unbound", "GW active hv-01"
	health         health
	wlKind         string // kWL: VirtualMachine / Pod / Node
	gn             *api.GraphNode
	conn           *RouterPortConnection
	children       []*xnode
	refs           []xref
}

// childrenOrNil is n.children, safe on a nil node.
func (n *xnode) childrenOrNil() []*xnode {
	if n == nil {
		return nil
	}
	return n.children
}

// sig is what a live update compares to decide whether a row changed.
func (n *xnode) sig() string {
	return fmt.Sprintf("%s|%s|%d|%d", n.summary, n.badge, n.health, len(n.children))
}

type xtree struct {
	byID      map[string]*xnode
	lspByName map[string]*xnode
	roots     map[mode][]*xnode
	stats     struct{ vpcs, switches, ports, chassis int }
}

func props(gn *api.GraphNode) map[string]interface{} {
	if gn == nil || gn.Properties == nil {
		return nil
	}
	return *gn.Properties
}

func pstr(p map[string]interface{}, k string) string {
	s, _ := p[k].(string)
	return s
}

func pint(p map[string]interface{}, k string) int {
	switch v := p[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// lspAddress returns the first IP and MAC of an LSP's addresses.
func lspAddress(gn *api.GraphNode) (ip, mac string) {
	addrs, _ := props(gn)["addresses"].([]interface{})
	for _, a := range addrs {
		s, _ := a.(string)
		if ip == "" {
			ip = extractCleanIP(s)
		}
		if mac == "" {
			mac = extractCleanMAC(s)
		}
	}
	return ip, mac
}

func lrpID(c *RouterPortConnection) string { return "lrp:" + c.RouterID + "/" + c.PortName }

// infraPortTypes are LSP types that are plumbing, not workloads: no binding badge.
var infraPortTypes = map[string]bool{"router": true, "localnet": true, "patch": true, "l2gateway": true, "vtep": true, "external": true}

func (m *model) rebuildTree() {
	t := &xtree{byID: map[string]*xnode{}, roots: map[mode][]*xnode{}}
	m.gatewayPorts = nil
	add := func(n *xnode) *xnode {
		t.byID[n.id] = n
		return n
	}

	// --- chassis (and every alias a binding may refer to)
	alias := map[string]*xnode{}
	chs := append([]api.GraphNode{}, m.chassisNodes...)
	sort.Slice(chs, func(i, j int) bool { return chs[i].Name < chs[j].Name })
	var chassis []*xnode
	for i := range chs {
		g := chs[i]
		p := props(&g)
		host := firstNonEmpty(pstr(p, "nodeName"), pstr(p, "hostname"), g.Name)
		n := add(&xnode{id: g.Id, kind: kChassis, name: host, gn: &g, health: hOK})
		chassis = append(chassis, n)
		for _, a := range []string{g.Id, strings.TrimPrefix(g.Id, graph.PrefixChassis), g.Name, host,
			pstr(p, "chassis"), pstr(p, "hostname"), pstr(p, "nodeName"), pstr(p, "uuid")} {
			if a != "" {
				alias[a] = n
			}
		}
	}
	findChassis := func(s string) *xnode {
		if s == "" {
			return nil
		}
		return alias[s]
	}

	// --- switches, their ports and the workloads behind them
	ns := map[string]*xnode{}
	wls := map[string]*xnode{}
	lspByName := map[string]*xnode{}
	var switches []*xnode
	for _, gn := range m.nodeMap {
		if gn.Type != api.GraphNodeTypeLS {
			continue
		}
		g := gn
		ls := add(&xnode{id: g.Id, kind: kLS, name: g.Name, gn: &g})
		switches = append(switches, ls)

		lsps := append([]api.GraphNode{}, m.switchWorkloads[g.Id]...)
		sort.SliceStable(lsps, func(i, j int) bool {
			ti, tj := infraPortTypes[pstr(props(&lsps[i]), "type")], infraPortTypes[pstr(props(&lsps[j]), "type")]
			if ti != tj {
				return ti
			}
			return lsps[i].Name < lsps[j].Name
		})
		for i := range lsps {
			lp := m.lspNode(&lsps[i], ls, findChassis)
			add(lp)
			ls.children = append(ls.children, lp)
			lspByName[lp.name] = lp
			for _, r := range lp.refs {
				if r.label == "bound on" {
					if c := t.byID[r.to]; c != nil {
						c.children = append(c.children, lp)
					}
				}
			}
			if wk, wname, wns, ok := workloadOf(lp.gn); ok {
				nsn := ns[wns]
				if nsn == nil {
					nsn = add(&xnode{id: "ns:" + wns, kind: kNS, name: wns})
					ns[wns] = nsn
				}
				wid := "wl:" + wns + "/" + wk + "/" + wname
				w := wls[wid]
				if w == nil {
					w = add(&xnode{id: wid, kind: kWL, wlKind: wk, name: wname})
					wls[wid] = w
					nsn.children = append(nsn.children, w)
				}
				w.children = append(w.children, lp)
				lp.refs = append(lp.refs, xref{"workload", wid})
			}
		}
	}
	sort.Slice(switches, func(i, j int) bool { return switches[i].name < switches[j].name })

	// --- routers and their ports
	gateways := map[string]string{} // lrp name -> active chassis (from LR properties)
	for _, gn := range m.nodeMap {
		if gn.Type != api.GraphNodeTypeLR {
			continue
		}
		g := gn
		p := props(&g)
		lr := add(&xnode{id: g.Id, kind: kLR, name: g.Name, gn: &g, health: hOK})
		known := map[string]bool{}
		ports := m.routerPorts[g.Id]
		for i := range ports {
			known[ports[i].PortName] = true
		}
		gws, _ := p["gateways"].([]interface{})
		var extra []*RouterPortConnection
		for _, x := range gws {
			gw, _ := x.(map[string]interface{})
			lrpName, active := pstr(gw, "lrp"), pstr(gw, "activeChassis")
			gateways[lrpName] = active
			if lrpName != "" && !known[lrpName] {
				extra = append(extra, &RouterPortConnection{RouterID: g.Id, RouterName: g.Name,
					VPCName: strings.TrimPrefix(deref(g.Parent), graph.PrefixVPC), PortName: lrpName, ActiveGateway: active})
			}
		}
		var conns []*RouterPortConnection
		for i := range ports {
			conns = append(conns, &ports[i])
		}
		conns = append(conns, extra...)
		m.gatewayPorts = append(m.gatewayPorts, extra...)
		sort.Slice(conns, func(i, j int) bool { return conns[i].PortName < conns[j].PortName })
		for _, c := range conns {
			lr.children = append(lr.children, add(m.lrpNode(c, gateways[c.PortName], lspByName, findChassis)))
		}
		lr.summary = fmt.Sprintf("ports %d · routes %d · nat %d · pol %d · lb %d", len(conns),
			pint(p, "routeCount"), pint(p, "natCount"), pint(p, "policyCount"), pint(p, "lbCount"))
	}

	// --- switch summaries now that router ports are known
	for _, ls := range switches {
		p := props(ls.gn)
		var cidrs []string
		for _, c := range m.switchPorts[ls.id] {
			cidrs = append(cidrs, c.Networks...)
			ls.refs = append(ls.refs, xref{"router", c.RouterID})
		}
		total, bound, unbound := pint(p, "portCount"), pint(p, "boundPortCount"), pint(p, "unboundPortCount")
		parts := []string{}
		if len(cidrs) > 0 {
			parts = append(parts, strings.Join(uniq(cidrs), ", "))
		}
		parts = append(parts, plural(total, "port")+fmt.Sprintf(" · %d bound", bound))
		if unbound > 0 {
			parts = append(parts, fmt.Sprintf("%d unbound", unbound))
		}
		ls.summary = strings.Join(parts, " · ")
		ls.health = hOK
		if unbound > 0 {
			ls.health = hWarn
		}
		if n := len(m.switchPorts[ls.id]); n > 1 {
			ls.badge = fmt.Sprintf("shared by %d routers", n)
		}
	}
	for _, e := range m.edges() {
		if e.Type == api.Localnet {
			if ls := t.byID[e.Source]; ls != nil {
				ls.refs = append(ls.refs, xref{"localnet", e.Target})
			}
		}
	}

	// --- VPCs and the vpc-xray groups
	var vpcs []*xnode
	for _, name := range m.vpcNames {
		v := add(&xnode{id: graph.PrefixVPC + name, kind: kVPC, name: name})
		if gn, ok := m.nodeMap[v.id]; ok {
			g := gn
			v.gn = &g
			if b, _ := props(&g)["isDefault"].(bool); b {
				v.badge = "default"
			}
		}
		var lrs, lss int
		for _, gn := range m.vpcGroups[name] {
			if c := t.byID[gn.Id]; c != nil {
				v.children = append(v.children, c)
				if c.kind == kLR {
					lrs++
				} else {
					lss++
				}
			}
		}
		v.summary = plural(lrs, "router") + " · " + plural(lss, "switch")
		v.health = hOK
		vpcs = append(vpcs, v)
	}
	var shared, exts []*xnode
	for _, gn := range m.sharedNodes {
		if c := t.byID[gn.Id]; c != nil {
			shared = append(shared, c)
		}
	}
	for _, gn := range m.externalNodes {
		g := gn
		x := add(&xnode{id: g.Id, kind: kExt, name: g.Name, gn: &g, summary: "provider network (physical underlay, outside OVN)"})
		for _, e := range m.edges() {
			if e.Type == api.Localnet && e.Target == g.Id {
				x.refs = append(x.refs, xref{"localnet from", e.Source})
			}
		}
		exts = append(exts, x)
	}
	var groups []*xnode
	for _, g := range []struct {
		id, name string
		kids     []*xnode
	}{
		{"group:vpcs", "VPCs", vpcs},
		{"group:shared", "Shared & standalone switches", shared},
		{"group:external", "External networks", exts},
	} {
		if len(g.kids) > 0 {
			groups = append(groups, add(&xnode{id: g.id, kind: kGroup, name: g.name, children: g.kids}))
		}
	}

	// --- chassis summaries (children were attached while walking ports)
	for _, c := range chassis {
		p := props(c.gn)
		var encs []string
		encaps, _ := p["encaps"].([]interface{})
		for _, e := range encaps {
			em, _ := e.(map[string]interface{})
			encs = append(encs, strings.TrimSpace(pstr(em, "type")+" "+pstr(em, "ip")))
		}
		sort.Slice(c.children, func(i, j int) bool { return c.children[i].name < c.children[j].name })
		c.summary = strings.Join(append(encs, plural(len(c.children), "port")), " · ")
	}
	for _, n := range t.byID {
		if n.kind == kLRP {
			for _, r := range n.refs {
				if strings.HasPrefix(r.label, "gateway") {
					if c := t.byID[r.to]; c != nil {
						c.refs = append(c.refs, xref{"active gateway for", n.id})
						c.badge = "gateway"
					}
				}
			}
		}
	}

	for _, c := range chassis { // map iteration above: make ref order stable
		sort.Slice(c.refs, func(i, j int) bool { return c.refs[i].to < c.refs[j].to })
	}

	// --- workload xray
	var nss []*xnode
	for _, n := range ns {
		sort.Slice(n.children, func(i, j int) bool { return n.children[i].name < n.children[j].name })
		nss = append(nss, n)
	}
	sort.Slice(nss, func(i, j int) bool { return nss[i].name < nss[j].name })
	for _, w := range wls {
		sort.Slice(w.children, func(i, j int) bool { return w.children[i].name < w.children[j].name })
		w.health = hOK
		var ips []string
		for _, c := range w.children {
			if c.health > w.health {
				w.health, w.badge = c.health, c.badge
			}
			if ip, _ := lspAddress(c.gn); ip != "" {
				ips = append(ips, ip)
			}
			for _, r := range c.refs {
				if r.label == "bound on" && len(w.refs) == 0 {
					w.refs = append(w.refs, xref{"runs on", r.to})
				}
			}
		}
		w.summary = strings.Join(ips, ", ")
		if len(w.children) > 1 {
			w.summary = fmt.Sprintf("%d NICs · %s", len(w.children), w.summary)
		}
	}
	for _, n := range nss {
		n.summary = plural(len(n.children), "workload")
		n.health = hOK
		for _, w := range n.children {
			n.health = max(n.health, w.health)
		}
	}

	t.roots[modeVPC] = groups
	t.roots[modeChassis] = chassis
	t.roots[modeWorkload] = nss
	t.stats.vpcs, t.stats.switches, t.stats.chassis = len(vpcs), len(switches), len(chassis)
	t.stats.ports = len(lspByName)
	t.lspByName = lspByName

	m.markChanged(t)
	m.tree = t
	m.rebuildRows()
	m.revalidateDiagTarget()
}

func (m model) edges() []api.GraphEdge {
	if m.graphResp == nil {
		return nil
	}
	return m.graphResp.Edges
}

func (m *model) lspNode(gn *api.GraphNode, ls *xnode, findChassis func(string) *xnode) *xnode {
	p := props(gn)
	typ := pstr(p, "type")
	ip, _ := lspAddress(gn)
	n := &xnode{id: gn.Id, kind: kLSP, name: gn.Name, gn: gn}
	n.refs = append(n.refs, xref{"subnet", ls.id})

	var parts []string
	if ip != "" {
		parts = append(parts, ip)
	}
	if typ != "" {
		parts = append(parts, typ)
	}
	if wk, wname, _, ok := workloadOf(gn); ok {
		label := map[string]string{"VirtualMachine": "VM"}[wk]
		if label == "" {
			label = wk
		}
		if !strings.HasPrefix(gn.Name, wname) {
			label += " " + wname
		}
		parts = append(parts, label)
	}
	ch := findChassis(pstr(p, "boundChassis"))
	if ch != nil {
		n.refs = append(n.refs, xref{"bound on", ch.id})
		parts = append(parts, "@"+ch.name)
	}
	n.summary = strings.Join(parts, " · ")

	switch {
	case infraPortTypes[typ]:
		n.health = hNone
	case pstr(p, "boundState") == "bound":
		n.health = hOK
	default:
		n.health, n.badge = hBad, "unbound"
	}
	if en, ok := p["enabled"].(bool); ok && !en {
		n.health, n.badge = hWarn, "disabled"
	}
	for i := range m.switchPorts[ls.id] {
		c := &m.switchPorts[ls.id][i]
		if c.PeerPortName == gn.Name {
			n.refs = append(n.refs, xref{"router port", lrpID(c)})
		}
	}
	return n
}

func (m *model) lrpNode(c *RouterPortConnection, lrGateway string, lspByName map[string]*xnode, findChassis func(string) *xnode) *xnode {
	n := &xnode{id: lrpID(c), kind: kLRP, name: c.PortName, conn: c, health: hOK}
	nets := strings.Join(c.Networks, ", ")
	switch {
	case c.SwitchName != "" && nets != "":
		n.summary = nets + " → " + c.SwitchName
	case c.SwitchName != "":
		n.summary = "→ " + c.SwitchName
	default:
		n.summary = nets
	}
	if c.SwitchID != "" {
		n.refs = append(n.refs, xref{"peer switch", c.SwitchID})
	}
	if p := lspByName[c.PeerPortName]; p != nil {
		n.refs = append(n.refs, xref{"peer port", p.id})
	}
	active := firstNonEmpty(c.ActiveGateway, lrGateway)
	if active != "" {
		n.badge = "GW active " + active
		if ch := findChassis(active); ch != nil {
			n.badge = "GW active " + ch.name
			n.refs = append(n.refs, xref{"gateway (active)", ch.id})
		}
	}
	return n
}

// workloadOf returns the Kubernetes workload behind an LSP, if any.
func workloadOf(gn *api.GraphNode) (kind, name, ns string, ok bool) {
	if gn == nil || gn.K8s == nil {
		return "", "", "", false
	}
	k := gn.K8s
	kind = deref(k.WorkloadKind)
	name = deref(k.WorkloadName)
	if kind == "" && k.Pod != nil {
		kind, name = "Pod", *k.Pod
	}
	if kind == "" || name == "" {
		return "", "", "", false
	}
	ns = deref(k.Namespace)
	if ns == "" {
		ns = "(cluster)"
	}
	return kind, name, ns, true
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "ch") {
		return fmt.Sprintf("%d %ses", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
