package main

// ovn-topo: k9s-style TUI for the OVN topology served by ovn-topologyd.
// Navigation is xray trees (vpc / chassis / workload) plus a :ports table; the
// Tier 1 diagnostics (ADR-0001) act on whatever is selected.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/diag"
	"github.com/coulof/ovn-topology/pkg/graph"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

type model struct {
	daemonURL   string
	fixturePath string
	theme       Theme
	themeIndex  int
	styles      ThemeStyles
	width       int
	height      int
	err         error

	// Graph data and indexes (data.go), shared by the views and the diagnostics.
	graphResp       *api.GraphResponse
	baseRevision    int
	nodeMap         map[string]api.GraphNode
	nodeList        []api.GraphNode
	vpcNames        []string
	vpcGroups       map[string][]api.GraphNode
	sharedNodes     []api.GraphNode
	externalNodes   []api.GraphNode
	portList        []RouterPortConnection
	gatewayPorts    []*RouterPortConnection // gateway LRPs with no router-peer edge (from LR properties)
	routerPorts     map[string][]RouterPortConnection
	switchPorts     map[string][]RouterPortConnection
	switchWorkloads map[string][]api.GraphNode
	chassisResp     *api.GraphResponse
	chassisNodes    []api.GraphNode

	// Xray / table UI
	tree       *xtree
	mode       mode
	scr        screen
	rows       []row
	cursor     int
	offset     int
	expanded   map[string]bool
	touched    map[string]bool
	history    []histEntry
	filter     string
	filterEdit bool
	cmdEdit    bool
	cmd        string
	desc       *descState
	pcur, poff int
	psort      int
	pdesc      bool
	pinspect   bool

	// Feedback and live updates
	flash         string
	flashAt       time.Time
	flashErr      bool
	changed       map[string]time.Time
	needFlashTick bool
	live          liveState
	livePaused    bool
	pendingIDs    []string // changed IDs from SSE since the last graph load

	// Tier 1 Diagnostics (ADR-0001)
	diagMode          bool
	diagAvailable     bool
	diagReason        string
	k8sNamespace      string
	k8sClient         kubernetes.Interface
	diagExecutor      diag.Executor
	diagLocator       *diag.PodLocator
	diagCentralPod    diag.PodRef
	diagModal         bool
	diagStep          diagStep
	diagRunning       bool
	diagAction        string
	diagTitle         string
	diagTraceRes      *diag.TraceResult
	diagTraceParams   string
	traceDestinations []TraceDestination
	selectedDestIdx   int
	traceSearchFilter string
	traceCategoryIdx  int
	traceProtoIdx     int
	traceProtocol     string
	tracePort         int
	tcpdumpFilterIdx  int
	tcpdumpCustom     string
	tcpdumpPackets    []string
	diagTgt           diagTarget // captured when the modal opens (diagtarget.go)
	nicIdx            int
	chassisIdx        int
	pendingAction     diagAction
	diagStreaming     bool
	diagFollow        bool // live capture view sticks to the newest packets
	diagCancelStream  context.CancelFunc
	diagRawOutput     string
	diagRawView       bool
	diagScrollOffset  int
	diagError         error
}

type streamLineMsg struct {
	line    string
	scanner *bufio.Scanner
}

type streamDoneMsg struct {
	err error
}

type graphLoadedMsg struct {
	resp *api.GraphResponse
	err  error
}

type graphUnchangedMsg struct{}

type l2LoadedMsg struct {
	lsID string
	lsps []api.GraphNode
	err  error
}

type chassisLoadedMsg struct {
	resp *api.GraphResponse
	err  error
}

type nodeDetailMsg struct {
	id     string
	detail *api.NodeDetail
	err    error
}

type diagProbeMsg struct {
	available  bool
	reason     string
	centralPod diag.PodRef
}

type diagExecResultMsg struct {
	action      string
	title       string
	traceRes    *diag.TraceResult
	traceParams string
	rawOutput   string
	err         error
}

