package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/diag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testDaemon = "http://127.0.0.1:8080/api/v1"

// send routes a message through the model. Tests call update (the router that
// Bubble Tea's Update wraps) because the repo's read-only lint forbids any
// `.Update(` call by name.
func send(m model, msg tea.Msg) model {
	r, _ := m.update(msg)
	return r.(model)
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func press(m model, keys ...string) model {
	for _, k := range keys {
		m = send(m, keyMsg(k))
	}
	return m
}

func typeText(m model, s string) model {
	for _, r := range s {
		m = send(m, keyMsg(string(r)))
	}
	return m
}

// loaded returns a model with the fixture graph, every switch's ports and the chassis view loaded.
func loaded(t *testing.T, w, h int, diagMode bool) model {
	t.Helper()
	m := initialModel(testDaemon, "dev-fixture", "default", diagMode, "", "")
	m = send(m, tea.WindowSizeMsg{Width: w, Height: h})
	m = send(m, m.fetchGraphCmd()())
	m = send(m, m.fetchChassisCmd()())
	if m.tree == nil || len(m.rows) == 0 {
		t.Fatalf("expected a populated xray tree, err=%v", m.err)
	}
	return expandAll(m)
}

// expandAll unfolds every level (the default only opens two) so tests can reach any row.
func expandAll(m model) model {
	for n := -1; n != len(m.rows); {
		n = len(m.rows)
		m = press(m, "e")
	}
	return m
}

func rowIndex(m model, kind, name string, ref bool) int {
	for i, r := range m.rows {
		if r.n.kind == kind && r.n.name == name && r.ref == ref {
			return i
		}
	}
	return -1
}

func selectRow(t *testing.T, m model, kind, name string) model {
	t.Helper()
	i := rowIndex(m, kind, name, false)
	if i < 0 {
		t.Fatalf("row %s %q not found in xray %s", kind, name, m.mode)
	}
	m.cursor = i
	return m
}

func assertFits(t *testing.T, label string, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("%s: expected exactly %d lines, got %d", label, h, len(lines))
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Errorf("%s: line %d is %d cells wide, want %d: %q", label, i, lw, w, l)
			return
		}
	}
}

func TestXrayVPCTree(t *testing.T) {
	m := press(loaded(t, 140, 80, false), "i") // full-width tree with every summary
	view := m.View()
	for _, want := range []string{"Xray(vpc)", "VPCS", "ovn_cluster", "lrp-default", "10.244.0.1/24 → default-subnet",
		"default-subnet", "app-pod-1.default", "@node-1", "[unbound]", "[shared by 2 routers]", "EXTERNAL NETWORKS"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in the vpc xray:\n%s", want, view)
		}
	}
	gw := m.tree.byID["lrp:lr:lr-default-uuid/lrp-gw"]
	if gw == nil || gw.badge != "GW active node-1" {
		t.Errorf("expected the gateway-only LRP from LR properties with its active chassis, got %+v", gw)
	}
	if ls := m.tree.byID["ls:ls-default-uuid"]; ls.health != hWarn {
		t.Errorf("a switch with unbound ports must be degraded, got %v", ls.health)
	}
	if p := m.tree.byID["lsp:lsp-unbound-uuid"]; p.health != hBad || p.badge != "unbound" {
		t.Errorf("unbound LSP must be flagged, got %+v", p)
	}
	if r := m.tree.byID["lsp:lsp-def-router-uuid"]; r.health != hNone {
		t.Errorf("router-type LSPs are infrastructure, not unbound workloads; got health %v", r.health)
	}
}

