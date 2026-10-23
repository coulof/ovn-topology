package main

// The diagnostic target: resolved once from the selection when the modal opens
// (x), then the only input of every Tier 1 action. It never borrows a field from
// another object ("first workload of the switch", "first router port", "first
// node of the cluster"): what cannot be derived from the selected object itself
// disables the action, with the reason shown in the menu.

import (
	"fmt"
	"net"
	"strings"

	"github.com/coulof/ovn-topology/pkg/diag"
)

type diagAction string

const (
	actTrace     diagAction = "trace"
	actTcpdump   diagAction = "tcpdump"
	actConntrack diagAction = "conntrack"
	actOvsPort   diagAction = "ovs-port"
	actVsctl     diagAction = "ovs-iface"
	actLogs      diagAction = "logs"
)

// diagActions is the menu order: key "1" runs diagActions[0], and so on.
var diagActions = []diagAction{actTrace, actTcpdump, actConntrack, actOvsPort, actVsctl, actLogs}

var nodeActions = []diagAction{actTcpdump, actConntrack, actOvsPort, actVsctl, actLogs}

type diagTarget struct {
	ID    string // xray node id the target was resolved from (revalidated by id on live updates)
	Kind  string
	Label string // what the user selected, e.g. "LSP app-pod-1.default"
	Owner string // workload the NIC belongs to, when chosen through a workload

	Port     string // logical port: trace inport and OVS iface-id
	Datapath string // LS (switch port) or LR (router port) name
	SwitchID string
	SrcIP    string
	SrcMAC   string
	Subnets  []*net.IPNet // networks of the port's switch (from its router ports)
	// Gateways maps router name → router port MAC on the port's switch. Exactly one
	// entry is needed to trace off-subnet; several means the next hop is ambiguous.
	Gateways map[string]string

	Node       string // Kubernetes node the actions run on
	NodeSource string // where Node comes from, shown in the modal
	Iface      string // host interface when it is known without a lookup
	IfaceNote  string
	// CapturePort is the logical port whose OVS interface tcpdump captures on, when it
	// differs from Port (a distributed join LRP is captured on the chosen node's ovn0).
	CapturePort string

	// ChooseChassis: the object exists on every chassis (distributed router port), so
	// node-scoped actions first ask which chassis to run on. Chassis holds the choice.
	ChooseChassis bool
	Chassis       string

	Warnings []string
	Disabled map[diagAction]string
	NICs     []*xnode // a workload with several NICs: the user picks one first
	Stale    string
}

func (t *diagTarget) disable(reason string, acts ...diagAction) {
	if t.Disabled == nil {
		t.Disabled = map[diagAction]string{}
	}
	for _, a := range acts {
		if _, done := t.Disabled[a]; !done {
			t.Disabled[a] = reason
		}
	}
}

// can reports whether an action may run on the target, or why not.
func (t diagTarget) can(a diagAction) (bool, string) {
	if t.Stale != "" {
		return false, t.Stale
	}
	if len(t.NICs) > 0 {
		return false, "pick a NIC first"
	}
	if r, ok := t.Disabled[a]; ok {
		return false, r
	}
	return true, ""
}

// needsChassis reports whether action a must first ask which chassis to run on.
func (t diagTarget) needsChassis(a diagAction) bool {
	return t.ChooseChassis && t.Chassis == "" && a != actTrace
}

func (t diagTarget) anyEnabled() bool {
	if len(t.NICs) > 0 {
		return true
	}
	for _, a := range diagActions {
		if ok, _ := t.can(a); ok {
			return true
		}
	}
	return false
}

