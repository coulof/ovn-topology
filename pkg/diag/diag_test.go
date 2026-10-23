package diag

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuildTraceCommand(t *testing.T) {
	tests := []struct {
		name        string
		req         TraceRequest
		wantErr     bool
		errContains string
		wantCmd     string
	}{
		{
			name: "valid icmp trace",
			req: TraceRequest{
				Datapath: "default-subnet",
				Inport:   "test-pod.default",
				SrcMAC:   "00:00:00:aa:bb:cc",
				DstMAC:   "00:00:00:dd:ee:ff",
				SrcIP:    "10.244.0.10",
				DstIP:    "10.244.0.20",
				Protocol: "icmp",
			},
			wantErr: false,
			wantCmd: `ovn-trace --ct=new --ct=new --ct=new --ct=new default-subnet inport == "test-pod.default" && eth.src == 00:00:00:aa:bb:cc && eth.dst == 00:00:00:dd:ee:ff && ip.ttl == 255 && ip4.src == 10.244.0.10 && ip4.dst == 10.244.0.20 && ip.proto == 1 && icmp4.type == 8 && icmp4.code == 0`,
		},
		{
			name: "valid tcp trace with custom ports",
			req: TraceRequest{
				Datapath: "ovn_cluster",
				Inport:   "lrp-join",
				SrcIP:    "100.64.0.2",
				DstIP:    "10.244.1.5",
				Protocol: "tcp",
				SrcPort:  54321,
				DstPort:  8080,
				CTState:  "est",
			},
			wantErr: false,
			wantCmd: `ovn-trace --ct=new --ct=new --ct=new --ct=new ovn_cluster inport == "lrp-join" && ip.ttl == 255 && ip4.src == 100.64.0.2 && ip4.dst == 10.244.1.5 && ip.proto == 6 && tcp.src == 54321 && tcp.dst == 8080`,
		},
		{
			name: "injection attempt in inport",
			req: TraceRequest{
				Datapath: "default-subnet",
				Inport:   "test; rm -rf /",
				SrcIP:    "10.244.0.10",
				DstIP:    "10.244.0.20",
			},
			wantErr:     true,
			errContains: "invalid characters",
		},
		{
			name: "injection attempt in src IP",
			req: TraceRequest{
				Datapath: "default-subnet",
				Inport:   "test",
				SrcIP:    "10.0.0.1$(whoami)",
				DstIP:    "10.244.0.20",
			},
			wantErr:     true,
			errContains: "invalid characters",
		},
		{
			name: "missing datapath",
			req: TraceRequest{
				Inport: "test",
				SrcIP:  "10.0.0.1",
				DstIP:  "10.0.0.2",
			},
			wantErr:     true,
			errContains: "datapath cannot be empty",
		},
		{
			name: "invalid protocol",
			req: TraceRequest{
				Datapath: "default-subnet",
				Inport:   "test",
				SrcIP:    "10.0.0.1",
				DstIP:    "10.0.0.2",
				Protocol: "sctp",
			},
			wantErr:     true,
			errContains: "unsupported protocol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := BuildTraceCommand(tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildTraceCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %v", tt.errContains, err)
				}
				return
			}
			if !tt.wantErr {
				joined := strings.Join(cmd, " ")
				if joined != tt.wantCmd {
					t.Errorf("BuildTraceCommand()\ngot:  %s\nwant: %s", joined, tt.wantCmd)
				}
			}
		})
	}
}

