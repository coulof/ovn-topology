package diag

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BuildOvsVsctlListInterfaceCommand returns the typed command for querying OVS interfaces.
func BuildOvsVsctlListInterfaceCommand() []string {
	return []string{"ovs-vsctl", "show"}
}

// BuildOvsIfaceDetailCommand returns the typed command for inspecting a specific interface's link state, ofport, and stats.
func BuildOvsIfaceDetailCommand(iface string) ([]string, error) {
	if iface == "" {
		return nil, fmt.Errorf("interface cannot be empty")
	}
	if invalidChars.MatchString(iface) {
		return nil, fmt.Errorf("invalid characters in interface name: %q", iface)
	}
	return []string{
		"ovs-vsctl",
		"--columns=name,ofport,admin_state,link_state,mtu,external_ids,statistics",
		"list",
		"interface",
		iface,
	}, nil
}

// portNameRe is the character set of OVN logical port names (e.g. "app-1.ns", "vm.ns.provider", "node-hv-01").
var portNameRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// BuildFindIfaceByIfaceIDCommand returns the typed command that finds the OVS interface
// bound to an OVN logical port (external_ids:iface-id == port name).
func BuildFindIfaceByIfaceIDCommand(port string) ([]string, error) {
	if !portNameRe.MatchString(port) {
		return nil, fmt.Errorf("invalid logical port name: %q", port)
	}
	return []string{"ovs-vsctl", "--bare", "--columns=name", "find", "interface", "external_ids:iface-id=" + port}, nil
}

// BuildListBridgePortsCommand returns the typed command to list all ports on an OVS bridge.
func BuildListBridgePortsCommand(bridge string) ([]string, error) {
	if bridge == "" {
		return nil, fmt.Errorf("bridge cannot be empty")
	}
	if !portNameRe.MatchString(bridge) {
		return nil, fmt.Errorf("invalid bridge name: %q", bridge)
	}
	return []string{"ovs-vsctl", "list-ports", bridge}, nil
}

// BuildListInterfacesTypeCommand returns the typed command to inspect interface types for given port names.
func BuildListInterfacesTypeCommand(ifaces ...string) ([]string, error) {
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("at least one interface required")
	}
	for _, iface := range ifaces {
		if !portNameRe.MatchString(iface) {
			return nil, fmt.Errorf("invalid interface name: %q", iface)
		}
	}
	cmd := []string{"ovs-vsctl", "--format=json", "--columns=name,type", "list", "interface"}
	return append(cmd, ifaces...), nil
}

// BuildOvsVsctlShowCommand returns the typed command for ovs-vsctl show.
func BuildOvsVsctlShowCommand() []string {
	return []string{"ovs-vsctl", "show"}
}

// BuildCTZoneListCommand returns the typed command for inspecting conntrack zones.
func BuildCTZoneListCommand() []string {
	return []string{"ovs-appctl", "ct-zone-list"}
}

// BuildConntrackDumpCommand returns the typed command for dumping conntrack entries.
func BuildConntrackDumpCommand(zone int) []string {
	if zone > 0 {
		return []string{"ovs-appctl", "dpctl/dump-conntrack", fmt.Sprintf("zone=%d", zone)}
	}
	return []string{"ovs-appctl", "dpctl/dump-conntrack"}
}

// BuildOvsOfctlDumpFlows returns the typed command for inspecting OpenFlow flows.
func BuildOvsOfctlDumpFlows(bridge string) ([]string, error) {
	return BuildOvsOfctlFilteredDumpFlows(bridge, "")
}

// BuildOvsOfctlFilteredDumpFlows returns the typed command for inspecting OpenFlow flows with an optional match filter.
func BuildOvsOfctlFilteredDumpFlows(bridge string, filter string) ([]string, error) {
	if bridge == "" {
		bridge = "br-int"
	}
	if invalidChars.MatchString(bridge) {
		return nil, fmt.Errorf("invalid characters in bridge name: %q", bridge)
	}
	filter = strings.TrimSpace(filter)
	if filter != "" && invalidChars.MatchString(filter) {
		return nil, fmt.Errorf("invalid characters in filter: %q", filter)
	}
	cmd := []string{"ovs-ofctl", "-O", "OpenFlow13", "dump-flows", bridge}
	if filter != "" {
		cmd = append(cmd, filter)
	}
	return cmd, nil
}

// BuildOvnControllerLogTail returns the typed command for inspecting local ovn-controller logs.
func BuildOvnControllerLogTail(lines int) []string {
	if lines <= 0 {
		lines = 50
	}
	if lines > 500 {
		lines = 500
	}
	return []string{"tail", "-n", strconv.Itoa(lines), "/var/log/ovn/ovn-controller.log"}
}

