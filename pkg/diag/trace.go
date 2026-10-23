package diag

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Dangerous shell metacharacters that must never appear in any argument.
var invalidChars = regexp.MustCompile(`[;&|` + "`" + `$\r\n\t]`)

// TraceRequest defines the parameters for building an ovn-trace microflow command.
type TraceRequest struct {
	Datapath string // Logical switch or router datapath name
	Inport   string // Ingress Logical Switch Port name
	SrcMAC   string // Source MAC address
	DstMAC   string // Destination (or next-hop router) MAC address
	SrcIP    string // Source IPv4 address
	DstIP    string // Destination IPv4 address
	Protocol string // "icmp", "tcp", "udp" (default: "icmp")
	SrcPort  int    // Source port for TCP/UDP
	DstPort  int    // Destination port for TCP/UDP
	CTState  string // "new" or "est" (default: "new")
}

// TraceStage represents a parsed stage from the ovn-trace execution tree.
type TraceStage struct {
	Pipeline string // "ingress" or "egress"
	Table    int
	Name     string
	Action   string
	Match    string
}

// TraceExpect is where the traced packet should leave the logical network, derived
// from the chosen destination. The zero value checks nothing.
type TraceExpect struct {
	Port   string // the logical port that owns the destination IP
	Egress bool   // the destination is outside OVN: a localnet port or a node's ovn0 (node-*) port
}

// TraceOutput is one "output to" line: the port the packet was sent to and its
// Port_Binding type ("patch", "localnet", "" for a VIF...).
type TraceOutput struct {
	Port string
	Type string
}

// TraceResult summarizes the output of an ovn-trace run.
type TraceResult struct {
	RawOutput string
	Verdict   string // "delivered", "misrouted", "dropped", "no-route", "unknown"
	Summary   string
	FinalPort string
	Expected  string // what the verdict was checked against; empty when unchecked
	HopPath   string
	Outputs   []TraceOutput
	Stages    []TraceStage
}

// transitTypes are port types a packet crosses on its way to another datapath; an
// output to one of them is a hop, not where the packet ends up.
var transitTypes = map[string]bool{"patch": true, "chassisredirect": true, "l3gateway": true}

// egressPort reports whether an output leaves the OVN overlay: a localnet port, or a
// Kube-OVN node port (node-<host> on join, the host's ovn0).
func egressPort(o TraceOutput) bool {
	return o.Type == "localnet" || strings.HasPrefix(o.Port, "localnet.") || strings.HasPrefix(o.Port, "node-")
}

// BuildTraceCommand constructs the typed argv slice for running ovn-trace in ovn-central.
func BuildTraceCommand(req TraceRequest) ([]string, error) {
	if req.Datapath == "" {
		return nil, fmt.Errorf("datapath cannot be empty")
	}
	if req.Inport == "" {
		return nil, fmt.Errorf("inport cannot be empty")
	}
	if req.SrcIP == "" || req.DstIP == "" {
		return nil, fmt.Errorf("both src-ip and dst-ip are required")
	}

	// Validate inputs against injection
	for _, field := range []string{req.Datapath, req.Inport, req.SrcMAC, req.DstMAC, req.SrcIP, req.DstIP} {
		if invalidChars.MatchString(field) {
			return nil, fmt.Errorf("invalid characters in parameter: %q", field)
		}
	}

	ct := req.CTState
	if ct == "" {
		ct = "new"
	}
	if ct != "new" && ct != "est" {
		return nil, fmt.Errorf("invalid ct state: must be 'new' or 'est'")
	}

	proto := strings.ToLower(req.Protocol)
	if proto == "" {
		proto = "icmp"
	}

	var matchParts []string
	matchParts = append(matchParts, fmt.Sprintf(`inport == "%s"`, req.Inport))

	if req.SrcMAC != "" {
		matchParts = append(matchParts, fmt.Sprintf("eth.src == %s", req.SrcMAC))
	}
	if req.DstMAC != "" {
		matchParts = append(matchParts, fmt.Sprintf("eth.dst == %s", req.DstMAC))
	}

	matchParts = append(matchParts, "ip.ttl == 255")
	matchParts = append(matchParts, fmt.Sprintf("ip4.src == %s", req.SrcIP))
	matchParts = append(matchParts, fmt.Sprintf("ip4.dst == %s", req.DstIP))

	switch proto {
	case "icmp":
		matchParts = append(matchParts, "ip.proto == 1 && icmp4.type == 8 && icmp4.code == 0")
	case "tcp":
		srcP := req.SrcPort
		if srcP <= 0 {
			srcP = 40000
		}
		dstP := req.DstPort
		if dstP <= 0 {
			dstP = 80
		}
		matchParts = append(matchParts, fmt.Sprintf("ip.proto == 6 && tcp.src == %d && tcp.dst == %d", srcP, dstP))
	case "udp":
		srcP := req.SrcPort
		if srcP <= 0 {
			srcP = 40000
		}
		dstP := req.DstPort
		if dstP <= 0 {
			dstP = 53
		}
		matchParts = append(matchParts, fmt.Sprintf("ip.proto == 17 && udp.src == %d && udp.dst == %d", srcP, dstP))
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (supported: icmp, tcp, udp)", proto)
	}

	matchStr := strings.Join(matchParts, " && ")
	return []string{
		"ovn-trace",
		"--ct=new",
		"--ct=new",
		"--ct=new",
		"--ct=new",
		req.Datapath,
		matchStr,
	}, nil
}

