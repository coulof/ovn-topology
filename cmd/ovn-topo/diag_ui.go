package main

// Tier 1 diagnostics UI (ADR-0001): modal, trace/tcpdump/OVS actions and the
// selection helpers that turn the current TUI selection into a diagnostic target.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/diag"
	"github.com/coulof/ovn-topology/pkg/graph"
)

type diagStep int

const (
	diagStepMenu diagStep = iota
	diagStepPickDest
	diagStepPickTcpdump
	diagStepResult
	diagStepPickChassis // distributed router port: which chassis node actions run on
)

// TcpdumpPreset defines a selectable BPF filter preset for tcpdump.
type TcpdumpPreset struct {
	Name   string
	Label  string
	Filter string
}

// TraceDestination represents an interactive target candidate for ovn-trace.
type TraceDestination struct {
	Label    string
	IP       string
	Subnet   string
	Category string
	Port     string // logical port owning IP, when the destination is an OVN port
}

// TraceProtocolOption defines a selectable protocol and port for ovn-trace.
type TraceProtocolOption struct {
	Name     string
	Protocol string
	Port     int
	Label    string
}

var AvailableTraceProtocols = []TraceProtocolOption{
	{Name: "icmp", Protocol: "icmp", Port: 0, Label: "ICMP (Ping)"},
	{Name: "ssh", Protocol: "tcp", Port: 22, Label: "TCP 22 (SSH)"},
	{Name: "http", Protocol: "tcp", Port: 80, Label: "TCP 80 (HTTP)"},
	{Name: "https", Protocol: "tcp", Port: 443, Label: "TCP 443 (HTTPS)"},
	{Name: "dns", Protocol: "udp", Port: 53, Label: "UDP 53 (DNS)"},
}

var TraceCategories = []string{
	"All",
	"VM",
	"Pod",
	"Gateway",
	"Node ovn0", // the node's ovn0 port on the join switch: inside the OVN overlay
	"Underlay",  // the chassis tunnel/encap IP on the physical network: outside OVN
	"Public DNS",
}

func (m model) probeDiagCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		centralPod, err := m.diagLocator.FindOvnCentralPod(ctx)
		if err != nil {
			return diagProbeMsg{
				available: false,
				reason:    fmt.Sprintf("ovn-central pod not found: %v", err),
			}
		}

		allowed, reason, err := diag.CheckExecAccess(ctx, m.diagExecutor, centralPod)
		if err != nil {
			return diagProbeMsg{
				available: false,
				reason:    fmt.Sprintf("probe error: %v", err),
			}
		}

		return diagProbeMsg{
			available:  allowed,
			reason:     reason,
			centralPod: centralPod,
		}
	}
}