// resolveDiagTarget maps one xray object to a diagnostic target. It is the only
// place that interprets the selection for Tier 1 diagnostics.
func (m model) resolveDiagTarget(n *xnode) diagTarget {
	if n == nil {
		t := diagTarget{}
		t.disable("nothing selected", diagActions...)
		return t
	}
	t := diagTarget{ID: n.id, Kind: n.kind, Label: strings.TrimSpace(kindTag[n.kind] + " " + n.name)}
	switch n.kind {
	case kLSP:
		m.resolveLSP(&t, n)
	case kLRP:
		m.resolveLRP(&t, n)
	case kChassis:
		t.Node, t.NodeSource = m.chassisNode(n), nodeSource(n, "selected chassis")
		t.disable("a chassis is not a traffic endpoint — select one of its ports to trace", actTrace)
		t.disable("select a port to capture its traffic", actTcpdump)
		t.disable("select a port to inspect its OVS interface", actOvsPort)
	case kWL:
		switch len(n.children) {
		case 0:
			t.disable("workload has no NIC in OVN", diagActions...)
		case 1:
			nic := m.resolveDiagTarget(n.children[0])
			nic.Owner = workloadLabel(n)
			return nic
		default:
			t.NICs = n.children
		}
	case kLS:
		t.disable("a switch is not a traffic endpoint — select one of its ports (l to unfold)", diagActions...)
	case kLR:
		t.disable("select one of the router's ports (l to unfold)", diagActions...)
	default:
		t.disable("select a port, a workload or a chassis", diagActions...)
	}
	return t
}

func workloadLabel(n *xnode) string {
	tag := map[string]string{"VirtualMachine": "VM", "Pod": "POD", "Node": "NODE"}[n.wlKind]
	return strings.TrimSpace(firstNonEmpty(tag, n.wlKind) + " " + n.name)
}

// chassisNode is the Kubernetes node of a chassis as the daemon matched it
// (graph.MatchChassis: node annotation, then hostname). Without a match it falls
// back to the OVN hostname, which nodeSource says.
func (m model) chassisNode(ch *xnode) string {
	p := props(ch.gn)
	return firstNonEmpty(pstr(p, "nodeName"), pstr(p, "hostname"), ch.name)
}

// nodeSource describes where a target's node came from, flagging the hostname
// fallback of chassisNode.
func nodeSource(ch *xnode, source string) string {
	if pstr(props(ch.gn), "nodeName") == "" {
		return source + " (OVN hostname: no Kubernetes node matched)"
	}
	return source
}

func (m model) resolveLSP(t *diagTarget, n *xnode) {
	p := props(n.gn)
	typ := pstr(p, "type")
	t.Port = n.name
	t.SrcIP, t.SrcMAC = lspAddress(n.gn)
	for _, r := range n.refs {
		x := m.tree.byID[r.to]
		if x == nil {
			continue
		}
		switch r.label {
		case "subnet":
			t.Datapath, t.SwitchID = x.name, x.id
		case "bound on":
			t.Node, t.NodeSource = m.chassisNode(x), nodeSource(x, "Port_Binding chassis "+x.name)
		}
	}
	t.Gateways = map[string]string{}
	for _, c := range m.switchPorts[t.SwitchID] {
		if mac := extractCleanMAC(c.MAC); mac != "" {
			t.Gateways[c.RouterName] = mac
		}
		for _, cidr := range c.Networks {
			if _, ipn, err := net.ParseCIDR(cidr); err == nil {
				t.Subnets = append(t.Subnets, ipn)
			}
		}
	}
	if n.gn != nil && n.gn.K8s != nil && t.Node != "" {
		if k := deref(n.gn.K8s.Node); k != "" && k != t.Node {
			t.Warnings = append(t.Warnings, fmt.Sprintf("Kubernetes reports node %s but OVN binds the port on %s (live migration?) — using the OVN binding", k, t.Node))
		}
	}

	switch {
	case t.Datapath == "":
		t.disable("switch of this port is unknown", actTrace)
	case t.SrcIP == "":
		t.disable("port has no IP address", actTrace)
	}
	if infraPortTypes[typ] {
		t.disable(fmt.Sprintf("%s port: no host interface — select a chassis for node diagnostics", typ), nodeActions...)
		return
	}
	if t.Node == "" {
		t.disable("port is not bound to any chassis (Port_Binding.chassis empty)", nodeActions...)
	}
	if t.SrcIP == "" {
		t.disable("port has no IP address", actConntrack)
	}
}