func initialModel(daemonURL, fixturePath, themeName string, diagMode bool, kubeconfigPath, k8sNamespace string) model {
	theme, idx := GetThemeByName(themeName)
	if k8sNamespace == "" {
		k8sNamespace = "kube-system"
	}

	m := model{
		daemonURL:        daemonURL,
		fixturePath:      fixturePath,
		mode:             modeVPC,
		themeIndex:       idx,
		theme:            theme,
		styles:           CompileStyles(theme),
		diagMode:         diagMode,
		k8sNamespace:     k8sNamespace,
		nodeMap:          make(map[string]api.GraphNode),
		routerPorts:      make(map[string][]RouterPortConnection),
		switchPorts:      make(map[string][]RouterPortConnection),
		switchWorkloads:  make(map[string][]api.GraphNode),
		expanded:         make(map[string]bool),
		touched:          make(map[string]bool),
		changed:          make(map[string]time.Time),
		pinspect:         true,
		traceProtocol:    AvailableTraceProtocols[0].Protocol,
		tracePort:        AvailableTraceProtocols[0].Port,
		traceProtoIdx:    0,
		traceCategoryIdx: 0,
	}

	if diagMode {
		var k8sConfig *rest.Config
		var err error
		if kubeconfigPath != "" {
			k8sConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		} else {
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			configOverrides := &clientcmd.ConfigOverrides{}
			kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
			k8sConfig, err = kubeConfig.ClientConfig()
		}

		if err != nil {
			m.diagAvailable = false
			m.diagReason = fmt.Sprintf("Failed to load kubeconfig: %v", err)
		} else {
			clientset, err := kubernetes.NewForConfig(k8sConfig)
			if err != nil {
				m.diagAvailable = false
				m.diagReason = fmt.Sprintf("Failed to create k8s client: %v", err)
			} else {
				m.k8sClient = clientset
				m.diagExecutor = diag.NewKubeExecutor(clientset, k8sConfig)
				m.diagLocator = diag.NewPodLocator(clientset, k8sNamespace)
				m.diagReason = "Probing RBAC..."
			}
		}
	}

	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetchGraphCmd()}
	if m.fixturePath == "" {
		cmds = append(cmds, m.connectLiveCmd())
	}
	if m.diagMode && m.diagExecutor != nil && m.diagLocator != nil {
		cmds = append(cmds, m.probeDiagCmd())
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	res, cmd := m.update(msg)
	mm := res.(model)
	if mm.needFlashTick {
		mm.needFlashTick = false
		cmd = tea.Batch(cmd, flashTick())
	}
	return mm, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.offset = window(m.cursor, m.offset, len(m.rows), m.bodyH())
		return m, nil

	case graphLoadedMsg:
		if msg.err != nil {
			if m.graphResp == nil {
				m.err = msg.err
			} else {
				m.setFlash("refresh failed: "+msg.err.Error(), true)
			}
			return m, nil
		}
		if m.tree == nil {
			m.baseRevision = msg.resp.Revision
		}
		switches, chassis := m.refetchPlan(m.pendingIDs)
		m.pendingIDs = nil
		m.applyGraph(msg.resp)
		cmds := []tea.Cmd{m.fetchSwitchesCmd(switches)}
		if chassis || m.chassisResp == nil {
			cmds = append(cmds, m.fetchChassisCmd())
		}
		return m, tea.Batch(cmds...)

	case graphUnchangedMsg:
		return m, nil

	case l2LoadedMsg:
		if msg.err != nil {
			m.setFlash("ports of "+msg.lsID+": "+msg.err.Error(), true)
			return m, nil
		}
		m.switchWorkloads[msg.lsID] = msg.lsps
		m.rebuildTree()
		return m, nil

	case chassisLoadedMsg:
		if msg.err != nil {
			m.setFlash("chassis: "+msg.err.Error(), true)
			return m, nil
		}
		m.chassisResp = msg.resp
		m.chassisNodes = msg.resp.Nodes
		m.rebuildTree()
		return m, nil

	case nodeDetailMsg:
		if m.desc != nil && m.desc.node != nil && m.desc.node.gn != nil && m.desc.node.gn.Id == msg.id {
			m.desc.loading = false
			m.desc.detail, m.desc.err = msg.detail, msg.err
		}
		return m, nil

	case liveEventMsg, liveErrMsg, pollTickMsg:
		return m.updateLive(msg)

	case flashTickMsg:
		for id, t := range m.changed {
			if time.Since(t) >= flashFor {
				delete(m.changed, id)
			}
		}
		return m, nil

	case diagProbeMsg:
		m.diagAvailable = msg.available
		m.diagReason = msg.reason
		m.diagCentralPod = msg.centralPod
		if msg.available {
			m.setFlash("diagnostics ready: "+msg.reason, false)
		} else {
			m.setFlash("diagnostics degraded: "+msg.reason, true)
		}
		return m, nil

	case streamLineMsg:
		if !m.diagStreaming {
			return m, nil
		}
		m.tcpdumpPackets = append(m.tcpdumpPackets, msg.line)
		m.diagRawOutput = strings.Join(m.tcpdumpPackets, "\n")
		return m, readNextStreamLineCmd(msg.scanner)

	case streamDoneMsg:
		m.diagStreaming = false
		m.freezeFollow()
		m.diagRunning = false
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) && !errors.Is(msg.err, io.EOF) {
			m.diagError = msg.err
		}
		return m, nil

	case tcpdumpStartedMsg:
		if msg.err != nil {
			m.diagStreaming, m.diagRunning = false, false
			m.diagError = fmt.Errorf("tcpdump not started: %w", msg.err)
			return m, nil
		}
		if !m.diagStreaming { // stopped while the interface was being resolved
			return m, nil
		}
		m.diagRunning = false // capture is live: show packets as they arrive
		m.diagTitle = msg.title
		m.tcpdumpPackets = msg.header
		m.diagRawOutput = strings.Join(m.tcpdumpPackets, "\n")
		return m, readNextStreamLineCmd(msg.scanner)

	case diagExecResultMsg:
		m.diagRunning = false
		m.diagAction = msg.action
		m.diagTitle = msg.title
		m.diagTraceRes = msg.traceRes
		m.diagTraceParams = msg.traceParams
		m.diagRawOutput = msg.rawOutput
		m.diagScrollOffset = 0
		m.diagError = msg.err
		return m, nil

	case tea.KeyMsg:
		if m.diagModal {
			return m.updateDiagModal(msg)
		}
		return m.handleKey(msg)
	}

	return m, nil
}