// updateDiagModal handles keys while the diagnostics modal is open.
func (m model) updateDiagModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.diagStep == diagStepPickChassis {
		chs := m.tree.roots[modeChassis]
		k := msg.String()
		switch {
		case k == "esc":
			m.diagStep, m.pendingAction = diagStepMenu, ""
		case k == "up" || k == "k":
			m.chassisIdx = max(m.chassisIdx-1, 0)
		case k == "down" || k == "j":
			m.chassisIdx = min(m.chassisIdx+1, len(chs)-1)
		case len(k) == 1 && k[0] >= '1' && k[0] <= '9' && int(k[0]-'1') < len(chs):
			m.chassisIdx = int(k[0] - '1')
			fallthrough
		case k == "enter" && len(chs) > 0:
			m.diagTgt = m.withChassis(m.diagTgt, chs[min(m.chassisIdx, len(chs)-1)])
			m.diagStep = diagStepMenu
			if a := m.pendingAction; a != "" {
				m.pendingAction = ""
				return m.runDiagAction(a)
			}
		}
		return m, nil
	}
	if len(m.diagTgt.NICs) > 0 && m.diagStep == diagStepMenu {
		nics := m.diagTgt.NICs
		k := msg.String()
		switch {
		case k == "esc" || k == "x" || k == "ctrl+c":
			m.diagModal = false
		case k == "up" || k == "k":
			m.nicIdx = max(m.nicIdx-1, 0)
		case k == "down" || k == "j":
			m.nicIdx = min(m.nicIdx+1, len(nics)-1)
		case len(k) == 1 && k[0] >= '1' && k[0] <= '9' && int(k[0]-'1') < len(nics):
			m.nicIdx = int(k[0] - '1')
			fallthrough
		case k == "enter":
			owner := m.diagTgt.Label
			if w := m.tree.byID[m.diagTgt.ID]; w != nil {
				owner = workloadLabel(w)
			}
			m.diagTgt = m.resolveDiagTarget(nics[min(m.nicIdx, len(nics)-1)])
			m.diagTgt.Owner = owner
		}
		return m, nil
	}
	if m.diagStep == diagStepPickDest {
		switch msg.String() {
		case "esc":
			if m.traceSearchFilter != "" {
				m.traceSearchFilter = ""
				m.selectedDestIdx = 0
				return m, nil
			}
			m.diagStep = diagStepMenu
			m.selectedDestIdx = 0
			return m, nil
		case "ctrl+c":
			m.diagModal = false
			m.diagStep = diagStepMenu
			return m, nil
		case "tab":
			m.traceCategoryIdx = (m.traceCategoryIdx + 1) % len(TraceCategories)
			m.selectedDestIdx = 0
			return m, nil
		case "shift+tab":
			m.traceCategoryIdx = (m.traceCategoryIdx - 1 + len(TraceCategories)) % len(TraceCategories)
			m.selectedDestIdx = 0
			return m, nil
		case "ctrl+p":
			m.traceProtoIdx = (m.traceProtoIdx + 1) % len(AvailableTraceProtocols)
			m.traceProtocol = AvailableTraceProtocols[m.traceProtoIdx].Protocol
			m.tracePort = AvailableTraceProtocols[m.traceProtoIdx].Port
			return m, nil
		case "f1":
			m.traceProtoIdx = 0
			m.traceProtocol = AvailableTraceProtocols[0].Protocol
			m.tracePort = AvailableTraceProtocols[0].Port
			return m, nil
		case "f2":
			m.traceProtoIdx = 1
			m.traceProtocol = AvailableTraceProtocols[1].Protocol
			m.tracePort = AvailableTraceProtocols[1].Port
			return m, nil
		case "f3":
			m.traceProtoIdx = 2
			m.traceProtocol = AvailableTraceProtocols[2].Protocol
			m.tracePort = AvailableTraceProtocols[2].Port
			return m, nil
		case "f4":
			m.traceProtoIdx = 3
			m.traceProtocol = AvailableTraceProtocols[3].Protocol
			m.tracePort = AvailableTraceProtocols[3].Port
			return m, nil
		case "f5":
			m.traceProtoIdx = 4
			m.traceProtocol = AvailableTraceProtocols[4].Protocol
			m.tracePort = AvailableTraceProtocols[4].Port
			return m, nil
		case "backspace":
			if len(m.traceSearchFilter) > 0 {
				m.traceSearchFilter = m.traceSearchFilter[:len(m.traceSearchFilter)-1]
				m.selectedDestIdx = 0
			}
			return m, nil
		case "ctrl+u":
			m.traceSearchFilter = ""
			m.selectedDestIdx = 0
			return m, nil
		case "up", "ctrl+k":
			if m.selectedDestIdx > 0 {
				m.selectedDestIdx--
			}
			return m, nil
		case "down", "ctrl+j":
			filtered := m.filteredTraceDestinations()
			if m.selectedDestIdx < len(filtered)-1 {
				m.selectedDestIdx++
			}
			return m, nil
		case "enter":
			filtered := m.filteredTraceDestinations()
			if len(filtered) > 0 && m.selectedDestIdx < len(filtered) {
				chosenDest := filtered[m.selectedDestIdx]
				proto := m.traceProtocol
				port := m.tracePort
				if chosenDest.Category == "Custom" && strings.Contains(m.traceSearchFilter, ":") {
					parts := strings.Split(m.traceSearchFilter, ":")
					if len(parts) == 2 {
						if pNum, err := strconv.Atoi(parts[1]); err == nil && pNum > 0 {
							proto = "tcp"
							port = pNum
						}
					}
				}
				m.diagRunning = true
				m.diagStep = diagStepResult
				m.diagScrollOffset = 0
				return m, m.executeTrace(chosenDest.IP, m.traceExpect(chosenDest), proto, port)
			}
			return m, nil
		default:
			s := msg.String()
			if len(s) == 1 {
				ch := s[0]
				if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
					ch == '.' || ch == ':' || ch == '-' || ch == '_' || ch == '/' {
					m.traceSearchFilter += s
					m.selectedDestIdx = 0
					return m, nil
				}
			}
			return m, nil
		}
	}

	if m.diagStep == diagStepPickTcpdump {
		switch msg.String() {
		case "esc":
			m.diagStep = diagStepMenu
			m.tcpdumpCustom = ""
			m.tcpdumpFilterIdx = 0
			return m, nil
		case "ctrl+c":
			m.diagModal = false
			m.diagStep = diagStepMenu
			return m, nil
		case "up", "ctrl+k":
			if m.tcpdumpFilterIdx > 0 {
				m.tcpdumpFilterIdx--
			}
			return m, nil
		case "down", "ctrl+j":
			presets := m.getTcpdumpPresets()
			if m.tcpdumpFilterIdx < len(presets) {
				m.tcpdumpFilterIdx++
			}
			return m, nil
		case "1", "2", "3", "4", "5", "6":
			idx := int(msg.String()[0] - '1')
			presets := m.getTcpdumpPresets()
			if idx < len(presets) {
				m.tcpdumpFilterIdx = idx
				return m, m.executeTcpdumpStream(presets[idx].Filter, m.diagTgt)
			}
			return m, nil
		case "backspace":
			if len(m.tcpdumpCustom) > 0 {
				m.tcpdumpCustom = m.tcpdumpCustom[:len(m.tcpdumpCustom)-1]
			}
			return m, nil
		case "enter":
			presets := m.getTcpdumpPresets()
			filter := ""
			if m.tcpdumpFilterIdx < len(presets) && m.tcpdumpCustom == "" {
				filter = presets[m.tcpdumpFilterIdx].Filter
			} else if m.tcpdumpCustom != "" {
				filter = m.tcpdumpCustom
			} else {
				filter = presets[0].Filter
			}
			return m, m.executeTcpdumpStream(filter, m.diagTgt)
		default:
			s := msg.String()
			if len(s) == 1 {
				ch := s[0]
				if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
					ch == '.' || ch == ' ' || ch == '-' || ch == '_' || ch == '=' || ch == '(' || ch == ')' {
					m.tcpdumpCustom += s
					m.tcpdumpFilterIdx = len(m.getTcpdumpPresets())
					return m, nil
				}
			}
			return m, nil
		}
	}

	if m.diagStep == diagStepResult {
		switch msg.String() {
		case " ", "esc":
			if m.diagStreaming {
				if m.diagCancelStream != nil {
					m.diagCancelStream()
					m.diagCancelStream = nil
				}
				m.diagStreaming = false
				m.freezeFollow()
				m.diagRunning = false
				return m, nil
			}
			switch m.diagAction {
			case "trace":
				m.diagStep = diagStepPickDest
			case "tcpdump":
				m.diagStep = diagStepPickTcpdump
			default:
				m.diagStep = diagStepMenu
			}
			m.diagRawOutput = ""
			m.diagTraceRes = nil
			return m, nil
		case "x", "ctrl+c":
			if m.diagStreaming {
				if m.diagCancelStream != nil {
					m.diagCancelStream()
					m.diagCancelStream = nil
				}
				m.diagStreaming = false
				m.freezeFollow()
				m.diagRunning = false
			}
			m.diagModal = false
			m.diagStep = diagStepMenu
			return m, nil
		case "o":
			m.diagRawView = !m.diagRawView
			return m, nil
		case "down", "j", "pgdown":
			if m.diagFollow {
				return m, nil
			}
			step := 1
			if msg.String() == "pgdown" {
				step = 8
			}
			m.diagScrollOffset += step
			if mx := m.diagMaxOffset(); m.diagStreaming && m.diagScrollOffset >= mx {
				m.diagScrollOffset = mx
				m.diagFollow = true // back at the bottom: follow again
			}
			return m, nil
		case "up", "k", "pgup":
			if m.diagFollow { // leave follow mode from the bottom, not from the top
				m.diagScrollOffset = m.diagMaxOffset()
				m.diagFollow = false
			}
			step := 1
			if msg.String() == "pgup" {
				step = 8
			}
			m.diagScrollOffset = max(0, m.diagScrollOffset-step)
			return m, nil
		case "G", "end":
			m.diagScrollOffset = m.diagMaxOffset()
			m.diagFollow = m.diagStreaming
			return m, nil
		default:
			return m, nil
		}
	}

	// Main Diagnostics Menu (diagStepMenu)
	switch msg.String() {
	case "esc", "x", "ctrl+c":
		m.diagModal = false
		m.diagStep = diagStepMenu
		m.diagRawOutput = ""
		m.diagTraceRes = nil
		m.diagError = nil
		m.diagRunning = false
		m.diagScrollOffset = 0
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		return m.runDiagAction(diagActions[msg.String()[0]-'1'])
	case "c":
		if m.diagTgt.ChooseChassis {
			m.diagStep, m.pendingAction, m.chassisIdx = diagStepPickChassis, "", 0
		}
		return m, nil
	default:
		return m, nil
	}
}