func TestDefaultExpansionTwoLevels(t *testing.T) {
	m := initialModel(testDaemon, "dev-fixture", "default", false, "", "")
	m = send(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = send(m, m.fetchGraphCmd()())
	m = send(m, m.fetchChassisCmd()())
	if rowIndex(m, kLR, "ovn_cluster", false) < 0 || rowIndex(m, kLS, "default-subnet", false) < 0 {
		t.Fatalf("routers and switches must be visible by default")
	}
	if rowIndex(m, kLRP, "lrp-default", false) >= 0 || rowIndex(m, kLSP, "app-pod-1.default", false) >= 0 {
		t.Errorf("router and switch ports must start folded (two levels open)")
	}
	m = press(selectRow(t, m, kLS, "default-subnet"), "l")
	if rowIndex(m, kLSP, "app-pod-1.default", false) < 0 {
		t.Errorf("l must unfold the switch and show its ports")
	}
}

func TestDetailPane(t *testing.T) {
	m := selectRow(t, loaded(t, 140, 40, false), kLR, "ovn_cluster")
	view := ansi.Strip(m.View())
	for _, want := range []string{"Details lr:lr-default-uuid", "Ports & interconnections (3)", "→ LS   default-subnet", "(shared interconnect)", "Active GW on node-1"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in the router's detail pane:\n%s", want, view)
		}
	}

	m = selectRow(t, m, kLSP, "app-pod-1.default")
	view = ansi.Strip(m.View())
	for _, want := range []string{"Path out", "→ LS   default-subnet", "LR   ovn_cluster", "Bound on       node-1"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in the port's detail pane:\n%s", want, view)
		}
	}

	i := rowIndex(m, kChassis, "node-1", true)
	if i < 0 {
		t.Fatalf("expected a bound-on ⇢ chassis relation row")
	}
	m.cursor = i
	if view = ansi.Strip(m.View()); !strings.Contains(view, "Details chassis:") || !strings.Contains(view, "Encap") {
		t.Errorf("on a ⇢ row the pane must describe the target:\n%s", view)
	}

	m = press(m, "i")
	if view = ansi.Strip(m.View()); strings.Contains(view, "Details ") {
		t.Errorf("i must hide the pane")
	}
	if small := ansi.Strip(loaded(t, 80, 24, false).View()); strings.Contains(small, "Details ") {
		t.Errorf("no pane below %d columns", paneMinW)
	}
}

func TestXrayChassisAndWorkloadRoots(t *testing.T) {
	m := press(loaded(t, 140, 50, false), "2")
	if m.mode != modeChassis {
		t.Fatalf("expected chassis xray, got %s", m.mode)
	}
	if rowIndex(m, kLSP, "app-pod-1.default", false) < 0 {
		t.Errorf("expected app-pod-1 bound under its chassis")
	}
	if i := rowIndex(m, kLRP, "lrp-gw", true); i < 0 || m.rows[i].relLabel != "active gateway for" {
		t.Errorf("expected node-1 to show its active gateway role as a relation")
	}

	m = press(m, "3")
	if m.mode != modeWorkload || rowIndex(m, kWL, "app-pod-1", false) < 0 || rowIndex(m, kNS, "tenant-a", false) < 0 {
		t.Errorf("expected namespaces → workloads in the workload xray, rows=%d", len(m.rows))
	}
	if w := m.tree.byID["wl:default/Pod/pending-pod"]; w == nil || w.health != hBad {
		t.Errorf("a workload inherits the worst state of its NICs, got %+v", w)
	}
}

func TestViewAlwaysFitsTerminal(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {60, 14}, {200, 60}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		m := loaded(t, w, h, false)
		screens := map[string]model{
			"vpc":      m,
			"chassis":  press(m, "2"),
			"workload": press(m, "3"),
			"ports":    press(m, "4"),
			"help":     press(m, "?"),
			"expanded": press(m, "e"),
			"filter":   typeText(press(m, "/"), "pod"),
			"command":  typeText(press(m, ":"), "xray ch"),
		}
		d := selectRow(t, m, kLSP, "app-pod-1.default")
		d = press(d, "d")
		screens["describe"] = d
		screens["raw"] = press(d, "y")
		empty := initialModel(testDaemon, "", "default", false, "", "")
		empty = send(empty, tea.WindowSizeMsg{Width: w, Height: h})
		empty.err = fmt.Errorf("connection refused")
		screens["unreachable"] = empty
		for name, s := range screens {
			assertFits(t, fmt.Sprintf("%s@%dx%d", name, w, h), s.View(), w, h)
		}
	}
}

func TestGotoAlongRelationAndBack(t *testing.T) {
	m := selectRow(t, loaded(t, 140, 50, false), kLSP, "app-pod-1.default")
	m = press(m, "l") // unfold the port: its relations appear
	i := rowIndex(m, kChassis, "node-1", true)
	if i < 0 {
		t.Fatalf("expected a 'bound on ⇢ node-1' relation row under the port")
	}
	refKey := m.rows[i].key
	m.cursor = i
	m = press(m, "enter")
	if m.mode != modeChassis {
		t.Fatalf("enter on a relation must jump to the chassis xray, got %s", m.mode)
	}
	if n := m.selectedXNode(); n == nil || n.id != "chassis:chassis-node-1" {
		t.Fatalf("expected node-1 selected after the jump, got %+v", n)
	}
	m = press(m, "b")
	if m.mode != modeVPC || m.selKey() != refKey {
		t.Errorf("b must return to the exact row, got mode=%s key=%s", m.mode, m.selKey())
	}
}

