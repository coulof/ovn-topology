package main

import (
	"bufio"
	"fmt"
	"strings"
	"testing"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/diag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// ownNode is the test oracle: the node an object itself is bound to, derived
// only from that object's own relations.
func ownNode(m model, n *xnode) string {
	for _, r := range n.refs {
		if r.label == "bound on" || strings.HasPrefix(r.label, "gateway") {
			return m.chassisNode(m.tree.byID[r.to])
		}
	}
	if n.kind == kChassis {
		return m.chassisNode(n)
	}
	return ""
}

func refTarget(n *xnode, label string) string {
	for _, r := range n.refs {
		if r.label == label {
			return r.to
		}
	}
	return ""
}

// TestDiagTargetIsAlwaysTheSelection walks every row of every xray and the
// :ports table: the target must be built from the selected object only.
func TestDiagTargetIsAlwaysTheSelection(t *testing.T) {
	base := loaded(t, 140, 50, false)
	check := func(where string, n *xnode, tg diagTarget) {
		t.Helper()
		obj := n
		if n.kind == kWL && len(n.children) == 1 {
			obj = n.children[0] // a single-NIC workload is that NIC
		}
		if tg.ID != obj.id {
			t.Errorf("%s: target id %s, want %s", where, tg.ID, obj.id)
		}
		if tg.Node != ownNode(base, obj) {
			t.Errorf("%s: node %q, want the object's own binding %q", where, tg.Node, ownNode(base, obj))
		}
		switch obj.kind {
		case kLSP:
			ls := base.tree.byID[refTarget(obj, "subnet")]
			if tg.Port != obj.name || ls == nil || tg.Datapath != ls.name {
				t.Errorf("%s: LSP target port=%q datapath=%q", where, tg.Port, tg.Datapath)
			}
			if ip, _ := lspAddress(obj.gn); tg.SrcIP != ip {
				t.Errorf("%s: source IP %q, want the port's own %q", where, tg.SrcIP, ip)
			}
		case kLRP:
			if tg.Port != obj.conn.PortName || tg.Datapath != obj.conn.RouterName {
				t.Errorf("%s: LRP target port=%q datapath=%q", where, tg.Port, tg.Datapath)
			}
		case kChassis:
			for _, a := range []diagAction{actTrace, actTcpdump, actOvsPort} {
				if ok, _ := tg.can(a); ok {
					t.Errorf("%s: %s must be disabled on a chassis", where, a)
				}
			}
		case kLS, kLR, kVPC, kGroup, kNS, kExt:
			if tg.anyEnabled() {
				t.Errorf("%s: %s is not a diagnostic endpoint; no action may borrow one of its ports", where, obj.kind)
			}
		}
		if tg.Port == "" && tg.Datapath != "" {
			t.Errorf("%s: datapath without port", where)
		}
	}
	for _, md := range []string{"1", "2", "3"} {
		m := expandAll(press(base, md))
		for i, r := range m.rows {
			m.cursor = i
			check(string(m.mode)+"/"+r.key, r.n, m.resolveDiagTarget(m.selectedXNode()))
		}
	}
	m := press(base, "4")
	for i, r := range m.portRows() {
		m.pcur = i
		check("ports/"+r.conn.PortName, m.selectedXNode(), m.resolveDiagTarget(m.selectedXNode()))
	}
}

// newClusterDiagModel has one OVS and one CNI pod per node, so tests can see which node an action ran on.
func newClusterDiagModel(t *testing.T, mock *diag.MockExecutor) model {
	t.Helper()
	objs := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ovn-central-0", Namespace: "kube-system", Labels: map[string]string{"app": "ovn-central"}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	for _, n := range []string{"node-1", "node-2", "node-3"} {
		objs = append(objs,
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ovs-ovn-" + n, Namespace: "kube-system", Labels: map[string]string{"app": "ovs-ovn"}},
				Spec: corev1.PodSpec{NodeName: n}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kube-ovn-cni-" + n, Namespace: "kube-system", Labels: map[string]string{"app": "kube-ovn-cni"}},
				Spec: corev1.PodSpec{NodeName: n}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	client := fake.NewSimpleClientset(objs...)
	mock.StdoutMap["true"] = []byte("")
	m := loaded(t, 140, 50, true)
	m.k8sClient = client
	m.diagExecutor = mock
	m.diagLocator = diag.NewPodLocator(client, "kube-system")
	m.diagAvailable = true
	return m
}

func lastCall(t *testing.T, mock *diag.MockExecutor) diag.MockExecCall {
	t.Helper()
	if len(mock.Calls) == 0 {
		t.Fatalf("expected an exec call")
	}
	return mock.Calls[len(mock.Calls)-1]
}

// TestDiagActionsExecuteOnTheSelectedPort runs every action on a port bound on
// node-2 and checks, from the executor, which pod and arguments were used.
func TestDiagActionsExecuteOnTheSelectedPort(t *testing.T) {
	mock := diag.NewMockExecutor()
	mock.StdoutMap["ovs-vsctl"] = []byte("abc123_h\n")
	m := newClusterDiagModel(t, mock)
	m = press(selectRow(t, m, kLSP, "app-pod-2.default"), "x")
	if !m.diagModal || m.diagTgt.Node != "node-2" || !strings.Contains(m.View(), "Port_Binding chassis node-2") {
		t.Fatalf("modal must show the captured target and its node source:\n%s", m.View())
	}

	run := func(key string) diagExecResultMsg {
		t.Helper()
		mm, cmd := m.updateDiagModal(keyMsg(key))
		if cmd == nil {
			t.Fatalf("action %s returned no command (err=%v)", key, mm.(model).diagError)
		}
		res, _ := cmd().(diagExecResultMsg)
		if res.err != nil {
			t.Fatalf("action %s failed: %v", key, res.err)
		}
		return res
	}
	for _, key := range []string{"3", "4", "5", "6"} {
		res := run(key)
		if c := lastCall(t, mock); c.Pod.Name != "ovs-ovn-node-2" {
			t.Errorf("action %s ran in %s, want the OVS pod of node-2", key, c.Pod.Name)
		}
		if !strings.Contains(res.title, "node-2") {
			t.Errorf("action %s title must name the node: %q", key, res.title)
		}
	}
	found := false
	for _, c := range mock.Calls {
		if strings.Join(c.Cmd, " ") == "ovs-vsctl --bare --columns=name find interface external_ids:iface-id=app-pod-2.default" && c.Pod.Name == "ovs-ovn-node-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("the OVS port lookup must use the selected port's iface-id on its node; calls: %+v", mock.Calls)
	}

	// trace: inport, source IP and MAC are the selected port's own.
	res, _ := m.executeTrace("10.244.0.10", diag.TraceExpect{}, "icmp", 0)().(diagExecResultMsg)
	if res.err != nil {
		t.Fatalf("trace failed: %v", res.err)
	}
	trace := strings.Join(lastCall(t, mock).Cmd, " ")
	if !strings.Contains(trace, `inport == "app-pod-2.default"`) || !strings.Contains(trace, "ip4.src == 10.244.0.11") ||
		lastCall(t, mock).Pod.Name != "ovn-central-0" {
		t.Errorf("trace must start at the selected port: %s", trace)
	}

	// tcpdump: the capture interface is looked up on the port's node, off the UI goroutine.
	cmd := m.executeTcpdumpStream("icmp", m.diagTgt)
	started, _ := cmd().(tcpdumpStartedMsg)
	if started.err != nil || !strings.Contains(started.title, "abc123_h") || !strings.Contains(started.title, "node-2") {
		t.Errorf("tcpdump must capture on the port's own interface on node-2: %+v", started)
	}
}

// TestDiagNeverGuesses: a missing interface or node stops the action instead of
// running it on ovn0 / br-int / the first node, as earlier versions did.
func TestDiagNeverGuesses(t *testing.T) {
	mock := diag.NewMockExecutor()
	mock.StdoutMap["ovs-vsctl"] = []byte("") // iface-id not found on the node
	m := newClusterDiagModel(t, mock)
	m = press(selectRow(t, m, kLSP, "app-pod-2.default"), "x")

	started, _ := m.executeTcpdumpStream("icmp", m.diagTgt)().(tcpdumpStartedMsg)
	if started.err == nil || !strings.Contains(started.err.Error(), "app-pod-2.default") {
		t.Errorf("tcpdump must fail when the port has no interface, got %+v", started)
	}
	for _, c := range mock.Calls {
		if c.Cmd[0] == "tcpdump" {
			t.Errorf("no capture may start on a guessed interface: %v", c.Cmd)
		}
	}
	if res, _ := m.executeNodeDiag(actOvsPort, m.diagTgt)().(diagExecResultMsg); res.err == nil {
		t.Errorf("OVS port telemetry must fail instead of showing br-int")
	}

	// Unbound port: node actions are disabled, nothing is executed.
	m.diagModal, m.diagStep, m.diagStreaming = false, diagStepMenu, false
	m = press(selectRow(t, m, kLSP, "pending-pod.default"), "x")
	before := len(mock.Calls)
	m = press(m, "3")
	if len(mock.Calls) != before || m.diagError == nil || !strings.Contains(m.diagError.Error(), "not bound") {
		t.Errorf("conntrack on an unbound port must be refused with the reason, err=%v", m.diagError)
	}

	// Chassis: no trace (it used to start from the first router port of the cluster).
	m.diagModal = false
	m = press(press(m, "2"), "g", "x")
	if m.diagTgt.Kind != kChassis {
		t.Fatalf("expected a chassis target, got %+v", m.diagTgt)
	}
	if _, err := m.traceRequest(m.diagTgt, "1.1.1.1", "icmp", 0); err == nil {
		t.Errorf("a trace from a chassis must be refused")
	}
	if !strings.Contains(m.View(), "unavailable") {
		t.Errorf("disabled actions must be listed with their reason:\n%s", m.View())
	}

	// Switch: no action borrows one of its pods.
	m.diagModal = false
	m = press(m, "1")
	m = press(selectRow(t, m, kLS, "default-subnet"), "x")
	if m.diagModal || !strings.Contains(m.flash, "select one of its ports") {
		t.Errorf("x on a switch must explain instead of picking a pod, flash=%q", m.flash)
	}
}

func TestTraceNextHopIsUnambiguous(t *testing.T) {
	m := loaded(t, 140, 50, false)
	tg := m.resolveDiagTarget(m.tree.byID["lsp:lsp-pod1-uuid"])
	off, err := m.traceRequest(tg, "8.8.8.8", "icmp", 0)
	if err != nil || off.DstMAC != tg.Gateways["ovn_cluster"] || off.DstMAC == "" {
		t.Errorf("off-subnet traffic goes to the subnet's router MAC, got %+v (%v)", off, err)
	}
	on, err := m.traceRequest(tg, "10.244.0.11", "icmp", 0)
	if err != nil || on.DstMAC == "" || on.DstMAC == off.DstMAC {
		t.Errorf("on-subnet traffic goes to the peer's own MAC, got %+v (%v)", on, err)
	}
	tg.Gateways = map[string]string{"r1": "00:00:00:00:00:01", "r2": "00:00:00:00:00:02"}
	if _, err := m.traceRequest(tg, "8.8.8.8", "icmp", 0); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("a switch with two routers must not pick one silently, err=%v", err)
	}
}

// TestDiagTargetSurvivesLiveUpdates: the open modal follows its object by id,
// never the cursor.
func TestDiagTargetSurvivesLiveUpdates(t *testing.T) {
	m := newClusterDiagModel(t, diag.NewMockExecutor())
	m = press(selectRow(t, m, kLSP, "app-pod-2.default"), "x")
	lsID := refTarget(m.tree.byID[m.diagTgt.ID], "subnet")

	// the port moves to node-1 (e.g. live migration)
	ports := m.switchWorkloads[lsID]
	for i := range ports {
		if ports[i].Name == "app-pod-2.default" {
			p := *ports[i].Properties
			cp := map[string]interface{}{}
			for k, v := range p {
				cp[k] = v
			}
			cp["boundChassis"] = "chassis-node-1"
			ports[i].Properties = &cp
		}
	}
	m.rebuildTree()
	if m.diagTgt.Node != "node-1" || !strings.Contains(m.flash, "node-2 → node-1") {
		t.Errorf("target must follow its port to node-1, got node=%q flash=%q", m.diagTgt.Node, m.flash)
	}

	// the port disappears: the target goes stale instead of jumping to another row
	var keep []api.GraphNode
	for _, p := range m.switchWorkloads[lsID] {
		if p.Name != "app-pod-2.default" {
			keep = append(keep, p)
		}
	}
	m.switchWorkloads[lsID] = keep
	m.rebuildTree()
	if ok, why := m.diagTgt.can(actConntrack); ok || !strings.Contains(why, "no longer exists") {
		t.Errorf("a removed target must disable every action, got ok=%v why=%q", ok, why)
	}
}

func TestDiagMultiNICWorkloadAsksWhichNIC(t *testing.T) {
	m := newClusterDiagModel(t, diag.NewMockExecutor())
	m = press(m, "3")
	wl := m.tree.byID["wl:default/Pod/app-pod-1"]
	wl.children = append(wl.children, m.tree.byID["lsp:lsp-pod2-uuid"])
	m = press(selectRow(t, m, kWL, "app-pod-1"), "x")
	if len(m.diagTgt.NICs) != 2 || !strings.Contains(m.View(), "several NICs") {
		t.Fatalf("a multi-NIC workload must ask for the NIC first:\n%s", m.View())
	}
	if ok, _ := m.diagTgt.can(actTrace); ok {
		t.Errorf("no action before a NIC is chosen")
	}
	m = press(m, "2")
	if m.diagTgt.Port != "app-pod-2.default" || m.diagTgt.Owner != "POD app-pod-1" {
		t.Errorf("choosing NIC 2 must target it, got %+v", m.diagTgt)
	}
}

// TestDistributedRouterPortAsksWhichChassis: a distributed LRP exists on every
// chassis, so node actions ask for one instead of being refused (or guessed).
func TestDistributedRouterPortAsksWhichChassis(t *testing.T) {
	mock := diag.NewMockExecutor()
	mock.StdoutMap["ovs-vsctl"] = []byte("ovn0\n")
	m := newClusterDiagModel(t, mock)
	m = press(selectRow(t, m, kLRP, "lrp-join-def"), "x")
	if !m.diagTgt.ChooseChassis || m.diagTgt.Node != "" || !strings.Contains(m.View(), "asks which chassis") {
		t.Fatalf("a distributed LRP must offer a chassis choice:\n%s", m.View())
	}
	if ok, _ := m.diagTgt.can(actTrace); !ok {
		t.Errorf("trace needs no chassis")
	}

	// [3] conntrack → chassis picker → [2] node-2 → runs on node-2 only
	m = press(m, "3")
	if m.diagStep != diagStepPickChassis || !strings.Contains(m.View(), "on which one?") {
		t.Fatalf("a node action must open the chassis picker:\n%s", m.View())
	}
	r, cmd := m.updateDiagModal(keyMsg("2"))
	m = r.(model)
	if m.diagTgt.Node != "node-2" || cmd == nil {
		t.Fatalf("choosing chassis 2 must pin node-2 and run the pending action, node=%q", m.diagTgt.Node)
	}
	if res, _ := cmd().(diagExecResultMsg); res.err != nil || lastCall(t, mock).Pod.Name != "ovs-ovn-node-2" {
		t.Errorf("conntrack must run on the chosen chassis, got %s (%v)", lastCall(t, mock).Pod.Name, res.err)
	}

	// No join port of node-2 yet: tcpdump has no host interface and says so.
	if ok, why := m.diagTgt.can(actTcpdump); ok || !strings.Contains(why, "no host interface") {
		t.Errorf("tcpdump without a node join port must be unavailable, got ok=%v %q", ok, why)
	}

	// node-2's ovn0 join port appears (live update): the chosen chassis is kept and
	// tcpdump captures on that node's ovn0, filtered on the router port IP.
	props := map[string]interface{}{"addresses": []interface{}{"00:00:00:aa:bb:92 100.64.0.12"}, "type": "",
		"boundChassis": "chassis-node-2", "boundState": "bound"}
	m.switchWorkloads["ls:ls-join-uuid"] = append(m.switchWorkloads["ls:ls-join-uuid"],
		api.GraphNode{Id: "lsp:node-node-2", Name: "node-node-2", Type: api.GraphNodeTypeLSP, Properties: &props})
	m.diagModal, m.diagStep = true, diagStepMenu
	m.rebuildTree()
	if m.diagTgt.Node != "node-2" || m.diagTgt.CapturePort != "node-node-2" {
		t.Fatalf("live update must keep the chosen chassis and find its join port, got %+v", m.diagTgt)
	}
	started, _ := m.executeTcpdumpStream("icmp", m.diagTgt)().(tcpdumpStartedMsg)
	if started.err != nil || !strings.Contains(started.title, "ovn0") || !strings.Contains(started.title, "host 100.64.0.1") ||
		!strings.Contains(started.title, "node-2") {
		t.Errorf("tcpdump must capture on node-2's ovn0 for the join router port: %+v", started)
	}
}

// TestTcpdumpRendersLive: packets show while the capture runs (not when it ends),
// and the view follows the newest ones until the user scrolls up.
func TestTcpdumpRendersLive(t *testing.T) {
	mock := diag.NewMockExecutor()
	mock.StdoutMap["ovs-vsctl"] = []byte("abc123_h\n")
	m := newClusterDiagModel(t, mock)
	m = press(selectRow(t, m, kLSP, "app-pod-2.default"), "x")
	m.height = 30

	cmd := m.executeTcpdumpStream("icmp", m.diagTgt)
	if v := m.View(); !strings.Contains(v, "resolving the interface of app-pod-2.default on node-2") {
		t.Errorf("while the interface is looked up, the modal must say so:\n%s", v)
	}
	started, _ := cmd().(tcpdumpStartedMsg)
	if started.err != nil {
		t.Fatalf("tcpdump not started: %v", started.err)
	}
	var pkts []string
	for i := 1; i <= 60; i++ {
		pkts = append(pkts, fmt.Sprintf("18:00:%02d.000 IP 10.244.0.11 > 10.244.0.10: ICMP echo request, seq %d", i, i))
	}
	started.scanner = bufio.NewScanner(strings.NewReader(strings.Join(pkts, "\n")))

	r, next := m.update(started)
	m = r.(model)
	if m.diagRunning || !m.diagStreaming || !strings.Contains(m.View(), "LIVE CAPTURE ACTIVE") {
		t.Fatalf("a started capture must show the live view (running=%v streaming=%v)", m.diagRunning, m.diagStreaming)
	}
	for i := 0; i < 3; i++ {
		r, next = m.update(next())
		m = r.(model)
	}
	if v := m.View(); !strings.Contains(v, "seq 3") {
		t.Errorf("packets must render as they arrive:\n%s", v)
	}
	for i := 3; i < 60; i++ {
		r, next = m.update(next())
		m = r.(model)
	}
	if v := m.View(); !strings.Contains(v, "seq 60") || strings.Contains(v, "18:00:01.000") || !strings.Contains(v, "following") {
		t.Errorf("the live view must follow the newest packets:\n%s", v)
	}
	m = press(m, "k")
	if m.diagFollow || !strings.Contains(m.View(), "paused") || strings.Contains(m.View(), "seq 60") {
		t.Errorf("scrolling up must pause following, one line above the bottom")
	}
	m = press(m, "G")
	if !m.diagFollow || !strings.Contains(m.View(), "seq 60") {
		t.Errorf("G must resume following")
	}
}

// TestChassisNodeFallbackIsFlagged: a chassis the daemon could not match to a
// Kubernetes node falls back to its OVN hostname, and the target says so.
func TestChassisNodeFallbackIsFlagged(t *testing.T) {
	matched := &xnode{name: "hv-01", gn: &api.GraphNode{Properties: &map[string]interface{}{"nodeName": "node-a", "hostname": "hv-01"}}}
	unmatched := &xnode{name: "hv-02", gn: &api.GraphNode{Properties: &map[string]interface{}{"hostname": "hv-02"}}}
	var m model
	if got := m.chassisNode(matched); got != "node-a" {
		t.Errorf("matched chassis must use the daemon's nodeName, got %q", got)
	}
	if src := nodeSource(matched, "selected chassis"); src != "selected chassis" {
		t.Errorf("matched chassis source must not be flagged: %q", src)
	}
	if got := m.chassisNode(unmatched); got != "hv-02" {
		t.Errorf("unmatched chassis falls back to the hostname, got %q", got)
	}
	if src := nodeSource(unmatched, "selected chassis"); !strings.Contains(src, "no Kubernetes node matched") {
		t.Errorf("hostname fallback must be flagged: %q", src)
	}
}

func TestRouterPortDiagnostics(t *testing.T) {
	mock := diag.NewMockExecutor()
	m := newClusterDiagModel(t, mock)

	// 1. Internal distributed LRP (lrp-default on default-subnet, not join switch)
	mDist := press(selectRow(t, m, kLRP, "lrp-default"), "x")
	tgDist := mDist.diagTgt
	if ok, why := tgDist.can(actTcpdump); ok || !strings.Contains(why, "no host interface") {
		t.Errorf("internal distributed LRP must have tcpdump disabled upfront: ok=%v why=%q", ok, why)
	}
	if len(tgDist.Warnings) == 0 || !strings.Contains(tgDist.Warnings[0], "br-int") {
		t.Errorf("internal distributed LRP must warn about br-int pipeline: %+v", tgDist.Warnings)
	}
	if ok, _ := tgDist.can(actTrace); !ok {
		t.Errorf("ovn-trace must remain available on internal LRP")
	}

	// 2. Gateway LRP (lrp-gw on node-1 via br-external)
	mGW := press(selectRow(t, m, kLRP, "lrp-gw"), "x")
	tgGW := mGW.diagTgt
	if tgGW.Iface != "br-external" || tgGW.Node != "node-1" {
		t.Fatalf("gateway LRP must target br-external on node-1, got iface=%q node=%q", tgGW.Iface, tgGW.Node)
	}
	if len(tgGW.Subnets) == 0 {
		t.Errorf("gateway LRP must collect router's attached subnets for filter matching")
	}
	cmd := mGW.executeTcpdumpStream("icmp", tgGW)
	started, _ := cmd().(tcpdumpStartedMsg)
	if started.err != nil {
		t.Fatalf("tcpdump on gateway LRP failed: %v", started.err)
	}
	// Filter must cover both router port IP and router subnets (e.g. 10.244.0.0/24)
	if !strings.Contains(started.title, "10.244.0.0/24") {
		t.Errorf("gateway tcpdump filter must match attached subnet, got title: %s", started.title)
	}
}