func (m model) resolveLRP(t *diagTarget, n *xnode) {
	c := n.conn
	if c == nil {
		t.disable("router port details unavailable", diagActions...)
		return
	}
	t.Port, t.Datapath = c.PortName, c.RouterName
	t.SrcMAC = extractCleanMAC(c.MAC)
	if len(c.Networks) > 0 {
		t.SrcIP = extractCleanIP(c.Networks[0])
	}
	if t.SrcIP == "" || t.SrcMAC == "" {
		t.disable("router port has no network/MAC in NB", actTrace)
	}
	for _, r := range n.refs {
		if strings.HasPrefix(r.label, "gateway") {
			if ch := m.tree.byID[r.to]; ch != nil {
				t.Node, t.NodeSource = m.chassisNode(ch), nodeSource(ch, "active gateway chassis "+ch.name+" (cr-"+c.PortName+" binding)")
			}
		}
	}
	t.disable("router ports have no OVS interface — select a switch port", actOvsPort)
	if t.Node == "" {
		// Distributed: the port is instantiated on every chassis. Node actions run on
		// the chassis the user picks (withChassis), never on an implicit one.
		t.ChooseChassis = true
		if t.SrcIP == "" {
			t.disable("router port has no IP", actConntrack)
		}
		return
	}
	t.Iface, t.IfaceNote = "br-external", "Kube-OVN default provider bridge on the gateway chassis (assumed)"
	if t.SrcIP == "" {
		t.disable("router port has no IP", actConntrack)
	}
}

// revalidateDiagTarget re-resolves the open modal's target by id after the tree
// was rebuilt (live update), so it can never silently follow the cursor elsewhere.
func (m *model) revalidateDiagTarget() {
	if !m.diagModal || m.diagTgt.ID == "" || m.tree == nil {
		return
	}
	old := m.diagTgt
	n := m.tree.byID[old.ID]
	if n == nil {
		m.diagTgt.Stale = old.Label + " no longer exists in the topology — Esc to close"
		return
	}
	if len(old.NICs) > 0 { // still picking: refresh the NIC list only
		m.diagTgt.NICs = n.children
		return
	}
	nt := m.resolveDiagTarget(n)
	nt.Owner = old.Owner
	if old.Chassis != "" && nt.ChooseChassis { // keep the chassis the user picked
		if ch := m.tree.byID[old.Chassis]; ch != nil {
			nt = m.withChassis(nt, ch)
		} else {
			m.setFlash("the chosen chassis is gone — choose another", true)
		}
	}
	if old.Node != "" && nt.Node != old.Node {
		m.setFlash(fmt.Sprintf("%s moved: %s → %s", old.Label, old.Node, firstNonEmpty(nt.Node, "unbound")), true)
	}
	m.diagTgt = nt
}