func TestFilterKeepsAncestors(t *testing.T) {
	m := press(typeText(press(loaded(t, 140, 50, false), "/"), "pod-2"), "enter")
	var got []string
	for _, r := range m.rows {
		got = append(got, r.n.name)
	}
	want := []string{"VPCs", "default", "default-subnet", "app-pod-2.default"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("filter rows = %v, want %v", got, want)
	}
	if m = press(m, "esc"); m.filter != "" || len(m.rows) <= len(want) {
		t.Errorf("esc must clear the filter")
	}
}

func TestPortsTable(t *testing.T) {
	m := press(loaded(t, 140, 50, false), "4")
	view := m.View()
	for _, want := range []string{"Ports(lrp)", "ROUTER PORT (LRP)", "lrp-default", "join *", "Inspector", "Routing"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in :ports:\n%s", want, view)
		}
	}
	rows := m.portRows()
	if len(rows) != len(m.portList)+1 {
		t.Errorf("expected router-peer ports plus the gateway-only lrp-gw, got %d rows", len(rows))
	}
	m = press(m, "s") // sort by port name
	rows = m.portRows()
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].conn.PortName < rows[j].conn.PortName }) {
		t.Errorf("expected rows sorted by port name after s")
	}
	for i, r := range rows {
		if r.conn.PortName == "lrp-join-def" {
			m.pcur = i
		}
	}
	if !strings.Contains(m.View(), "Interconnect: 'join' is attached to 2 routers") {
		t.Errorf("expected the inspector to explain the shared join switch")
	}
	m = press(m, "enter")
	if n := m.selectedXNode(); m.mode != modeVPC || n == nil || n.name != "lrp-join-def" {
		t.Fatalf("enter must open the port in the vpc xray, got mode=%s node=%+v", m.mode, n)
	}
	m = press(m, "b")
	if m.mode != modePorts || m.portRows()[m.pcur].conn.PortName != "lrp-join-def" {
		t.Errorf("b must return to the same table row")
	}
}

func TestDescribeLoadsNodeDetail(t *testing.T) {
	m := selectRow(t, loaded(t, 140, 50, false), kLSP, "app-pod-1.default")
	r, cmd := m.update(keyMsg("d"))
	m = r.(model)
	if m.scr != scrDescribe || cmd == nil {
		t.Fatalf("d must open describe and fetch /node detail")
	}
	m = send(m, cmd())
	view := m.View()
	for _, want := range []string{"Describe(lsp)", "app-pod-1.default", "Properties", "Kubernetes", "Related"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in describe:\n%s", want, view)
		}
	}
	if m.desc.err != nil || m.desc.detail == nil {
		t.Errorf("expected node detail to load, err=%v", m.desc.err)
	}
	if m = press(m, "y"); !strings.Contains(m.View(), "Raw(lsp)") {
		t.Errorf("y must toggle raw columns")
	}
	if m = press(m, "esc"); m.scr != scrMain {
		t.Errorf("esc must close describe")
	}
}