func TestParseTraceOutput(t *testing.T) {
	deliveredSample := `
# ingress(input: "pod1.default", mac: "00:00:00:01:02:03")
------------------------------------------------------------
 0. ls_in_port_sec_l2 (ovn-northd.c:7531): inport == "pod1.default", priority 100, uuid d49f57f6
    next;
 1. ls_in_pre_acl (ovn-northd.c:7640): ip, priority 100, uuid e1052fa9
    next;
 2. ls_in_acl (ovn-northd.c:8012): ip4, priority 1000, uuid fb01a912
    next;
13. ls_in_l2_lkup (ovn-northd.c:9200): eth.dst == 00:00:00:04:05:06, priority 50, uuid 37cae019
    outport = "pod2.default";
    output;

# egress(input: "pod2.default", output: "pod2.default")
-------------------------------------------------------
 0. ls_out_pre_acl (ovn-northd.c:7640): ip, priority 100, uuid d8ba458a
    next;
 8. ls_out_port_sec_l2 (ovn-northd.c:7531): outport == "pod2.default", priority 100, uuid c12fbc90
    output("pod2.default");
`
	res := ParseTraceOutput(deliveredSample, TraceExpect{Port: "pod2.default"})
	if res.Verdict != "delivered" {
		t.Errorf("expected verdict delivered, got %s", res.Verdict)
	}
	if res.FinalPort != "pod2.default" {
		t.Errorf("expected final port pod2.default, got %s", res.FinalPort)
	}
	if len(res.Stages) == 0 {
		t.Errorf("expected parsed stages, got 0")
	}

	dropSample := `
# ingress(input: "pod1.default")
 2. ls_in_acl: ip4 && tcp.dst == 22, priority 2000, uuid abc
    drop;
`
	dropRes := ParseTraceOutput(dropSample, TraceExpect{})
	if dropRes.Verdict != "dropped" {
		t.Errorf("expected verdict dropped, got %s", dropRes.Verdict)
	}
	if !strings.Contains(dropRes.Summary, "dropped by ACL") {
		t.Errorf("expected ACL drop explanation, got %q", dropRes.Summary)
	}

	martianSample := `
ingress(dp="lr", inport="lr-ext")
---------------------------------
 3. lr_in_ip_input (northd.c:16921): ip4.src == {10.144.124.13, 10.144.124.31} && reg9[0] == 0, priority 100, uuid ae17e09d
    drop;
`
	martianRes := ParseTraceOutput(martianSample, TraceExpect{})
	if martianRes.Verdict != "dropped" {
		t.Errorf("expected verdict dropped, got %s", martianRes.Verdict)
	}
	if !strings.Contains(martianRes.Summary, "Martian/spoofed packet") {
		t.Errorf("expected Martian drop explanation, got %q", martianRes.Summary)
	}
}

// A cross-switch trace in ovn-trace's detailed format: the packet crosses two patch
// ports (switch → router → switch) and ends on the destination VIF.
const crossSwitchTrace = `
ingress(dp="ls-a", inport="pod1.default")
-----------------------------------------
24. ls_in_l2_lkup (northd.c:7930): eth.dst == 00:00:00:00:00:01, priority 50, uuid 2f4b1a5f
    outport = "ls-a-lr";
    output;

egress(dp="ls-a", inport="pod1.default", outport="ls-a-lr")
-----------------------------------------------------------
 9. ls_out_port_sec_l2 (northd.c:5704): outport == "ls-a-lr", priority 50, uuid 0c1e
    output;
    /* output to "ls-a-lr", type "patch" */

ingress(dp="lr", inport="lr-ls-a")
----------------------------------
13. lr_in_ip_routing (northd.c:9200): ip4.dst == 10.0.2.0/24, priority 49, uuid 5c3a
    outport = "lr-ls-b";
    next;
EGRESS

egress(dp="ls-b", inport="ls-b-lr", outport="pod2.default")
-----------------------------------------------------------
 9. ls_out_port_sec_l2 (northd.c:5704): outport == "pod2.default", priority 50, uuid 1d2f
    output;
    /* output to "pod2.default", type "" */
`

