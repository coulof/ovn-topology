package diag

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	if bridge == "" {
		bridge = "br-int"
	}
	if invalidChars.MatchString(bridge) {
		return nil, fmt.Errorf("invalid characters in bridge name: %q", bridge)
	}
	return []string{"ovs-ofctl", "-O", "OpenFlow13", "dump-flows", bridge}, nil
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