// runDiagAction starts an action on the captured target, or explains why it cannot run.
func (m model) runDiagAction(a diagAction) (tea.Model, tea.Cmd) {
	if m.diagTgt.needsChassis(a) {
		m.diagStep, m.pendingAction, m.chassisIdx = diagStepPickChassis, a, 0
		m.diagError = nil
		return m, nil
	}
	if ok, why := m.diagTgt.can(a); !ok {
		m.diagError = fmt.Errorf("%s: %s", a, why)
		return m, nil
	}
	if m.diagRunning {
		return m, nil
	}
	m.diagError = nil
	switch a {
	case actTrace:
		m.diagStep = diagStepPickDest
		m.selectedDestIdx = 0
		m.traceSearchFilter = ""
		m.traceCategoryIdx = 0
		m.traceProtoIdx = 0
		m.traceProtocol = AvailableTraceProtocols[0].Protocol
		m.tracePort = AvailableTraceProtocols[0].Port
		m.traceDestinations = m.collectTraceDestinations(nil, m.diagTgt.SrcIP)
		return m, nil
	case actTcpdump:
		m.diagStep = diagStepPickTcpdump
		m.tcpdumpFilterIdx = 0
		m.tcpdumpCustom = ""
		return m, nil
	}
	m.diagRunning = true
	m.diagStep = diagStepResult
	m.diagScrollOffset = 0
	return m, m.executeNodeDiag(a, m.diagTgt)
}

func extractCleanIP(raw string) string {
	parts := strings.Fields(raw)
	for _, part := range parts {
		if part == "router" || part == "unknown" || part == "dynamic" {
			continue
		}
		if parsed := net.ParseIP(part); parsed != nil {
			return parsed.String()
		}
		if strings.Contains(part, "/") {
			ipPart := strings.Split(part, "/")[0]
			if parsed := net.ParseIP(ipPart); parsed != nil {
				return parsed.String()
			}
		}
	}
	return ""
}

func extractCleanMAC(raw string) string {
	parts := strings.Fields(raw)
	for _, part := range parts {
		if _, err := net.ParseMAC(part); err == nil {
			return part
		}
	}
	return ""
}

func (m model) resolveNodeName(candidate string) string {
	if candidate == "" {
		return ""
	}
	// Check if candidate matches any chassis hostname or nodeName in chassisNodes
	for _, ch := range m.chassisNodes {
		if ch.Id == candidate || ch.Name == candidate || strings.TrimPrefix(ch.Id, graph.PrefixChassis) == candidate {
			if ch.Properties != nil {
				p := *ch.Properties
				if nodeName, ok := p["nodeName"].(string); ok && nodeName != "" {
					return nodeName
				}
				if host, ok := p["hostname"].(string); ok && host != "" {
					return host
				}
			}
			return ch.Name
		}
		if ch.Properties != nil {
			p := *ch.Properties
			if u, ok := p["uuid"].(string); ok && u == candidate {
				if nodeName, ok := p["nodeName"].(string); ok && nodeName != "" {
					return nodeName
				}
				return ch.Name
			}
		}
	}
	// Check if candidate matches nodeName in nodeList
	for _, n := range m.nodeList {
		if n.Type == api.GraphNodeTypeChassis {
			if n.Id == candidate || strings.TrimPrefix(n.Id, graph.PrefixChassis) == candidate {
				return n.Name
			}
		}
	}
	// Check in nodeMap
	if n, ok := m.nodeMap[graph.PrefixChassis+candidate]; ok {
		return n.Name
	}
	if n, ok := m.nodeMap[candidate]; ok && n.Type == api.GraphNodeTypeChassis {
		return n.Name
	}
	return candidate
}