// TestTraceVerdictChecksDestination: the verdict is about reaching the chosen
// destination, not about reaching some port.
func TestTraceVerdictChecksDestination(t *testing.T) {
	routerDrop := strings.Replace(crossSwitchTrace, `    outport = "lr-ls-b";
    next;
EGRESS`, "    drop;", 1)
	routerDrop = routerDrop[:strings.Index(routerDrop, "\negress(dp=\"ls-b\"")]
	delivered := strings.Replace(crossSwitchTrace, "EGRESS\n", "", 1)

	cases := []struct {
		name, raw, verdict, final string
		expect                    TraceExpect
	}{
		{"reaches the destination across a router", delivered, "delivered", "pod2.default", TraceExpect{Port: "pod2.default"}},
		{"reaches another port", delivered, "misrouted", "pod2.default", TraceExpect{Port: "vm1.ns"}},
		{"patch hop then dropped by the router", routerDrop, "dropped", "", TraceExpect{Port: "pod2.default"}},
		{"patch hop then dropped, unchecked", routerDrop, "dropped", "", TraceExpect{}},
		{"unchecked destination", delivered, "delivered", "pod2.default", TraceExpect{}},
		{"external leaves via localnet", `/* output to "localnet.external", type "localnet" */`, "delivered", "localnet.external", TraceExpect{Egress: true}},
		{"external leaves via a node ovn0", `/* output to "node-hv-01", type "" */`, "delivered", "node-hv-01", TraceExpect{Egress: true}},
		{"external reaches a pod", `/* output to "pod2.default", type "" */`, "misrouted", "pod2.default", TraceExpect{Egress: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ParseTraceOutput(c.raw, c.expect)
			if res.Verdict != c.verdict || res.FinalPort != c.final {
				t.Errorf("verdict %q final %q, want %q %q (%s)", res.Verdict, res.FinalPort, c.verdict, c.final, res.Summary)
			}
		})
	}
}

func TestBuildTcpdumpCommand(t *testing.T) {
	tests := []struct {
		name    string
		req     TcpdumpRequest
		wantErr bool
		wantCmd []string
	}{
		{
			name: "standard port capture",
			req: TcpdumpRequest{
				Interface: "test_h",
				Filter:    "tcp port 80",
				Count:     50,
			},
			wantErr: false,
			wantCmd: []string{"timeout", "5", "tcpdump", "-l", "-i", "test_h", "-nn", "-c", "50", "tcp port 80"},
		},
		{
			name: "packet count capped at max",
			req: TcpdumpRequest{
				Interface: "geneve0",
				Count:     5000,
			},
			wantErr: false,
			wantCmd: []string{"timeout", "5", "tcpdump", "-l", "-i", "geneve0", "-nn", "-c", "1000", "ip"},
		},
		{
			name: "injection in filter",
			req: TcpdumpRequest{
				Interface: "eth0",
				Filter:    "tcp; rm -rf /",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := BuildTcpdumpCommand(tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildTcpdumpCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if len(cmd) != len(tt.wantCmd) {
					t.Fatalf("expected %d args, got %d", len(tt.wantCmd), len(cmd))
				}
				for i := range cmd {
					if cmd[i] != tt.wantCmd[i] {
						t.Errorf("arg[%d] got %s, want %s", i, cmd[i], tt.wantCmd[i])
					}
				}
			}
		})
	}
}