func runMachineOutput(daemonURL, fixturePath, viewName, focusID, format string) error {
	var resp *api.GraphResponse
	var err error

	if fixturePath != "" {
		snap := graph.LoadTestSnapshot(nil)
		resp, err = snap.BuildGraph(viewName, focusID, 1, 1, 100)
	} else {
		url := fmt.Sprintf("%s/graph?view=%s", strings.TrimRight(daemonURL, "/"), viewName)
		if focusID != "" {
			url += fmt.Sprintf("&focus=%s", focusID)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		r, reqErr := client.Get(url)
		if reqErr != nil {
			return fmt.Errorf("failed to reach daemon: %w", reqErr)
		}
		defer func() { _ = r.Body.Close() }()
		if r.StatusCode != http.StatusOK {
			return fmt.Errorf("daemon returned HTTP %d", r.StatusCode)
		}
		var decoded api.GraphResponse
		if decodeErr := json.NewDecoder(r.Body).Decode(&decoded); decodeErr != nil {
			return decodeErr
		}
		resp = &decoded
	}

	if err != nil {
		return err
	}

	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	case "yaml", "yml":
		yBytes, err := yaml.Marshal(resp)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(yBytes)
		return err
	default:
		return fmt.Errorf("unsupported output format: %s (supported: json, yaml)", format)
	}
}

func main() {
	var daemonURL string
	var useFixture bool
	var themeName string
	var diagMode bool
	var kubeconfig string
	var k8sNamespace string
	var outputFormat string
	var viewName string
	var focusID string

	flag.StringVar(&daemonURL, "daemon-url", "http://127.0.0.1:8080/api/v1", "URL of the ovn-topologyd API")
	flag.BoolVar(&useFixture, "fixture", false, "Use bundled dev fixture dataset offline")
	flag.StringVar(&themeName, "theme", getEnvDefault("THEME", "default"), "Color theme: default, suse, dracula, nord, tokyonight, catppuccin")
	flag.BoolVar(&diagMode, "diag", false, "Enable Tier 1 exec-based diagnostics (ADR-0001)")
	flag.StringVar(&kubeconfig, "kubeconfig", getEnvDefault("KUBECONFIG", ""), "Path to kubeconfig (defaults to standard kubeconfig paths)")
	flag.StringVar(&k8sNamespace, "k8s-namespace", "kube-system", "Kubernetes namespace containing Kube-OVN/OVS daemon pods")
	flag.StringVar(&outputFormat, "o", "", "Output format: json or yaml (scriptable machine output mode)")
	flag.StringVar(&viewName, "view", "l3", "View for machine output: l3, l2, chassis")
	flag.StringVar(&focusID, "focus", "", "Focus target for view (e.g. ls:<name|uuid>)")

	// Allow "ovn-topo graph ..." style invocations
	if len(os.Args) > 1 && os.Args[1] == "graph" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	flag.Parse()

	fixturePath := ""
	if useFixture {
		fixturePath = "dev-fixture"
	}

	if outputFormat != "" {
		if err := runMachineOutput(daemonURL, fixturePath, viewName, focusID, outputFormat); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(initialModel(daemonURL, fixturePath, themeName, diagMode, kubeconfig, k8sNamespace), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

func getEnvDefault(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}