func (m model) collectTraceDestinations(sourceNode *api.GraphNode, sourceIP string) []TraceDestination {
	var dests []TraceDestination
	seenIPs := make(map[string]bool)
	if sourceIP != "" {
		seenIPs[sourceIP] = true
	}

	// 1. Workload Destinations (VMs and Pods across all subnets)
	for lsID, workloads := range m.switchWorkloads {
		swName := lsID
		if node, ok := m.nodeMap[lsID]; ok {
			swName = node.Name
		}
		for _, w := range workloads {
			if w.Properties != nil {
				p := *w.Properties
				portType, _ := p["type"].(string)
				if portType == "router" {
					continue
				}
				if addrs, ok := p["addresses"].([]interface{}); ok {
					for _, a := range addrs {
						if s, ok := a.(string); ok {
							ip := extractCleanIP(s)
							if ip != "" && !seenIPs[ip] {
								seenIPs[ip] = true
								kind := "Pod"
								category := "Pod"
								name := w.Name
								if w.K8s != nil {
									if w.K8s.WorkloadKind != nil && *w.K8s.WorkloadKind != "" {
										kind = *w.K8s.WorkloadKind
										if strings.EqualFold(kind, "VirtualMachine") || strings.EqualFold(kind, "VM") {
											category = "VM"
										}
									}
									if w.K8s.WorkloadName != nil && *w.K8s.WorkloadName != "" {
										name = *w.K8s.WorkloadName
									}
								}
								if strings.HasPrefix(w.Name, "node-") || swName == "join" {
									dests = append(dests, TraceDestination{
										Label:    fmt.Sprintf("[Node ovn0] %s (IP: %s on join — OVN overlay)", name, ip),
										IP:       ip,
										Subnet:   swName,
										Category: "Node ovn0",
										Port:     w.Name,
									})
									continue
								}
								dests = append(dests, TraceDestination{
									Label:    fmt.Sprintf("[%s] %s (IP: %s on %s)", kind, name, ip, swName),
									IP:       ip,
									Subnet:   swName,
									Category: category,
									Port:     w.Name,
								})
							}
						}
					}
				}
			}
		}
	}

	// 2. Gateway Destinations (Logical Router Ports)
	for _, conn := range m.portList {
		for _, netCIDR := range conn.Networks {
			ip := extractCleanIP(netCIDR)
			if ip != "" && !seenIPs[ip] {
				seenIPs[ip] = true
				category := "Subnet Gateway"
				role := conn.RouterName
				if conn.ActiveGateway != "" {
					category = "External Gateway"
					role = fmt.Sprintf("%s (Active: %s)", conn.RouterName, conn.ActiveGateway)
				}
				dests = append(dests, TraceDestination{
					Label:    fmt.Sprintf("%s: %s (IP: %s on %s)", category, role, ip, conn.SwitchName),
					IP:       ip,
					Subnet:   conn.SwitchName,
					Category: "Gateway",
				})
			}
		}
	}

	// 3. Cluster Nodes / Physical Hosts
	for _, ch := range m.chassisNodes {
		nodeName := ch.Name
		if ch.Properties != nil {
			p := *ch.Properties
			if n, ok := p["nodeName"].(string); ok && n != "" {
				nodeName = n
			} else if h, ok := p["hostname"].(string); ok && h != "" {
				nodeName = h
			}
			if encaps, ok := p["encaps"].([]interface{}); ok {
				for _, enc := range encaps {
					if encMap, ok := enc.(map[string]interface{}); ok {
						if encIP, ok := encMap["ip"].(string); ok && encIP != "" && !seenIPs[encIP] {
							seenIPs[encIP] = true
							dests = append(dests, TraceDestination{
								Label:    fmt.Sprintf("[Underlay] %s (tunnel/encap IP: %s — outside OVN)", nodeName, encIP),
								IP:       encIP,
								Subnet:   "physical",
								Category: "Underlay",
							})
						}
					}
				}
			} else if encMaps, ok := p["encaps"].([]map[string]interface{}); ok {
				for _, encMap := range encMaps {
					if encIP, ok := encMap["ip"].(string); ok && encIP != "" && !seenIPs[encIP] {
						seenIPs[encIP] = true
						dests = append(dests, TraceDestination{
							Label:    fmt.Sprintf("[Underlay] %s (tunnel/encap IP: %s — outside OVN)", nodeName, encIP),
							IP:       encIP,
							Subnet:   "physical",
							Category: "Underlay",
						})
					}
				}
			}
		}
	}

	// 4. Well-known Public Internet & DNS
	dests = append(dests,
		TraceDestination{
			Label:    "Cloudflare Public DNS: 1.1.1.1",
			IP:       "1.1.1.1",
			Subnet:   "external",
			Category: "Public DNS",
		},
		TraceDestination{
			Label:    "Google Public DNS: 8.8.8.8",
			IP:       "8.8.8.8",
			Subnet:   "external",
			Category: "Public DNS",
		},
		TraceDestination{
			Label:    "Quad9 Public DNS: 9.9.9.9",
			IP:       "9.9.9.9",
			Subnet:   "external",
			Category: "Public DNS",
		},
	)

	return dests
}

func (m model) filteredTraceDestinations() []TraceDestination {
	var list []TraceDestination
	cleanTyped := strings.TrimSpace(m.traceSearchFilter)
	query := strings.ToLower(cleanTyped)

	catFilter := "All"
	if m.traceCategoryIdx >= 0 && m.traceCategoryIdx < len(TraceCategories) {
		catFilter = TraceCategories[m.traceCategoryIdx]
	}

	// 1. If user typed an IP address, offer it as Custom Destination at the top
	typedHost := cleanTyped
	if strings.Contains(cleanTyped, ":") {
		typedHost = strings.Split(cleanTyped, ":")[0]
	}
	if parsed := net.ParseIP(typedHost); parsed != nil {
		list = append(list, TraceDestination{
			Label:    fmt.Sprintf("⚡ Custom IP Target: %s (press Enter to trace)", cleanTyped),
			IP:       parsed.String(),
			Subnet:   "custom",
			Category: "Custom",
		})
	}

	for _, d := range m.traceDestinations {
		if catFilter != "All" && d.Category != catFilter {
			continue
		}
		if query != "" {
			labelMatch := strings.Contains(strings.ToLower(d.Label), query)
			ipMatch := strings.Contains(d.IP, query)
			subMatch := strings.Contains(strings.ToLower(d.Subnet), query)
			catMatch := strings.Contains(strings.ToLower(d.Category), query)
			if !labelMatch && !ipMatch && !subMatch && !catMatch {
				continue
			}
		}
		list = append(list, d)
	}

	return list
}

// traceExpect is where a trace to dest must end for the verdict to be "delivered".
// A custom IP that belongs to a known destination takes its expectation. Router
// port IPs and unknown custom IPs are not checked.
func (m model) traceExpect(dest TraceDestination) diag.TraceExpect {
	if dest.Category == "Custom" {
		for _, d := range m.traceDestinations {
			if d.IP == dest.IP {
				return m.traceExpect(d)
			}
		}
		return diag.TraceExpect{}
	}
	switch {
	case dest.Port != "":
		return diag.TraceExpect{Port: dest.Port}
	case dest.Category == "Underlay" || dest.Category == "Public DNS":
		return diag.TraceExpect{Egress: true}
	}
	return diag.TraceExpect{}
}