func TestOvsCommands(t *testing.T) {
	ifaceDetailCmd, err := BuildOvsIfaceDetailCommand("test_h")
	if err != nil || len(ifaceDetailCmd) != 5 || ifaceDetailCmd[4] != "test_h" {
		t.Errorf("unexpected ifaceDetailCmd: %v, err: %v", ifaceDetailCmd, err)
	}

	sampleOvs := `name                : "test_h"
ofport              : 10
admin_state         : up
link_state          : up
mtu                 : 1450
external_ids        : {iface-id="test-port"}
statistics          : {rx_bytes=1000, rx_packets=10, tx_bytes=2000, tx_packets=20}`
	formatted := FormatOvsIfaceOutput(sampleOvs, "test_h")
	if !strings.Contains(formatted, "ofport 10") || !strings.Contains(formatted, "1450 bytes") {
		t.Errorf("expected formatted telemetry, got: %s", formatted)
	}

	streamCmd, err := BuildTcpdumpStreamCommand(TcpdumpRequest{Interface: "test_h", Filter: "tcp"})
	if err != nil || len(streamCmd) != 10 || streamCmd[1] != "60" {
		t.Errorf("unexpected streamCmd: %v, err: %v", streamCmd, err)
	}

	ifaceCmd := BuildOvsVsctlListInterfaceCommand()
	if len(ifaceCmd) != 2 || ifaceCmd[0] != "ovs-vsctl" || ifaceCmd[1] != "show" {
		t.Errorf("unexpected ifaceCmd: %v", ifaceCmd)
	}

	ctCmd := BuildConntrackDumpCommand(42)
	if len(ctCmd) != 3 || ctCmd[2] != "zone=42" {
		t.Errorf("unexpected ctCmd: %v", ctCmd)
	}

	flowsCmd, err := BuildOvsOfctlDumpFlows("br-int")
	if err != nil || len(flowsCmd) != 5 {
		t.Errorf("unexpected flowsCmd: %v, err=%v", flowsCmd, err)
	}

	filteredFlowsCmd, err := BuildOvsOfctlFilteredDumpFlows("br-int", "dl_dst=4a:73:ae:a4:54:b3")
	if err != nil || len(filteredFlowsCmd) != 6 || filteredFlowsCmd[5] != "dl_dst=4a:73:ae:a4:54:b3" {
		t.Errorf("unexpected filteredFlowsCmd: %v, err=%v", filteredFlowsCmd, err)
	}

	_, err = BuildOvsOfctlFilteredDumpFlows("br-int", "dl_dst=test; rm -rf /")
	if err == nil {
		t.Errorf("expected error on flow filter injection attempt")
	}

	_, err = BuildOvsOfctlDumpFlows("br-int; rm -rf /")
	if err == nil {
		t.Errorf("expected error on bridge injection attempt")
	}

	// Test OpenFlow formatter
	rawOF := `OFPST_FLOW reply (OF1.3) (xid=0x6):
 cookie=0xc04f547b, duration=696124.227s, table=8, n_packets=61254, n_bytes=5696576, priority=50,reg14=0x1,metadata=0x2,dl_dst=4a:73:ae:a4:54:b3 actions=load:0x4a73aea454b3->NXM_NX_XXREG0[64..111],resubmit(,9)
 cookie=0x7258a7cb, duration=696124.227s, table=36, n_packets=54953, n_bytes=5431934, priority=50,metadata=0x7,dl_dst=4a:73:ae:a4:54:b3 actions=set_field:0x1->reg15,resubmit(,40)
`
	formattedOF := FormatOpenFlowDump(rawOF, "dl_dst=4a:73:ae:a4:54:b3")
	if !strings.Contains(formattedOF, "61,254") || !strings.Contains(formattedOF, "5.4 MB") || !strings.Contains(formattedOF, "TABLE") {
		t.Errorf("unexpected formatted OpenFlow output:\n%s", formattedOF)
	}

	// Test conntrack formatter
	rawCT := "tcp,orig=(src=10.54.0.1,dst=10.54.0.2,sport=1234,dport=80),reply=(src=10.54.0.2,dst=10.54.0.1,sport=80,dport=1234),protoinfo=(state=ESTABLISHED)\n" +
		"tcp,orig=(src=127.0.0.1,dst=127.0.0.1,sport=2379,dport=3456),reply=(src=127.0.0.1,dst=127.0.0.1,sport=3456,dport=2379)\n"
	formattedCT := FormatConntrackOutput(rawCT, "10.54.0.1")
	if !strings.Contains(formattedCT, "10.54.0.1:1234 -> 10.54.0.2:80") {
		t.Errorf("expected matching flow formatted in conntrack, got: %s", formattedCT)
	}
	if strings.Contains(formattedCT, "127.0.0.1") {
		t.Errorf("expected 127.0.0.1 loopback lines to be omitted from matching output")
	}

	// Test JSON pretty formatter
	rawJSON := `{"name":"test","count":42}`
	formattedJSON := FormatJsonOutput(rawJSON)
	if !strings.Contains(formattedJSON, "  \"name\": \"test\"") {
		t.Errorf("expected 2-space indented pretty JSON, got: %s", formattedJSON)
	}
}