// traceRequest builds the ovn-trace microflow from the target and a destination.
// Every field comes from the target; nothing defaults to another datapath or IP.
func (m model) traceRequest(t diagTarget, dstIP, proto string, dstPort int) (diag.TraceRequest, error) {
	if ok, why := t.can(actTrace); !ok {
		return diag.TraceRequest{}, fmt.Errorf("trace unavailable: %s", why)
	}
	req := diag.TraceRequest{Datapath: t.Datapath, Inport: t.Port, SrcIP: t.SrcIP, SrcMAC: t.SrcMAC,
		DstIP: dstIP, Protocol: firstNonEmpty(proto, "icmp"), DstPort: dstPort}
	if net.ParseIP(dstIP) == nil {
		return req, fmt.Errorf("invalid destination IP %q", dstIP)
	}
	if t.Kind == kLRP {
		req.DstMAC = t.SrcMAC // lr_in_admission requires eth.dst == router port MAC
		return req, nil
	}
	dst := net.ParseIP(dstIP)
	onLink := len(t.Subnets) == 0 // a switch without router: everything is L2-local
	for _, s := range t.Subnets {
		if s.Contains(dst) {
			onLink = true
		}
	}
	if onLink {
		if mac := m.macOnSwitch(t.SwitchID, dstIP); mac != "" {
			req.DstMAC = mac
			return req, nil
		}
		if len(t.Subnets) == 0 {
			return req, fmt.Errorf("%s is not a port of switch %s and the switch has no router", dstIP, t.Datapath)
		}
	}
	switch len(t.Gateways) {
	case 0:
		return req, fmt.Errorf("switch %s has no router port: cannot route to %s", t.Datapath, dstIP)
	case 1:
		for _, mac := range t.Gateways {
			req.DstMAC = mac
		}
		return req, nil
	default:
		var rs []string
		for r := range t.Gateways {
			rs = append(rs, r)
		}
		return req, fmt.Errorf("switch %s is attached to %d routers (%s): the next hop is ambiguous — trace from the router port instead",
			t.Datapath, len(rs), strings.Join(uniq(rs), ", "))
	}
}

func (m model) macOnSwitch(lsID, ip string) string {
	for _, w := range m.switchWorkloads[lsID] {
		addrs, _ := props(&w)["addresses"].([]interface{})
		for _, a := range addrs {
			s, _ := a.(string)
			if extractCleanIP(s) == ip {
				return extractCleanMAC(s)
			}
		}
	}
	return ""
}

// describe renders the target for the modal header: what, where, and why.
func (t diagTarget) describe() []string {
	head := t.Label
	if t.Owner != "" {
		head = t.Owner + " · " + head
	}
	lines := []string{head + "  (" + t.ID + ")"}
	var facts []string
	if t.Datapath != "" {
		facts = append(facts, "datapath "+t.Datapath)
	}
	if t.SrcIP != "" {
		facts = append(facts, "ip "+t.SrcIP)
	}
	if t.SrcMAC != "" {
		facts = append(facts, "mac "+t.SrcMAC)
	}
	if len(facts) > 0 {
		lines = append(lines, strings.Join(facts, " · "))
	}
	if t.Node != "" {
		lines = append(lines, "node "+t.Node+"  ← "+t.NodeSource)
	}
	if t.Iface != "" {
		lines = append(lines, "iface "+t.Iface+"  ← "+t.IfaceNote)
	}
	return lines
}

// withChassis pins a distributed router port's node-scoped actions to one chassis.
// tcpdump needs a host interface: on the join switch that is the chosen node's
// ovn0 (its node-<host> port, looked up by iface-id); other distributed ports are
// routed inside OVS and have none, so the capture stays unavailable.
func (m model) withChassis(t diagTarget, ch *xnode) diagTarget {
	t.Chassis = ch.id
	t.Node, t.NodeSource = m.chassisNode(ch), nodeSource(ch, "chosen chassis "+ch.name+" (distributed port)")
	lrp := m.tree.byID[t.ID]
	if lrp == nil || lrp.conn == nil {
		t.disable("router port no longer exists", actTcpdump)
		return t
	}
	for _, c := range m.tree.byID[lrp.conn.SwitchID].childrenOrNil() {
		if c.kind != kLSP || !strings.HasPrefix(c.name, "node-") {
			continue
		}
		for _, r := range c.refs {
			if r.label == "bound on" && r.to == ch.id {
				t.CapturePort = c.name
				t.IfaceNote = "ovn0 of " + t.Node + " (join port " + c.name + ")"
			}
		}
	}
	if t.CapturePort == "" {
		t.disable("routed inside OVS: no host interface — capture on a switch port", actTcpdump)
	}
	return t
}
