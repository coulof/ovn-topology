package main

// Describe: everything known about one object — graph properties, Kubernetes
// identity, relations, and the daemon's /node/{id} detail (external_ids, raw columns).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coulof/ovn-topology/pkg/api"
)

type descState struct {
	node    *xnode
	detail  *api.NodeDetail
	err     error
	loading bool
	raw     bool
	off     int
}

func (m *model) openDescribe(n *xnode, raw bool) tea.Cmd {
	if n == nil {
		return nil
	}
	m.scr = scrDescribe
	m.desc = &descState{node: n, raw: raw}
	if n.gn != nil && n.kind != kVPC {
		m.desc.loading = true
		return m.fetchNodeDetailCmd(n.gn.Id)
	}
	return nil
}

func fmtValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case string:
		if x == "" {
			return "—"
		}
		return x
	case []interface{}:
		var parts []string
		for _, e := range x {
			parts = append(parts, fmtValue(e))
		}
		if len(parts) == 0 {
			return "—"
		}
		return strings.Join(parts, ", ")
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+fmtValue(x[k]))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	}
	return fmt.Sprintf("%v", v)
}

func sortedKeys[V any](mp map[string]V) []string {
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m model) describeLines() []string {
	st := m.styles
	d := m.desc
	n := d.node
	kv := func(k, v string) string { return "   " + st.Label.Render(pad(k, 20)) + v }
	section := func(s string) string { return " " + st.Value.Render(s) }

	if d.raw {
		var out []string
		var raw map[string]interface{}
		switch {
		case d.detail != nil:
			raw = d.detail.Raw
		case n.gn != nil:
			raw = props(n.gn)
		}
		if n.conn != nil && raw == nil {
			b, _ := json.Marshal(n.conn)
			_ = json.Unmarshal(b, &raw)
		}
		if raw == nil {
			return []string{"", "   " + st.MutedText.Render("no raw columns for this object")}
		}
		b, err := json.MarshalIndent(raw, "", "  ") // jq-style: indented, keys sorted
		if err != nil {
			return []string{"", "   " + st.Bad.Render("raw columns not encodable: "+err.Error())}
		}
		for _, l := range strings.Split(string(b), "\n") {
			out = append(out, m.jsonLines(l, m.w()-3)...)
		}
		if d.loading {
			out = append(out, "", "   "+st.MutedText.Render("loading /node detail…"))
		}
		return out
	}

	out := []string{" " + m.glyph(n.health) + " " + m.kind(n) + " " + st.Value.Render(n.name) + "  " + m.badge(n)}
	if n.summary != "" {
		out = append(out, "   "+st.MutedText.Render(n.summary))
	}
	out = append(out, "", kv("ID", n.id))
	if c := n.conn; c != nil {
		out = append(out,
			kv("Router", c.RouterName+"  (VPC "+c.VPCName+")"),
			kv("Networks", st.CIDR.Render(strings.Join(c.Networks, ", "))),
			kv("MAC", firstNonEmpty(c.MAC, "—")),
			kv("Peer switch", firstNonEmpty(c.SwitchName, "—")),
			kv("Peer LSP", firstNonEmpty(c.PeerPortName, "—")),
			kv("Active gateway", firstNonEmpty(m.resolveNodeName(c.ActiveGateway), "— (distributed)")),
			kv("Role", portRole(c)))
	}
	if p := props(n.gn); len(p) > 0 {
		out = append(out, "", section("Properties"))
		for _, k := range sortedKeys(p) {
			out = append(out, m.kvBlock(k, p[k], 0)...)
		}
	}
	if n.gn != nil && n.gn.K8s != nil {
		k := n.gn.K8s
		var rows []string
		for _, f := range []struct{ k, v string }{
			{"Namespace", deref(k.Namespace)}, {"Pod", deref(k.Pod)}, {"Workload", strings.TrimSpace(deref(k.WorkloadKind) + " " + deref(k.WorkloadName))},
			{"Owner", deref(k.Owner)}, {"VMI", deref(k.Vmi)}, {"Node", deref(k.Node)}, {"Subnet", deref(k.Subnet)},
			{"VPC", deref(k.Vpc)}, {"NAD", deref(k.NetworkAttachment)}, {"Vendor", deref(k.Vendor)},
		} {
			if f.v != "" {
				rows = append(rows, kv(f.k, f.v))
			}
		}
		if len(rows) > 0 {
			out = append(out, "", section("Kubernetes"))
			out = append(out, rows...)
		}
	}
	if len(n.refs) > 0 && m.tree != nil {
		out = append(out, "", section("Related"))
		for _, r := range n.refs {
			if t := m.tree.byID[r.to]; t != nil {
				out = append(out, "   "+st.MutedText.Render(pad(r.label, 20))+m.kind(t)+" "+t.name+" "+m.badge(t))
			}
		}
	}
	if len(n.children) > 0 {
		out = append(out, "", section(fmt.Sprintf("Children (%d)", len(n.children))))
		for i, c := range n.children {
			if i == 50 {
				out = append(out, "   "+st.MutedText.Render(fmt.Sprintf("… %d more (use the xray tree)", len(n.children)-50)))
				break
			}
			out = append(out, "   "+m.glyph(c.health)+" "+m.kind(c)+" "+c.name+"  "+st.MutedText.Render(c.summary))
		}
	}
	switch {
	case d.loading:
		out = append(out, "", "   "+st.MutedText.Render("loading /node detail…"))
	case d.err != nil:
		out = append(out, "", "   "+st.Bad.Render("node detail unavailable: "+d.err.Error()))
	case d.detail != nil:
		if len(d.detail.ExternalIds) > 0 {
			out = append(out, "", section("External IDs"))
			for _, k := range sortedKeys(d.detail.ExternalIds) {
				out = append(out, m.kvBlock(k, d.detail.ExternalIds[k], 0)...)
			}
		}
		if d.detail.Related != nil && len(*d.detail.Related) > 0 {
			out = append(out, "", section("Related objects (daemon)"))
			for _, r := range *d.detail.Related {
				out = append(out, "   "+st.MutedText.Render(pad(r.Relationship, 20))+r.Type+" "+firstNonEmpty(deref(r.Name), r.Id))
			}
		}
	}
	return out
}