var (
	origRegex  = regexp.MustCompile(`orig=\(([^)]+)\)`)
	replyRegex = regexp.MustCompile(`reply=\(([^)]+)\)`)
	stateRegex = regexp.MustCompile(`state=([A-Za-z0-9_]+)`)
)

// FormatConntrackOutput filters and formats raw dump-conntrack output into a clean human-readable table.
func FormatConntrackOutput(raw string, targetIP string) string {
	lines := strings.Split(raw, "\n")
	var matchingLines []string
	var nonLoopbackLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if targetIP != "" && strings.Contains(trimmed, targetIP) {
			matchingLines = append(matchingLines, trimmed)
		}
		if !strings.Contains(trimmed, "127.0.0.1") && !strings.Contains(trimmed, "::1") {
			nonLoopbackLines = append(nonLoopbackLines, trimmed)
		}
	}

	selected := matchingLines
	header := fmt.Sprintf("Active Conntrack Sessions for IP %s (%d matching):", targetIP, len(matchingLines))
	if len(matchingLines) == 0 {
		selected = nonLoopbackLines
		if len(selected) > 25 {
			selected = selected[:25]
		}
		header = fmt.Sprintf("No active flows for IP %s. Showing active node sessions (%d flows):", targetIP, len(selected))
	}

	var b strings.Builder
	b.WriteString(header + "\n\n")
	fmt.Fprintf(&b, "  %-6s %-38s %-38s %s\n", "PROTO", "ORIGIN", "REPLY", "STATE")
	fmt.Fprintf(&b, "  %-6s %-38s %-38s %s\n", "-----", "------", "-----", "-----")

	for _, line := range selected {
		proto, orig, reply, state := parseConntrackLine(line)
		fmt.Fprintf(&b, "  %-6s %-38s %-38s %s\n", proto, orig, reply, state)
	}

	return b.String()
}

func parseConntrackLine(line string) (proto, orig, reply, state string) {
	parts := strings.SplitN(line, ",", 2)
	proto = parts[0]

	origMatch := origRegex.FindStringSubmatch(line)
	if len(origMatch) > 1 {
		orig = formatTuple(origMatch[1])
	}

	replyMatch := replyRegex.FindStringSubmatch(line)
	if len(replyMatch) > 1 {
		reply = formatTuple(replyMatch[1])
	}

	stateMatch := stateRegex.FindStringSubmatch(line)
	if len(stateMatch) > 1 {
		state = stateMatch[1]
	} else {
		state = "TRACKED"
	}

	return proto, orig, reply, state
}

func formatTuple(tuple string) string {
	kvs := strings.Split(tuple, ",")
	src := ""
	dst := ""
	sport := ""
	dport := ""

	for _, kv := range kvs {
		pair := strings.SplitN(kv, "=", 2)
		if len(pair) != 2 {
			continue
		}
		switch pair[0] {
		case "src":
			src = pair[1]
		case "dst":
			dst = pair[1]
		case "sport":
			sport = pair[1]
		case "dport":
			dport = pair[1]
		}
	}

	if src != "" && dst != "" {
		if sport != "" && dport != "" {
			return fmt.Sprintf("%s:%s -> %s:%s", src, sport, dst, dport)
		}
		return fmt.Sprintf("%s -> %s", src, dst)
	}
	return tuple
}

// FormatJsonOutput pretty-prints a JSON string with 2-space indentation (jq-style).
func FormatJsonOutput(raw string) string {
	var val interface{}
	if err := json.Unmarshal([]byte(raw), &val); err != nil {
		return raw
	}
	formatted, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return raw
	}
	return string(formatted)
}

// FormatOvsIfaceOutput formats ovs-vsctl list interface output into an inspection card.
func FormatOvsIfaceOutput(raw string, iface string) string {
	var b strings.Builder
	lines := strings.Split(raw, "\n")
	fields := make(map[string]string)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			fields[k] = v
		}
	}

	name := strings.Trim(fields["name"], `"`)
	if name == "" {
		name = iface
	}
	ofport := fields["ofport"]
	adminState := fields["admin_state"]
	linkState := fields["link_state"]
	mtu := fields["mtu"]

	fmt.Fprintf(&b, "Contextual OVS Interface Telemetry: '%s'\n\n", name)
	fmt.Fprintf(&b, "  • Host Interface:   %s\n", name)
	fmt.Fprintf(&b, "  • OpenFlow Port:    ofport %s\n", ofport)
	fmt.Fprintf(&b, "  • Interface State:  Link %s / Admin %s\n", strings.ToUpper(linkState), strings.ToUpper(adminState))
	fmt.Fprintf(&b, "  • Configured MTU:   %s bytes\n\n", mtu)

	if stats, ok := fields["statistics"]; ok && stats != "" {
		b.WriteString("  Hardware Packet & Byte Counters:\n")
		statsClean := strings.Trim(stats, "{}")
		for _, statPair := range strings.Split(statsClean, ",") {
			sParts := strings.SplitN(strings.TrimSpace(statPair), "=", 2)
			if len(sParts) == 2 {
				fmt.Fprintf(&b, "    • %-20s %s\n", sParts[0]+":", sParts[1])
			}
		}
		b.WriteString("\n")
	}

	if ext, ok := fields["external_ids"]; ok && ext != "" {
		b.WriteString("  External Metadata:\n")
		extClean := strings.Trim(ext, "{}")
		for _, extPair := range strings.Split(extClean, ",") {
			eParts := strings.SplitN(strings.TrimSpace(extPair), "=", 2)
			if len(eParts) == 2 {
				fmt.Fprintf(&b, "    • %-20s %s\n", eParts[0]+":", strings.Trim(eParts[1], `"`))
			}
		}
	}

	return b.String()
}