func newDiagModel(t *testing.T) model {
	t.Helper()
	fakeClient := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "ovn-central-0", Namespace: "kube-system", Labels: map[string]string{"app": "ovn-central"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "ovs-ovn-node1", Namespace: "kube-system", Labels: map[string]string{"app": "ovs-ovn"}},
			Spec:       corev1.PodSpec{NodeName: "node1"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	mockExec := diag.NewMockExecutor()
	mockExec.StdoutMap["true"] = []byte("")
	mockExec.StdoutMap["ovn-trace"] = []byte("output(\"target-port\");\n")
	mockExec.StdoutMap["ovs-appctl"] = []byte("tcp,orig=(src=10.244.0.5,dst=10.244.0.1)\n")

	m := loaded(t, 140, 50, true)
	m.k8sClient = fakeClient
	m.diagExecutor = mockExec
	m.diagLocator = diag.NewPodLocator(fakeClient, "kube-system")
	probe, ok := m.probeDiagCmd()().(diagProbeMsg)
	if !ok || !probe.available {
		t.Fatalf("expected diag probe to succeed: %+v", probe)
	}
	// Chassis → node comes from the daemon's graph, never from an extra OVN query.
	for _, c := range mockExec.Calls {
		if len(c.Cmd) > 0 && c.Cmd[0] == "ovn-sbctl" {
			t.Fatalf("enabling diagnostics must not query OVN for chassis names: %v", c.Cmd)
		}
	}
	return send(m, probe)
}

func TestDiagnosticsMode(t *testing.T) {
	m := newDiagModel(t)
	view := m.View()
	if !strings.Contains(view, "DIAG") || !strings.Contains(view, "ready") || !strings.Contains(view, "<x>") {
		t.Errorf("expected DIAG ready badge and <x> hotkey in header:\n%s", view)
	}

	// Groups are not diagnosable: x explains instead of opening an empty modal.
	if g := press(m, "g", "x"); g.diagModal || !g.flashErr {
		t.Errorf("x on a group row must not open diagnostics")
	}

	m = selectRow(t, m, kLSP, "app-pod-1.default")
	m = press(m, "x")
	if !m.diagModal {
		t.Fatalf("x on a port must open the diagnostics modal")
	}
	modal := m.View()
	for _, want := range []string{"TIER 1 DIAGNOSTICS", "[1] ovn-trace", "app-pod-1"} {
		if !strings.Contains(modal, want) {
			t.Errorf("expected %q in modal:\n%s", want, modal)
		}
	}
	assertFits(t, "diag modal", modal, 140, 50)

	// The trace source is the selected port (regression: it used to come from the old tree index).
	req, err := m.traceRequest(m.diagTgt, "10.100.0.10", "icmp", 0)
	if err != nil || req.Inport != "app-pod-1.default" || req.SrcIP != "10.244.0.10" || req.Datapath != "default-subnet" {
		t.Errorf("trace must start at the selected workload port, got %+v (%v)", req, err)
	}

	m = press(m, "1")
	if m.diagStep != diagStepPickDest || len(m.traceDestinations) == 0 {
		t.Fatalf("expected the destination picker with candidates")
	}
	picker := m.View()
	for _, want := range []string{"Choose Destination Target:", "Search / Custom IP:", "TCP 22 (SSH)", "Category:"} {
		if !strings.Contains(picker, want) {
			t.Errorf("expected %q in picker", want)
		}
	}
	// ovn0 join ports (OVN overlay) and chassis encap IPs (physical underlay) are separate categories.
	cats := map[string]int{}
	for _, d := range m.traceDestinations {
		cats[d.Category]++
		if d.Category == "Underlay" && d.IP == "192.168.1.11" && !strings.Contains(d.Label, "outside OVN") {
			t.Errorf("underlay destination must say it is outside OVN: %q", d.Label)
		}
	}
	if cats["Node"] != 0 || cats["Underlay"] == 0 {
		t.Errorf("expected a separate Underlay category and no merged Node category, got %v", cats)
	}
	if !strings.Contains(picker, "[Node ovn0 (") || !strings.Contains(picker, "[Underlay (") {
		t.Errorf("expected both Node ovn0 and Underlay tabs in the picker")
	}
	join := collectJoinDest(m)
	if join.Category != "Node ovn0" {
		t.Errorf("a node join port must be in the Node ovn0 category, got %+v", join)
	}
	m.traceSearchFilter = "10.144.97.201:22"
	if f := m.filteredTraceDestinations(); len(f) == 0 || f[0].Category != "Custom" || f[0].IP != "10.144.97.201" {
		t.Errorf("expected custom IP at top of filtered list, got %+v", f)
	}
	m.traceSearchFilter = ""

	dest := m.traceDestinations[0]
	if mock, ok := m.diagExecutor.(*diag.MockExecutor); ok && dest.Port != "" {
		mock.StdoutMap["ovn-trace"] = []byte(`/* output to "` + dest.Port + `", type "" */` + "\n")
	}
	res, ok := m.executeTrace(dest.IP, m.traceExpect(dest), "icmp", 0)().(diagExecResultMsg)
	if !ok || res.err != nil {
		t.Fatalf("unexpected trace result %+v", res)
	}
	m = send(m, res)
	m.diagStep = diagStepResult
	if !strings.Contains(m.View(), "DELIVERED") {
		t.Errorf("expected DELIVERED verdict")
	}

	m.diagStep = diagStepPickTcpdump
	if v := m.View(); !strings.Contains(v, "Live tcpdump: Select Capture Filter") || !strings.Contains(v, "TCP 22 (SSH)") {
		t.Errorf("expected tcpdump preset dialog")
	}
	m.diagStep = diagStepResult
	m.diagStreaming = true
	m.diagAction = "tcpdump"
	m.tcpdumpPackets = append(m.tcpdumpPackets, "18:00:00.000 IP 10.0.0.1 > 10.0.0.2: ICMP echo request")
	m.diagRawOutput = strings.Join(m.tcpdumpPackets, "\n")
	if v := m.View(); !strings.Contains(v, "LIVE CAPTURE ACTIVE") || !strings.Contains(v, "ICMP echo request") {
		t.Errorf("expected live capture view")
	}

	m.diagStreaming = false
	if m = press(m, "x"); m.diagModal {
		t.Errorf("x must close the modal")
	}
}

func TestDiagDisabledWithoutFlag(t *testing.T) {
	m := press(selectRow(t, loaded(t, 120, 40, false), kLSP, "app-pod-1.default"), "x")
	if m.diagModal || !strings.Contains(m.flash, "--diag") {
		t.Errorf("without --diag, x must explain how to enable diagnostics, got flash %q", m.flash)
	}
}

func TestDaemonPaginationAndETag(t *testing.T) {
	var sawETag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("view") {
		case "l3":
			sawETag = r.Header.Get("If-None-Match")
			if sawETag == "7" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_ = json.NewEncoder(w).Encode(api.GraphResponse{Revision: 7, View: "l3"})
		case "l2":
			page, _ := strconv.Atoi(q.Get("page"))
			name := fmt.Sprintf("p%d", page)
			_ = json.NewEncoder(w).Encode(api.GraphResponse{
				Nodes:      []api.GraphNode{{Id: "lsp:" + name, Name: name, Type: api.GraphNodeTypeLSP}},
				Pagination: &api.PaginationInfo{Page: page, PageSize: l2PageSize, TotalPages: 3, TotalItems: 3},
			})
		}
	}))
	defer srv.Close()

	lsps, err := fetchL2All("", srv.URL, "ls:x")
	if err != nil || len(lsps) != 3 {
		t.Fatalf("expected all 3 pages of ports, got %d (%v)", len(lsps), err)
	}

	m := initialModel(srv.URL, "", "default", false, "", "")
	msg, ok := m.fetchGraphCmd()().(graphLoadedMsg)
	if !ok || msg.err != nil || msg.resp.Revision != 7 {
		t.Fatalf("expected first graph load, got %+v", msg)
	}
	m.graphResp = msg.resp
	if _, ok := m.fetchGraphCmd()().(graphUnchangedMsg); !ok || sawETag != "7" {
		t.Errorf("expected If-None-Match: 7 and a 304 → graphUnchangedMsg, sent %q", sawETag)
	}
}