// rawTable names the OVSDB table behind /node/{id} raw columns, per object kind.
var rawTable = map[string]string{
	kLR: "NB Logical_Router", kLRP: "NB Logical_Router_Port", kLS: "NB Logical_Switch",
	kLSP: "NB Logical_Switch_Port", kChassis: "SB Chassis",
}

func (m model) describeView() []string {
	st := m.styles
	if m.desc == nil || m.desc.node == nil {
		return box(nil, m.w(), m.areaH(), "", st.Border)
	}
	lines := m.describeLines()
	off := min(m.desc.off, max(len(lines)-m.bodyH(), 0))
	verb, source := "Describe", ""
	if m.desc.raw {
		verb = "Raw"
		// Say what the raw data is: the OVSDB row from /node, or (no daemon detail) graph properties.
		source = "  — graph properties"
		if m.desc.detail != nil && rawTable[m.desc.node.kind] != "" {
			source = "  — " + rawTable[m.desc.node.kind] + " row"
		}
	}
	title := st.Label.Render(verb) + st.CIDR.Render("("+m.desc.node.kind+")") + " " + m.desc.node.name + st.Value.Render(source) +
		st.MutedText.Render("  esc: back · y: raw · j/k: scroll · G: end")
	return box(lines[off:], m.w(), m.areaH(), title, st.Border)
}

// kvBlock renders one describe property. Values that fit stay on one line; a
// map or list that does not fit unfolds into one indented line per entry, and a
// long scalar wraps, so nothing is cut off with "…".
func (m model) kvBlock(k string, v interface{}, depth int) []string {
	st := m.styles
	ind := 3 + 2*depth
	lw := max(20-2*depth, 8)
	label := strings.Repeat(" ", ind) + st.Label.Render(pad(k, lw))
	inline := fmtValue(v)
	avail := m.w() - 2 - ind - lw
	if len([]rune(inline)) <= avail {
		return []string{label + inline}
	}
	var out []string
	switch x := v.(type) {
	case map[string]interface{}:
		out = append(out, label+st.MutedText.Render(fmt.Sprintf("{%d}", len(x))))
		for _, kk := range sortedKeys(x) {
			out = append(out, m.kvBlock(kk, x[kk], depth+1)...)
		}
		return out
	case []interface{}:
		out = append(out, label+st.MutedText.Render(fmt.Sprintf("[%d]", len(x))))
		for i, e := range x {
			out = append(out, m.kvBlock(fmt.Sprintf("[%d]", i), e, depth+1)...)
		}
		return out
	}
	for i, l := range hardWrap(inline, max(avail, 10)) {
		if i == 0 {
			out = append(out, label+l)
		} else {
			out = append(out, strings.Repeat(" ", ind+lw)+l)
		}
	}
	return out
}

// hardWrap splits s into chunks of at most w runes (no word boundaries: OVSDB
// values such as matches and option lists rarely have useful ones).
func hardWrap(s string, w int) []string {
	r := []rune(s)
	var out []string
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	return append(out, string(r))
}

type jsonSeg struct {
	text string
	kind byte // k key, s string, n number/bool, z null, p punctuation/space
}

// tokenizeJSONLine splits one line of MarshalIndent output into coloured segments.
func tokenizeJSONLine(l string) []jsonSeg {
	var segs []jsonSeg
	r := []rune(l)
	for i := 0; i < len(r); {
		switch c := r[i]; {
		case c == '"':
			j := i + 1
			for j < len(r) && r[j] != '"' {
				if r[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(r))
			kind := byte('s')
			if rest := strings.TrimLeft(string(r[j:]), " "); strings.HasPrefix(rest, ":") {
				kind = 'k'
			}
			segs = append(segs, jsonSeg{string(r[i:j]), kind})
			i = j
		case c == '-' || c >= '0' && c <= '9' || c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(r) && !strings.ContainsRune(",]} ", r[j]) {
				j++
			}
			kind := byte('n')
			if string(r[i:j]) == "null" {
				kind = 'z'
			}
			segs = append(segs, jsonSeg{string(r[i:j]), kind})
			i = j
		default:
			segs = append(segs, jsonSeg{string(c), 'p'})
			i++
		}
	}
	return segs
}

// jsonLines colours one MarshalIndent line like jq and wraps it at w cells,
// continuing at the line's indentation + 2. Only colour is added: the text
// itself stays valid JSON.
func (m model) jsonLines(l string, w int) []string {
	st := m.styles
	style := map[byte]func(...string) string{
		'k': st.CIDR.Render, 's': st.Ok.Render, 'n': st.ActiveGateway.Render, 'z': st.Faint.Render, 'p': st.MutedText.Render,
	}
	indent := len(l) - len(strings.TrimLeft(l, " "))
	cont := strings.Repeat(" ", min(indent+2, w/2))
	var out []string
	cur, curW := " ", 1
	for _, seg := range tokenizeJSONLine(l) {
		r := []rune(seg.text)
		for len(r) > 0 {
			room := w - curW
			if room <= 0 {
				out = append(out, cur)
				cur, curW = " "+cont, 1+len(cont)
				continue
			}
			n := min(room, len(r))
			cur += style[seg.kind](string(r[:n]))
			curW += n
			r = r[n:]
		}
	}
	return append(out, cur)
}