var (
	outputPortRegex = regexp.MustCompile(`output\("([^"]+)"\)`)
	outputRegex     = regexp.MustCompile(`output to "([^"]+)"(?:, type "([^"]*)")?`)
	tableRegex      = regexp.MustCompile(`(?:table=(\d+)\s*\(([^)]*)\)|^\s*(\d+)\.\s+([a-zA-Z0-9_-]+))`)
	datapathRegex   = regexp.MustCompile(`(?:ingress|egress)\(dp="([^"]+)"`)
)

// ParseTraceOutput parses raw ovn-trace output into a structured TraceResult. The
// verdict is checked against expect: a packet that reaches some other port is
// "misrouted", not delivered. Outputs to patch-like ports are hops, so a packet
// handed to a router and dropped there is "dropped".
func ParseTraceOutput(raw string, expect TraceExpect) TraceResult {
	res := TraceResult{
		RawOutput: raw,
		Verdict:   "unknown",
		Summary:   "Trace completed with unclassified verdict",
	}

	lines := strings.Split(raw, "\n")
	var stages []TraceStage
	var hops []string
	currentPipeline := "ingress"
	var sawDrop, sawNoRoute bool

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "# egress") {
			currentPipeline = "egress"
		} else if strings.HasPrefix(trimmed, "# ingress") {
			currentPipeline = "ingress"
		}

		// Datapath hop detection
		if dm := datapathRegex.FindStringSubmatch(trimmed); len(dm) > 1 {
			dp := dm[1]
			if len(hops) == 0 || hops[len(hops)-1] != dp {
				hops = append(hops, dp)
			}
		}

		// Stage detection
		if m := tableRegex.FindStringSubmatch(trimmed); len(m) > 0 {
			var tableNum int
			var stageName string
			if m[1] != "" {
				tableNum, _ = strconv.Atoi(m[1])
				stageName = m[2]
			} else if len(m) > 4 && m[3] != "" {
				tableNum, _ = strconv.Atoi(m[3])
				stageName = m[4]
			}

			if stageName != "" {
				stages = append(stages, TraceStage{
					Pipeline: currentPipeline,
					Table:    tableNum,
					Name:     stageName,
					Action:   trimmed,
				})
			}
		}

		if m := outputPortRegex.FindStringSubmatch(trimmed); len(m) > 1 {
			res.Outputs = append(res.Outputs, TraceOutput{Port: m[1]})
		} else if m := outputRegex.FindStringSubmatch(trimmed); len(m) > 1 {
			res.Outputs = append(res.Outputs, TraceOutput{Port: m[1], Type: m[2]})
		} else if strings.Contains(trimmed, "drop;") || strings.Contains(trimmed, "/* drop */") || strings.Contains(trimmed, "action=drop") {
			sawDrop = true
		} else if strings.Contains(trimmed, "No route") || strings.Contains(trimmed, "unroutable") {
			sawNoRoute = true
		}
	}
	res.judge(expect, sawDrop, sawNoRoute)

	if res.FinalPort != "" {
		hops = append(hops, fmt.Sprintf("Port '%s'", res.FinalPort))
	}
	if len(hops) > 0 {
		res.HopPath = strings.Join(hops, " ──► ")
	}

	res.Stages = stages
	return res
}

// judge sets the verdict from the terminal outputs (those that are not hops).
func (res *TraceResult) judge(expect TraceExpect, sawDrop, sawNoRoute bool) {
	var terminals []TraceOutput
	for _, o := range res.Outputs {
		if !transitTypes[o.Type] {
			terminals = append(terminals, o)
		}
	}
	switch {
	case expect.Port != "":
		res.Expected = fmt.Sprintf("port %s", expect.Port)
	case expect.Egress:
		res.Expected = "an exit from OVN (localnet or node ovn0 port)"
	}
	for _, o := range terminals {
		if (expect.Port != "" && o.Port == expect.Port) || (expect.Port == "" && expect.Egress && egressPort(o)) {
			res.Verdict, res.FinalPort = "delivered", o.Port
			if egressPort(o) {
				res.Summary = fmt.Sprintf("Packet left the overlay via %s (the physical network applies from there)", o.Port)
			} else {
				res.Summary = fmt.Sprintf("Packet delivered to port %s", o.Port)
			}
			return
		}
	}
	if len(terminals) > 0 {
		last := terminals[len(terminals)-1]
		res.FinalPort = last.Port
		if res.Expected != "" {
			res.Verdict = "misrouted"
			res.Summary = fmt.Sprintf("Packet reached %s, expected %s", last.Port, res.Expected)
			return
		}
		res.Verdict = "delivered"
		res.Summary = fmt.Sprintf("Packet forwarded to %s (not checked against the destination)", last.Port)
		return
	}
	switch {
	case sawNoRoute:
		res.Verdict, res.Summary = "no-route", "Packet dropped: no route found"
	case sawDrop:
		res.Verdict, res.Summary = "dropped", "Packet dropped by rule or ACL"
	}
}