func TestPodLocator(t *testing.T) {
	ctx := context.Background()
	fakeClient := fake.NewSimpleClientset(
		// Follower pod (lexicographically first)
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn-central-58f79fbc94-follower",
				Namespace: "kube-system",
				Labels: map[string]string{
					"app":           "ovn-central",
					"ovn-sb-leader": "false",
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		// Leader pod (should be prioritized!)
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn-central-58f79fbc94-leader",
				Namespace: "kube-system",
				Labels: map[string]string{
					"app":           "ovn-central",
					"ovn-sb-leader": "true",
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-ovn-cni-node1",
				Namespace: "kube-system",
				Labels:    map[string]string{"app": "kube-ovn-cni"},
			},
			Spec:   corev1.PodSpec{NodeName: "node1"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	locator := NewPodLocator(fakeClient, "kube-system")

	centralPod, err := locator.FindOvnCentralPod(ctx)
	if err != nil {
		t.Fatalf("FindOvnCentralPod failed: %v", err)
	}
	if centralPod.Name != "ovn-central-58f79fbc94-leader" {
		t.Errorf("expected leader pod ovn-central-58f79fbc94-leader to be prioritized, got %s", centralPod.Name)
	}

	cniPod, err := locator.FindNodeCNIPod(ctx, "node1")
	if err != nil {
		t.Fatalf("FindNodeCNIPod failed: %v", err)
	}
	if cniPod.Name != "kube-ovn-cni-node1" {
		t.Errorf("unexpected cni pod name: %s", cniPod.Name)
	}
}

func TestMockExecutor(t *testing.T) {
	ctx := context.Background()
	mock := NewMockExecutor()
	mock.StdoutMap["ovn-trace"] = []byte("output(\"target-port\");\n")

	pod := PodRef{Namespace: "kube-system", Name: "ovn-central-0", Container: "ovn-central"}
	out, errOut, err := mock.Exec(ctx, pod, []string{"ovn-trace", "args"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errOut) > 0 {
		t.Errorf("unexpected stderr: %s", string(errOut))
	}
	if !strings.Contains(string(out), "output(\"target-port\")") {
		t.Errorf("unexpected stdout: %s", string(out))
	}

	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call recorded, got %d", len(mock.Calls))
	}
	if mock.Calls[0].Cmd[0] != "ovn-trace" {
		t.Errorf("expected cmd ovn-trace, got %s", mock.Calls[0].Cmd[0])
	}
}

func TestFindOVSInterfaceNeverGuesses(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "ovs-ovn-a", Namespace: "kube-system", Labels: map[string]string{"app": "ovs-ovn"}},
			Spec:       corev1.PodSpec{NodeName: "node-a"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "ovs-ovn-b", Namespace: "kube-system", Labels: map[string]string{"app": "ovs-ovn"}},
			Spec:       corev1.PodSpec{NodeName: "node-b"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	locator := NewPodLocator(client, "kube-system")
	exec := NewMockExecutor()
	exec.StdoutMap["ovs-vsctl"] = []byte("4f2a_h\n")

	iface, err := locator.FindOVSInterface(ctx, exec, "node-b", "app-1.default")
	if err != nil || iface != "4f2a_h" {
		t.Fatalf("expected the iface-id match, got %q %v", iface, err)
	}
	c := exec.Calls[len(exec.Calls)-1]
	if c.Pod.Name != "ovs-ovn-b" || strings.Join(c.Cmd, " ") != "ovs-vsctl --bare --columns=name find interface external_ids:iface-id=app-1.default" {
		t.Errorf("lookup must run on the port's node with the typed command, got %s %v", c.Pod.Name, c.Cmd)
	}

	exec.StdoutMap["ovs-vsctl"] = []byte("")
	if _, err := locator.FindOVSInterface(ctx, exec, "node-b", "gw-external"); !errors.Is(err, ErrIfaceNotFound) {
		t.Errorf("a port without interface must be ErrIfaceNotFound (no ovn0/br-external guess), got %v", err)
	}
	if _, err := locator.FindOVSInterface(ctx, exec, "", "app-1.default"); err == nil {
		t.Errorf("an unknown node must be an error, not the first node of the cluster")
	}
	for _, bad := range []string{"", "a;rm -rf /", "x y", "p$(id)"} {
		if _, err := BuildFindIfaceByIfaceIDCommand(bad); err == nil {
			t.Errorf("port name %q must be rejected", bad)
		}
	}
}