var (
	flowTableRegex    = regexp.MustCompile(`table=(\d+)`)
	flowPacketsRegex  = regexp.MustCompile(`n_packets=(\d+)`)
	flowBytesRegex    = regexp.MustCompile(`n_bytes=(\d+)`)
	flowPriorityMatch = regexp.MustCompile(`(?:priority=\d+,)?(.*?)\s+actions=(.*)$`)
)

type openFlowRule struct {
	table   int
	packets int64
	bytes   int64
	match   string
	actions string
}

// FormatOpenFlowDump formats raw ovs-ofctl dump-flows output into a clean, human-readable table.
func FormatOpenFlowDump(raw string, filter string) string {
	lines := strings.Split(raw, "\n")
	var rules []openFlowRule
	var totalPackets int64
	var totalBytes int64

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "OFPST_FLOW") || strings.HasPrefix(trimmed, "NXST_FLOW") {
			continue
		}

		table := 0
		if tm := flowTableRegex.FindStringSubmatch(trimmed); len(tm) > 1 {
			table, _ = strconv.Atoi(tm[1])
		}

		var packets int64
		if pm := flowPacketsRegex.FindStringSubmatch(trimmed); len(pm) > 1 {
			packets, _ = strconv.ParseInt(pm[1], 10, 64)
		}

		var nbytes int64
		if bm := flowBytesRegex.FindStringSubmatch(trimmed); len(bm) > 1 {
			nbytes, _ = strconv.ParseInt(bm[1], 10, 64)
		}

		matchStr := ""
		actionStr := ""
		if sm := flowPriorityMatch.FindStringSubmatch(trimmed); len(sm) > 2 {
			matchStr = strings.TrimSpace(sm[1])
			actionStr = strings.TrimSpace(sm[2])
		} else {
			actionStr = trimmed
		}

		totalPackets += packets
		totalBytes += nbytes

		rules = append(rules, openFlowRule{
			table:   table,
			packets: packets,
			bytes:   nbytes,
			match:   matchStr,
			actions: actionStr,
		})
	}

	if len(rules) == 0 {
		if filter != "" {
			return fmt.Sprintf("No OpenFlow flows matched filter: %s", filter)
		}
		return "No OpenFlow flows found."
	}

	sort.Slice(rules, func(i, j int) bool {
		if rules[i].table != rules[j].table {
			return rules[i].table < rules[j].table
		}
		return rules[i].packets > rules[j].packets
	})

	var out strings.Builder
	if filter != "" {
		fmt.Fprintf(&out, "OpenFlow flows on br-int matching filter: %s\n", filter)
	} else {
		out.WriteString("OpenFlow flows on br-int:\n")
	}
	fmt.Fprintf(&out, "Total Activity: %s packets · %s across %d flow(s)\n\n",
		formatCount(totalPackets), formatBytes(totalBytes), len(rules))

	fmt.Fprintf(&out, "  %-6s  %-12s  %-10s  %s\n", "TABLE", "PACKETS", "BYTES", "MATCH ➔ ACTIONS")
	out.WriteString("  " + strings.Repeat("─", 74) + "\n")

	for _, r := range rules {
		matchDisplay := r.match
		if matchDisplay == "" {
			matchDisplay = "(any)"
		}
		fmt.Fprintf(&out, "  %-6d  %-12s  %-10s  %s ➔ %s\n",
			r.table, formatCount(r.packets), formatBytes(r.bytes), matchDisplay, r.actions)
	}

	return out.String()
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	rem := len(s) % 3
	if rem > 0 {
		parts = append(parts, s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		parts = append(parts, s[i:i+3])
	}
	return strings.Join(parts, ",")
}