func TestForeignServiceOn404(t *testing.T) {
	foreign := httptest.NewServer(http.NotFoundHandler())
	defer foreign.Close()
	m := initialModel(foreign.URL+"/api/v1", "", "default", false, "", "")
	msg, _ := m.fetchGraphCmd()().(graphLoadedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "404 page not found") || !strings.Contains(msg.err.Error(), "not ovn-topologyd") {
		t.Fatalf("expected body excerpt and foreign-service hint, got %v", msg.err)
	}

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			_ = json.NewEncoder(w).Encode(api.VersionInfo{Version: "0.1.0", ApiVersion: "v1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer daemon.Close()
	m = initialModel(daemon.URL+"/api/v1", "", "default", false, "", "")
	msg, _ = m.fetchGraphCmd()().(graphLoadedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "ovn-topologyd 0.1.0") {
		t.Fatalf("expected daemon version in the error, got %v", msg.err)
	}
}

func TestLiveUpdatesFlashChangedRows(t *testing.T) {
	m := loaded(t, 120, 40, false)
	m.fixturePath = "" // behave like a live daemon

	m, cmd := m.updateLive(liveEventMsg{ev: api.GraphEvent{Revision: m.graphResp.Revision}, stream: &sseStream{}})
	if m.live != liveSSE || cmd == nil {
		t.Errorf("an SSE event must mark the stream live and keep reading")
	}
	m, _ = m.updateLive(liveErrMsg{err: fmt.Errorf("EOF")})
	if m.live != livePolling || !m.flashErr {
		t.Errorf("a broken stream must fall back to polling with a notice")
	}

	// A new revision where the pending pod got bound: its switch and port rows flash.
	m.graphResp.Revision++
	lsps := m.switchWorkloads["ls:ls-default-uuid"]
	for i := range lsps {
		if lsps[i].Name == "pending-pod.default" {
			p := *lsps[i].Properties
			cp := map[string]interface{}{}
			for k, v := range p {
				cp[k] = v
			}
			cp["boundState"], cp["boundChassis"] = "bound", "chassis-node-2"
			lsps[i].Properties = &cp
		}
	}
	m.rebuildTree()
	if _, ok := m.changed["lsp:lsp-unbound-uuid"]; !ok {
		t.Errorf("expected the re-bound port to flash, changed=%v", m.changed)
	}
	if p := m.tree.byID["lsp:lsp-unbound-uuid"]; p.health != hOK {
		t.Errorf("expected the port to be bound after the update")
	}
}

func TestThemes(t *testing.T) {
	for _, name := range []string{"default", "suse", "dracula", "nord", "tokyonight", "catppuccin"} {
		theme, idx := GetThemeByName(name)
		if idx < 0 || idx >= len(AvailableThemes) {
			t.Errorf("invalid theme index for %s: %d", name, idx)
		}
		if CompileStyles(theme).Title.Render("Test") == "" {
			t.Errorf("failed to render title with theme %s", name)
		}
	}
	m := initialModel(testDaemon, "dev-fixture", "dracula", false, "", "")
	if m.theme.Name != "Dracula" {
		t.Errorf("expected Dracula, got %s", m.theme.Name)
	}
	m = loaded(t, 100, 30, false)
	if m = press(m, "t"); m.theme.Name == ThemeDefault.Name {
		t.Errorf("t must cycle the theme")
	}
	if m = typeText(press(m, ":"), "theme nord"); press(m, "enter").theme.Name != "Nord" {
		t.Errorf(":theme nord must switch theme")
	}
}

func TestMachineOutput(t *testing.T) {
	if err := runMachineOutput("", "dev-fixture", "l3", "", "json"); err != nil {
		t.Errorf("expected machine output json to succeed, got: %v", err)
	}
	if err := runMachineOutput("", "dev-fixture", "chassis", "", "yaml"); err != nil {
		t.Errorf("expected machine output yaml to succeed, got: %v", err)
	}
}

func TestRefetchPlanTargetsAffectedSwitches(t *testing.T) {
	m := loaded(t, 120, 40, false)
	cases := []struct {
		changed []string
		want    []string // nil = refetch every switch
		chassis bool
	}{
		{[]string{"lsp:pending-pod.default"}, []string{"ls:ls-default-uuid"}, true}, // SB Port_Binding: by name
		{[]string{"lsp:lsp-pod1-uuid"}, []string{"ls:ls-default-uuid"}, true},       // NB LSP: by uuid
		{[]string{"ls:ls-custom-uuid", "lr:lr-custom-uuid"}, []string{"ls:ls-custom-uuid"}, false},
		{[]string{"chassis:chassis-node-1"}, []string{}, true},
		{[]string{"*"}, nil, true},
		{[]string{"lsp:brand-new-port"}, nil, true},
	}
	for _, c := range cases {
		only, chassis := m.refetchPlan(c.changed)
		if c.want == nil {
			if only != nil {
				t.Errorf("%v: expected a full refetch, got %v", c.changed, only)
			}
			continue
		}
		var got []string
		for id := range only {
			got = append(got, id)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(c.want, ",") || chassis != c.chassis {
			t.Errorf("%v: got switches %v chassis=%v, want %v chassis=%v", c.changed, got, chassis, c.want, c.chassis)
		}
	}
}

func TestDescribeRawIsPrettyJSON(t *testing.T) {
	m := press(loaded(t, 80, 24, false), "2")
	m = selectRow(t, m, kChassis, "node-1")
	long := strings.Repeat("abcdefghij", 30)
	other := map[string]interface{}{"datapath-type": "", "is-interconnection": "false", "long": long}
	for _, k := range []string{"ct-commit-nat-v2", "ct-commit-to-zone", "ct-label-flush", "ct-next-zone", "ct-no-masked-label",
		"ct-state-save", "fdb-timestamp", "iface-types", "mac-binding-timestamp"} {
		other[k] = "true"
	}
	raw := map[string]interface{}{"OtherConfig": other, "Hostname": "node-1", "NbCfg": 42.0, "Transport": nil}
	m.scr, m.desc = scrDescribe, &descState{node: m.selectedXNode(), raw: true, detail: &api.NodeDetail{Raw: raw}}

	lines := m.describeLines()
	var plain []string
	for _, l := range lines {
		s := ansi.Strip(l)
		if strings.Contains(s, "…") {
			t.Errorf("raw view must wrap, not truncate: %q", s)
		}
		if w := lipgloss.Width(l); w > m.w()-2 {
			t.Errorf("raw line wider than the box (%d): %q", w, s)
		}
		plain = append(plain, s)
	}
	text := strings.Join(plain, "\n")
	for _, want := range []string{`"OtherConfig": {`, `"ct-commit-nat-v2": "true",`, `"NbCfg": 42,`, `"Transport": null`} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q on its own line in the raw view:\n%s", want, text)
		}
	}
	// Undo the soft wraps and indentation (no value here contains spaces): the text is
	// still valid JSON and the 300-char value is complete.
	joined := strings.Join(strings.Fields(text), "")
	var back map[string]interface{}
	if err := json.Unmarshal([]byte(joined), &back); err != nil {
		t.Fatalf("raw view must stay valid JSON once wraps are removed: %v\n%s", err, joined)
	}
	if got := back["OtherConfig"].(map[string]interface{})["long"]; got != long {
		t.Errorf("long value cut: %v", got)
	}

	// The describe view unfolds a map that does not fit instead of truncating it.
	m.desc.raw = false
	m.desc.node.gn.Properties = &map[string]interface{}{"options": other}
	desc := ansi.Strip(strings.Join(m.describeLines(), "\n"))
	if strings.Contains(desc, "…") || !strings.Contains(desc, "ct-commit-nat-v2") || !strings.Contains(desc, "{12}") {
		t.Errorf("describe must unfold long maps one key per line:\n%s", desc)
	}
}

func TestChassisPaneExplainsNodeMapping(t *testing.T) {
	m := selectRow(t, press(loaded(t, 150, 40, false), "2"), kChassis, "node-1")
	view := ansi.Strip(m.View())
	for _, want := range []string{"System-id      chassis-node-1", "Node           node-1  ← static mapping", "_uuid"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in the chassis pane:\n%s", want, view)
		}
	}
	if strings.Contains(view, "⚠") {
		t.Errorf("a cleanly mapped chassis must not warn:\n%s", view)
	}

	// hostname-only match, duplicate hostname and an interconnect chassis are flagged
	ch := m.selectedXNode()
	p := *ch.gn.Properties
	cp := map[string]interface{}{}
	for k, v := range p {
		cp[k] = v
	}
	cp["nodeMatch"], cp["hostnameShared"], cp["isInterconn"] = "hostname", 2.0, true
	ch.gn.Properties = &cp
	view = ansi.Strip(m.View())
	for _, want := range []string{"matched by hostname only", "2 chassis share hostname node-1", "interconnect chassis"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected warning %q:\n%s", want, view)
		}
	}

	m.scr, m.desc = scrDescribe, &descState{node: ch, raw: true, detail: &api.NodeDetail{Raw: map[string]interface{}{"hostname": "node-1"}}}
	if !strings.Contains(ansi.Strip(m.View()), "SB Chassis row") {
		t.Errorf("the raw title must name the OVSDB table")
	}
}

// collectJoinDest adds a node join port (node-<host> on join) to the fixture and returns its trace destination.
func collectJoinDest(m model) TraceDestination {
	lsID := "ls:ls-join-uuid"
	props := map[string]interface{}{"addresses": []interface{}{"00:00:00:aa:bb:99 100.64.0.9"}, "type": ""}
	m.switchWorkloads = map[string][]api.GraphNode{lsID: {{Id: "lsp:node-hv-09", Name: "node-hv-09", Type: api.GraphNodeTypeLSP, Properties: &props}}}
	for _, d := range m.collectTraceDestinations(nil, "") {
		if d.IP == "100.64.0.9" {
			return d
		}
	}
	return TraceDestination{}
}