func (m model) executeTrace(targetDstIP string, expect diag.TraceExpect, proto string, dstPort int) tea.Cmd {
	t := m.diagTgt
	req, reqErr := m.traceRequest(t, targetDstIP, proto, dstPort)
	return func() tea.Msg {
		if reqErr != nil {
			return diagExecResultMsg{action: "trace", err: reqErr}
		}
		if m.diagExecutor == nil || m.diagLocator == nil {
			return diagExecResultMsg{err: fmt.Errorf("diagnostics executor not initialized")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		centralPod, err := m.diagLocator.FindOvnCentralPod(ctx)
		if err != nil {
			return diagExecResultMsg{err: fmt.Errorf("failed to locate ovn-central leader pod: %w", err)}
		}

		cmd, err := diag.BuildTraceCommand(req)
		if err != nil {
			return diagExecResultMsg{err: err}
		}

		stdout, stderr, err := m.diagExecutor.Exec(ctx, centralPod, cmd)
		if err != nil {
			errMsg := string(stderr)
			if errMsg == "" {
				errMsg = err.Error()
			}
			return diagExecResultMsg{err: fmt.Errorf("ovn-trace failed: %s", errMsg)}
		}

		traceRes := diag.ParseTraceOutput(string(stdout), expect)
		traceParams := fmt.Sprintf("Source (From):      [Datapath %s] inport '%s' (IP: %s, MAC: %s)\nDestination (To):   IP: %s (MAC: %s)\nProtocol Match:     %s (ip.ttl=255, 4x --ct=new)\nExecuted in:        pod %s",
			req.Datapath, req.Inport, req.SrcIP, req.SrcMAC, req.DstIP, req.DstMAC, strings.ToUpper(req.Protocol), centralPod)

		return diagExecResultMsg{
			action:      "trace",
			title:       fmt.Sprintf("ovn-trace: %s -> %s via %s", req.SrcIP, req.DstIP, req.Inport),
			traceRes:    &traceRes,
			traceParams: traceParams,
			rawOutput:   string(stdout),
		}
	}
}

// executeNodeDiag runs a node-scoped action on the target's node. The node always
// comes from the target (Port_Binding chassis, gateway chassis or the selected
// chassis); there is no "first node of the cluster" fallback.
func (m model) executeNodeDiag(action diagAction, t diagTarget) tea.Cmd {
	return func() tea.Msg {
		if m.diagExecutor == nil || m.diagLocator == nil {
			return diagExecResultMsg{err: fmt.Errorf("diagnostics executor not initialized")}
		}
		if t.Node == "" {
			return diagExecResultMsg{action: string(action), err: fmt.Errorf("%s: target has no node", action)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pod, err := m.diagLocator.FindNodeOVSPod(ctx, t.Node)
		if err != nil {
			return diagExecResultMsg{err: fmt.Errorf("failed to locate OVS pod on node %s: %w", t.Node, err)}
		}

		var cmd []string
		var title, iface string

		switch action {
		case actConntrack:
			cmd = diag.BuildConntrackDumpCommand(0)
			title = fmt.Sprintf("Conntrack on node %s (filter: %s)", t.Node, firstNonEmpty(t.SrcIP, "none"))
		case actOvsPort:
			iface, err = m.diagLocator.FindOVSInterface(ctx, m.diagExecutor, t.Node, t.Port)
			if err != nil {
				return diagExecResultMsg{action: string(action), err: err}
			}
			cmd, err = diag.BuildOvsIfaceDetailCommand(iface)
			if err != nil {
				return diagExecResultMsg{err: err}
			}
			title = fmt.Sprintf("OVS interface %s (iface-id %s on %s)", iface, t.Port, t.Node)
		case actVsctl:
			cmd = diag.BuildOvsVsctlShowCommand()
			title = fmt.Sprintf("OVS Bridges & Interfaces (ovs-vsctl show) on node %s", t.Node)
		case actLogs:
			cmd = diag.BuildOvnControllerLogTail(50)
			title = fmt.Sprintf("ovn-controller logs on node %s", t.Node)
		default:
			return diagExecResultMsg{err: fmt.Errorf("unknown diagnostic action: %s", action)}
		}
		title += "  [pod " + pod.String() + "]"

		stdout, stderr, err := m.diagExecutor.Exec(ctx, pod, cmd)
		if err != nil {
			errMsg := string(stderr)
			if errMsg == "" {
				errMsg = err.Error()
			}
			return diagExecResultMsg{err: fmt.Errorf("%s failed: %s", action, errMsg)}
		}

		out := string(stdout)
		switch action {
		case actConntrack:
			out = diag.FormatConntrackOutput(out, t.SrcIP)
		case actOvsPort:
			out = diag.FormatOvsIfaceOutput(out, iface)
		}

		return diagExecResultMsg{
			action:    string(action),
			title:     title,
			rawOutput: out,
		}
	}
}

// tcpdumpStartedMsg reports the resolved capture interface (looked up off the UI
// goroutine) and the scanner of the running capture.
type tcpdumpStartedMsg struct {
	title   string
	header  []string
	scanner *bufio.Scanner
	err     error
}

// executeTcpdumpStream captures on the target's own host interface: the OVS
// interface whose iface-id is the port, on the port's node. If it cannot be
// found the capture does not start (no guessed ovn0 / br-int / first node).
func (m *model) executeTcpdumpStream(filter string, t diagTarget) tea.Cmd {
	if ok, why := t.can(actTcpdump); !ok {
		m.diagError = fmt.Errorf("tcpdump: %s", why)
		return nil
	}
	m.diagRunning = true
	m.diagStreaming = true
	m.diagFollow = true
	m.diagStep = diagStepResult
	m.diagAction = "tcpdump"
	m.diagScrollOffset = 0
	m.diagError = nil
	capture := firstNonEmpty(t.CapturePort, t.Port)
	m.diagTitle = fmt.Sprintf("Live tcpdump: resolving the interface of %s on %s…", capture, t.Node)
	m.tcpdumpPackets = nil
	m.diagRawOutput = ""

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	m.diagCancelStream = cancel
	exec, loc := m.diagExecutor, m.diagLocator

	return func() tea.Msg {
		iface, how := t.Iface, t.IfaceNote
		if iface == "" {
			found, err := loc.FindOVSInterface(ctx, exec, t.Node, capture)
			if err != nil {
				return tcpdumpStartedMsg{err: err}
			}
			iface, how = found, firstNonEmpty(t.IfaceNote, "OVS iface-id "+capture)
		}
		effectiveFilter := filter
		if t.Iface != "" && !strings.Contains(filter, "host") && !strings.Contains(filter, "net") {
			var targets []string
			if t.SrcIP != "" {
				targets = append(targets, "host "+t.SrcIP)
			}
			for _, sub := range t.Subnets {
				targets = append(targets, "net "+sub.String())
			}
			if len(targets) > 0 {
				effectiveFilter = fmt.Sprintf("(%s) and (%s)", filter, strings.Join(targets, " or "))
			}
		} else if t.CapturePort != "" && t.SrcIP != "" && !strings.Contains(filter, "host") {
			effectiveFilter = fmt.Sprintf("(%s) and host %s", filter, t.SrcIP) // shared bridge/port: keep it to this port
		}
		pod, err := loc.FindNodeCNIPod(ctx, t.Node)
		if err != nil {
			return tcpdumpStartedMsg{err: err}
		}
		cmd, err := diag.BuildTcpdumpStreamCommand(diag.TcpdumpRequest{Interface: iface, Filter: effectiveFilter, Count: 500})
		if err != nil {
			return tcpdumpStartedMsg{err: err}
		}
		pr, pw := io.Pipe()
		go func() {
			_ = exec.Stream(ctx, pod, cmd, nil, pw, pw)
			_ = pw.Close()
		}()
		return tcpdumpStartedMsg{
			title: fmt.Sprintf("Live tcpdump on %s (node: %s, filter: %s)", iface, t.Node, effectiveFilter),
			header: []string{
				fmt.Sprintf("Listening on interface '%s' (%s) — port %s on node %s, pod %s", iface, how, t.Port, t.Node, pod),
				fmt.Sprintf("Filter: %s", effectiveFilter),
				"",
			},
			scanner: bufio.NewScanner(pr),
		}
	}
}

// freezeFollow keeps a stopped capture scrolled to its last packets.
func (m *model) freezeFollow() {
	if m.diagFollow {
		m.diagScrollOffset = m.diagMaxOffset()
		m.diagFollow = false
	}
}

// diagModalHead is the modal title and the target block shown on every step.
func (m model) diagModalHead() string {
	var box strings.Builder
	box.WriteString(m.styles.Header.Render("⚡ [TIER 1 DIAGNOSTICS - ADR-0001]") + "\n")
	box.WriteString(m.styles.MutedText.Render("Executed inside privileged cluster pods using operator credentials") + "\n\n")

	// Target: captured when the modal opened; every action below acts on exactly this.
	t := m.diagTgt
	for i, l := range t.describe() {
		label := "          "
		if i == 0 {
			label = "  Target: "
		}
		st := m.styles.MutedText
		if i == 0 {
			st = m.styles.PortBadge
		}
		box.WriteString(label + st.Render(l) + "\n")
	}
	for _, w := range t.Warnings {
		box.WriteString("  " + m.styles.ActiveGateway.Render("⚠ "+w) + "\n")
	}
	if t.Stale != "" {
		box.WriteString("  " + m.styles.BadgeUnbound.Render("✗ "+t.Stale) + "\n")
	}
	box.WriteString("\n")
	return box.String()
}

// diagResultTop is the result title and capture badge above the output.
func (m model) diagResultTop() string {
	var box strings.Builder
	fmt.Fprintf(&box, "  %s\n\n", m.styles.Header.Render("Result: "+m.diagTitle))

	if m.diagStreaming {
		packetCount := 0
		if len(m.tcpdumpPackets) > 3 {
			packetCount = len(m.tcpdumpPackets) - 3
		}
		follow := "following"
		if !m.diagFollow {
			follow = "paused — G: follow"
		}
		streamBadge := fmt.Sprintf("🔴 LIVE CAPTURE ACTIVE — %d packets received, %s (Press Space or Esc to stop)", packetCount, follow)
		box.WriteString("  " + m.styles.ActiveGateway.Render(streamBadge) + "\n\n")
	} else if m.diagAction == "tcpdump" {
		packetCount := 0
		if len(m.tcpdumpPackets) > 3 {
			packetCount = len(m.tcpdumpPackets) - 3
		}
		stopBadge := fmt.Sprintf("⏹ CAPTURE STOPPED — %d packets captured (Press Space or Esc: back | o: toggle raw)", packetCount)
		box.WriteString("  " + m.styles.BadgeBound.Render(stopBadge) + "\n\n")
	}
	return box.String()
}

// diagVisibleLines is the height of the result viewport: what is left of the
// modal area once its head, top, footer hint, border and padding are drawn.
func (m model) diagVisibleLines() int {
	used := strings.Count(m.diagModalHead()+m.diagResultTop(), "\n")
	return max(3, m.areaH()-6-used)
}

// diagMaxOffset is the scroll offset that shows the last lines of the raw output.
func (m model) diagMaxOffset() int {
	return max(0, len(strings.Split(strings.TrimSpace(m.diagRawOutput), "\n"))-m.diagVisibleLines())
}

func readNextStreamLineCmd(scanner *bufio.Scanner) tea.Cmd {
	return func() tea.Msg {
		if scanner.Scan() {
			return streamLineMsg{line: scanner.Text(), scanner: scanner}
		}
		return streamDoneMsg{err: scanner.Err()}
	}
}

func (m model) getTcpdumpPresets() []TcpdumpPreset {
	targetIP := m.diagTgt.SrcIP
	ipFilter := "ip"
	if targetIP != "" && net.ParseIP(targetIP) != nil {
		ipFilter = fmt.Sprintf("host %s", targetIP)
	}

	return []TcpdumpPreset{
		{Name: "ip", Label: fmt.Sprintf("Port IP Traffic (%s)", ipFilter), Filter: ipFilter},
		{Name: "icmp", Label: "ICMP (Ping / Echo requests & replies)", Filter: "icmp or icmp6"},
		{Name: "tcp", Label: "TCP (All TCP connections)", Filter: "tcp"},
		{Name: "ssh", Label: "TCP 22 (SSH)", Filter: "tcp port 22"},
		{Name: "web", Label: "Web Traffic (HTTP 80 / HTTPS 443)", Filter: "tcp port 80 or tcp port 443"},
		{Name: "dns", Label: "DNS Traffic (UDP 53)", Filter: "udp port 53"},
	}
}

func (m model) renderDiagModal() string {
	var box strings.Builder

	box.WriteString(m.diagModalHead())
	t := m.diagTgt

	if m.diagStep == diagStepPickChassis {
		box.WriteString(m.styles.Header.Render("  Distributed router port: it runs on every chassis — on which one?") + "\n")
		for i, ch := range m.tree.roots[modeChassis] {
			line := fmt.Sprintf("[%d] %s  %s", i+1, ch.name, ch.summary)
			if i == m.chassisIdx {
				box.WriteString("  " + m.styles.SelectedNode.Render("► "+line) + "\n")
			} else {
				box.WriteString("    " + m.styles.Node.Render(line) + "\n")
			}
		}
		box.WriteString("\n" + m.styles.MutedText.Render("  ↑/↓ + Enter or 1-9: choose chassis | Esc: back"))
		return m.styles.ModalBox.Render(box.String())
	}

	if len(t.NICs) > 0 && m.diagStep == diagStepMenu {
		box.WriteString(m.styles.Header.Render("  This workload has several NICs — which one?") + "\n")
		for i, nic := range t.NICs {
			ip, _ := lspAddress(nic.gn)
			where := ""
			for _, r := range nic.refs {
				if x := m.tree.byID[r.to]; x != nil && (r.label == "subnet" || r.label == "bound on") {
					where += map[string]string{"subnet": " on ", "bound on": " @"}[r.label] + x.name
				}
			}
			line := fmt.Sprintf("[%d] %s  %s%s", i+1, nic.name, ip, where)
			if i == m.nicIdx {
				box.WriteString("  " + m.styles.SelectedNode.Render("► "+line) + "\n")
			} else {
				box.WriteString("    " + m.styles.Node.Render(line) + "\n")
			}
		}
		box.WriteString("\n" + m.styles.MutedText.Render("  ↑/↓ + Enter or 1-9: choose NIC | Esc: cancel"))
		return m.styles.ModalBox.Render(box.String())
	}

	if m.diagRunning {
		box.WriteString(m.styles.ActiveGateway.Render("  ⏳ Executing diagnostic command inside pod...") + "\n")
		if m.diagStreaming {
			box.WriteString(m.styles.MutedText.Render("  "+m.diagTitle) + "\n\n")
		} else {
			box.WriteString(m.styles.MutedText.Render("  Please wait while the command completes...") + "\n\n")
		}
		return m.styles.ModalBox.Render(box.String())
	}

	if m.diagError != nil {
		box.WriteString(m.styles.BadgeUnbound.Render(fmt.Sprintf("  ❌ Diagnostic Error: %v", m.diagError)) + "\n\n")
	}

	if m.diagStep == diagStepPickDest {
		box.WriteString(m.styles.Header.Render("Choose Destination Target:") + "\n\n")

		// 1. Search / Custom IP Input Bar
		searchPrompt := "  Search / Custom IP: "
		cursorStr := "█"
		inputDisplay := m.traceSearchFilter
		if inputDisplay == "" {
			inputDisplay = m.styles.MutedText.Render("(type any IP address, node name, or keyword...)") + cursorStr
		} else {
			inputDisplay = m.styles.SelectedNode.Render(inputDisplay) + cursorStr
		}
		box.WriteString(m.styles.Header.Render(searchPrompt) + inputDisplay + "\n\n")

		// 2. Category Badges Bar
		counts := make(map[string]int)
		counts["All"] = len(m.traceDestinations)
		for _, d := range m.traceDestinations {
			counts[d.Category]++
		}

		var catBadges []string
		for i, cat := range TraceCategories {
			count := counts[cat]
			label := fmt.Sprintf("[%s (%d)]", cat, count)
			if i == m.traceCategoryIdx {
				catBadges = append(catBadges, m.styles.TabActive.Render(label))
			} else {
				catBadges = append(catBadges, m.styles.TabInactive.Render(label))
			}
		}
		box.WriteString("  Category: " + strings.Join(catBadges, " ") + "\n\n")

		// 3. Filtered Destinations List
		filtered := m.filteredTraceDestinations()
		if len(filtered) == 0 {
			box.WriteString(m.styles.MutedText.Render("    (no destinations matching filter; type an IP to trace directly)") + "\n\n")
		} else {
			if m.selectedDestIdx >= len(filtered) {
				m.selectedDestIdx = len(filtered) - 1
			}
			if m.selectedDestIdx < 0 {
				m.selectedDestIdx = 0
			}

			visibleLimit := 10
			startIdx := 0
			if m.selectedDestIdx >= visibleLimit {
				startIdx = m.selectedDestIdx - visibleLimit + 1
			}
			endIdx := startIdx + visibleLimit
			if endIdx > len(filtered) {
				endIdx = len(filtered)
			}

			currentCat := ""
			for i := startIdx; i < endIdx; i++ {
				d := filtered[i]
				if d.Category != currentCat && (TraceCategories[m.traceCategoryIdx] == "All" || d.Category == "Custom") {
					currentCat = d.Category
					box.WriteString(m.styles.MutedText.Render(fmt.Sprintf("  ── %s ──", currentCat)) + "\n")
				}

				isSel := i == m.selectedDestIdx
				cursor := "  "
				if isSel {
					cursor = "► "
				}
				line := fmt.Sprintf("%s[%d] %s", cursor, i+1, d.Label)
				if isSel {
					box.WriteString("  " + m.styles.SelectedNode.Render(line) + "\n")
				} else {
					box.WriteString("  " + m.styles.Node.Render(line) + "\n")
				}
			}
			if len(filtered) > visibleLimit {
				box.WriteString(m.styles.MutedText.Render(fmt.Sprintf("    ... (showing %d-%d of %d matches)", startIdx+1, endIdx, len(filtered))) + "\n")
			}
		}

		// 4. Protocol Selector Bar
		var protoBadges []string
		for i, opt := range AvailableTraceProtocols {
			label := fmt.Sprintf("[%d] %s", i+1, opt.Label)
			if i == m.traceProtoIdx {
				protoBadges = append(protoBadges, m.styles.ActiveGateway.Render("► "+label))
			} else {
				protoBadges = append(protoBadges, m.styles.MutedText.Render(label))
			}
		}

		box.WriteString("\n  Protocol: " + strings.Join(protoBadges, " | ") + "\n\n")
		box.WriteString(m.styles.MutedText.Render("  [Type: filter / enter IP | Tab: category | Ctrl+P or F1-F5: proto | Enter: trace | Esc: back]"))

		return m.styles.ModalBox.Render(box.String())
	}

	if m.diagStep == diagStepPickTcpdump {
		box.WriteString(m.styles.Header.Render("Live tcpdump: Select Capture Filter") + "\n\n")

		iface := firstNonEmpty(t.Iface, t.IfaceNote, "OVS interface with iface-id "+t.Port)
		fmt.Fprintf(&box, "  Capture on: %s on node %s\n\n",
			m.styles.PortBadge.Render(iface), m.styles.SelectedNode.Render(t.Node))

		box.WriteString(m.styles.Header.Render("  Choose a Protocol Preset or Filter:") + "\n")
		presets := m.getTcpdumpPresets()
		for i, p := range presets {
			isSel := i == m.tcpdumpFilterIdx && m.tcpdumpCustom == ""
			cursor := "  "
			if isSel {
				cursor = "► "
			}
			line := fmt.Sprintf("%s[%d] %s", cursor, i+1, p.Label)
			if isSel {
				box.WriteString("  " + m.styles.SelectedNode.Render(line) + "\n")
			} else {
				box.WriteString("  " + m.styles.Node.Render(line) + "\n")
			}
		}

		// Custom filter option
		isCustomSel := m.tcpdumpFilterIdx == len(presets) || m.tcpdumpCustom != ""
		cursor := "  "
		if isCustomSel {
			cursor = "► "
		}
		customDisplay := m.tcpdumpCustom
		if customDisplay == "" {
			customDisplay = m.styles.MutedText.Render("(type any custom BPF filter e.g. 'tcp port 22 or icmp')")
		} else {
			customDisplay = m.styles.SelectedNode.Render(customDisplay)
		}
		fmt.Fprintf(&box, "  %s[7] Custom Filter: %s█\n\n", cursor, customDisplay)

		box.WriteString(m.styles.MutedText.Render("  [↑/↓: select filter | 1-6: run preset | Type: custom filter | Enter: start live capture | Esc: back]"))
		return m.styles.ModalBox.Render(box.String())
	}

	if m.diagRawOutput != "" || m.diagTraceRes != nil {
		box.WriteString(m.diagResultTop())

		var contentText string
		if m.diagAction == "trace" && m.diagTraceRes != nil && !m.diagRawView {
			var traceContent strings.Builder
			tr := m.diagTraceRes
			verdictStyle := m.styles.BadgeBound
			if tr.Verdict != "delivered" {
				verdictStyle = m.styles.BadgeUnbound
			}
			fmt.Fprintf(&traceContent, "Verdict: %s\n", verdictStyle.Render(strings.ToUpper(tr.Verdict)))
			fmt.Fprintf(&traceContent, "Summary: %s\n", tr.Summary)
			expected := tr.Expected
			if expected == "" {
				expected = "not checked (router port or unknown IP)"
			}
			fmt.Fprintf(&traceContent, "Expected: %s\n\n", expected)

			if m.diagTraceParams != "" {
				traceContent.WriteString("Simulation Endpoints:\n")
				for _, pLine := range strings.Split(m.diagTraceParams, "\n") {
					traceContent.WriteString("  • " + pLine + "\n")
				}
				traceContent.WriteString("\n")
			}

			if tr.HopPath != "" {
				traceContent.WriteString("Hop-by-Hop Flow Path:\n")
				traceContent.WriteString("  " + m.styles.ActiveGateway.Render(tr.HopPath) + "\n\n")
			}

			traceContent.WriteString("Pipeline Stages Evaluated:\n")
			for _, st := range tr.Stages {
				fmt.Fprintf(&traceContent, "  • [%s table %d] %-24s ➔ %s\n",
					st.Pipeline, st.Table, st.Name, truncate(st.Action, 48))
			}
			contentText = traceContent.String()
		} else {
			rawText := m.diagRawOutput
			if m.diagRawView {
				rawText = diag.FormatJsonOutput(rawText)
			}
			contentText = rawText
		}

		lines := strings.Split(strings.TrimSpace(contentText), "\n")
		visibleLines := m.diagVisibleLines()

		maxOffset := max(0, len(lines)-visibleLines)
		if m.diagScrollOffset > maxOffset || (m.diagStreaming && m.diagFollow) {
			m.diagScrollOffset = maxOffset
		}

		endIdx := m.diagScrollOffset + visibleLines
		if endIdx > len(lines) {
			endIdx = len(lines)
		}

		displayedLines := lines[m.diagScrollOffset:endIdx]
		box.WriteString("  " + strings.Join(displayedLines, "\n  ") + "\n\n")

		scrollHint := fmt.Sprintf("  [Lines %d-%d of %d | ↑/↓ or j/k: scroll | o: toggle raw | 1-6: run action | Esc/x: close]",
			m.diagScrollOffset+1, endIdx, len(lines))
		box.WriteString(m.styles.MutedText.Render(scrollHint))
		return m.styles.ModalBox.Render(box.String())
	}

	on := " on node " + t.Node
	switch {
	case t.ChooseChassis && t.Node == "":
		on = " — asks which chassis"
	case t.Node == "":
		on = ""
	}
	menu := map[diagAction]string{
		actTrace:     "ovn-trace            (microflow from " + firstNonEmpty(t.Port, "—") + " through the logical pipeline)",
		actTcpdump:   "Live tcpdump         (capture on this port's host interface" + on + ")",
		actConntrack: "Filtered Conntrack   (flows for " + firstNonEmpty(t.SrcIP, "all IPs") + on + ")",
		actOvsPort:   "Port OVS Interface   (link state, ofport, MTU, rx/tx of iface-id " + firstNonEmpty(t.Port, "—") + ")",
		actVsctl:     "Physical Bridges     (ovs-vsctl show" + on + ")",
		actLogs:      "Controller Logs      (ovn-controller" + on + ")",
	}
	box.WriteString(m.styles.Header.Render("  Choose a Diagnostic Action:") + "\n")
	for i, a := range diagActions {
		if ok, why := t.can(a); ok {
			fmt.Fprintf(&box, "    [%d] %s\n", i+1, menu[a])
		} else {
			box.WriteString(m.styles.MutedText.Render(fmt.Sprintf("    [%d] %s — unavailable: %s", i+1, strings.SplitN(menu[a], "(", 2)[0], why)) + "\n")
		}
	}
	hint := "  Press 1-6 to execute | Esc or x to cancel"
	if t.ChooseChassis {
		hint = "  Press 1-6 to execute | c: choose chassis | Esc or x to cancel"
	}
	box.WriteString("\n" + m.styles.MutedText.Render(hint))

	return m.styles.ModalBox.Render(box.String())
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		if maxLen > 3 {
			return s[:maxLen-3] + "..."
		}
		return s[:maxLen]
	}
	return s
}
