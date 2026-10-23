package main

// Xray navigation: flattening the tree into rows, folding, filtering, jumping
// along relations, and mapping the selection to a diagnostics target.

import (
	"strings"
)

type mode string

const (
	modeVPC      mode = "vpc"
	modeChassis  mode = "chassis"
	modeWorkload mode = "workload"
	modePorts    mode = "ports"
)

// row is one visible line of an xray tree. key is the path of IDs from the
// root (prefixed by the mode) so a shared node keeps separate fold state per place.
type row struct {
	n        *xnode
	key      string
	depth    int
	prefix   string
	ref      bool
	relLabel string
	hasKids  bool
}

type histEntry struct {
	mode mode
	key  string
}

// defaultExpanded opens two levels: the roots and their direct children
// (VPC xray: groups and VPCs, so routers and switches show folded).
func defaultExpanded(r row) bool {
	n := r.n
	if r.depth >= 2 {
		return false
	}
	switch n.kind {
	case kGroup, kVPC, kLR:
		return true
	case kLS, kChassis, kNS:
		return len(n.children) > 0 && len(n.children) <= 16
	}
	return false
}

func (m *model) isExpanded(r row) bool {
	if m.touched[r.key] {
		return m.expanded[r.key]
	}
	return defaultExpanded(r)
}

// visibleRefs drops relations already obvious from the tree: the parent and its children.
func visibleRefs(t *xtree, n *xnode, parent *xnode) []xref {
	skip := map[string]bool{}
	if parent != nil {
		skip[parent.id] = true
		for _, c := range parent.children {
			skip[c.id] = true
		}
	}
	var out []xref
	for _, r := range n.refs {
		if !skip[r.to] && t.byID[r.to] != nil {
			out = append(out, r)
		}
	}
	return out
}

func (m *model) rebuildRows() {
	sel := m.selKey()
	m.rows = m.rows[:0]
	if m.tree == nil || m.mode == modePorts {
		return
	}
	memo := map[*xnode]bool{}
	var matches func(n *xnode) bool
	matches = func(n *xnode) bool {
		if v, ok := memo[n]; ok {
			return v
		}
		memo[n] = false // guards against cycles
		ok := containsFold(n.name+" "+n.summary+" "+n.badge, m.filter)
		for _, c := range n.children {
			if matches(c) {
				ok = true
			}
		}
		memo[n] = ok
		return ok
	}
	// trailing is the number of relation rows that follow nodes at the same level,
	// so the last child keeps a ├─ branch when refs come after it.
	var walk func(nodes []*xnode, parent *xnode, key, prefix string, depth, trailing int)
	walk = func(nodes []*xnode, parent *xnode, key, prefix string, depth, trailing int) {
		if m.filter != "" {
			var keep []*xnode
			for _, n := range nodes {
				if matches(n) {
					keep = append(keep, n)
				}
			}
			nodes = keep
		}
		for i, n := range nodes {
			branch, next := "├─ ", "│  "
			if i == len(nodes)-1 && trailing == 0 {
				branch, next = "└─ ", "   "
			}
			if depth == 0 {
				branch, next = "", ""
			}
			k := key + "/" + n.id
			refs := visibleRefs(m.tree, n, parent)
			r := row{n: n, key: k, depth: depth, prefix: prefix + branch, hasKids: len(n.children) > 0 || len(refs) > 0}
			m.rows = append(m.rows, r)
			open := m.isExpanded(r)
			if m.filter != "" { // filtering shows matches plus their ancestors
				open = len(n.children) > 0
			}
			if !open || strings.Count(k, "/") > 12 {
				continue
			}
			showRefs := m.filter == "" || m.touched[k]
			if !showRefs {
				refs = nil
			}
			walk(n.children, n, k, prefix+next, depth+1, len(refs))
			for j, ref := range refs {
				t := m.tree.byID[ref.to]
				b := "├╌ "
				if j == len(refs)-1 {
					b = "└╌ "
				}
				m.rows = append(m.rows, row{n: t, key: k + "/@" + ref.to, depth: depth + 1, prefix: prefix + next + b, ref: true, relLabel: ref.label})
			}
		}
	}
	walk(m.tree.roots[m.mode], nil, string(m.mode), "", 0, 0)
	// Restore the selection by row key; if that path vanished (object re-parented),
	// fall back to the same object elsewhere before giving up and going to the top.
	m.cursor = 0
	found := false
	for i, r := range m.rows {
		if r.key == sel {
			m.cursor, found = i, true
			break
		}
	}
	if id := sel[strings.LastIndex(sel, "/")+1:]; !found && id != "" {
		id = strings.TrimPrefix(id, "@")
		for i, r := range m.rows {
			if r.n.id == id {
				m.cursor = i
				break
			}
		}
	}
	m.offset = window(m.cursor, m.offset, len(m.rows), m.bodyH())
}

// selKey identifies the selection in the current mode (row key, or LRP id in :ports).
func (m model) selKey() string {
	if m.mode == modePorts {
		if rows := m.portRows(); len(rows) > 0 {
			return lrpID(rows[min(m.pcur, len(rows)-1)].conn)
		}
		return ""
	}
	if m.cursor < len(m.rows) {
		return m.rows[m.cursor].key
	}
	return ""
}

func (m model) selectedRow() *row {
	if m.mode == modePorts || len(m.rows) == 0 {
		return nil
	}
	return &m.rows[min(m.cursor, len(m.rows)-1)]
}

// selectedXNode is the object the user is pointing at (a ref row points at its target).
func (m model) selectedXNode() *xnode {
	if m.mode == modePorts {
		if rows := m.portRows(); len(rows) > 0 && m.tree != nil {
			return m.tree.byID[lrpID(rows[min(m.pcur, len(rows)-1)].conn)]
		}
		return nil
	}
	if r := m.selectedRow(); r != nil {
		return r.n
	}
	return nil
}

func (m *model) toggle(r row, open bool) {
	m.touched[r.key], m.expanded[r.key] = true, open
	m.rebuildRows()
}

// gotoNode switches to the xray whose forest contains id and selects it there.
func (m *model) gotoNode(id string) {
	n := m.tree.byID[id]
	if n == nil {
		m.setFlash("object not found in the current graph", true)
		return
	}
	target := modeVPC
	switch n.kind {
	case kChassis:
		target = modeChassis
	case kWL, kNS:
		target = modeWorkload
	}
	m.history = append(m.history, histEntry{m.mode, m.selKey()})
	m.mode, m.filter = target, ""
	var path []string
	var find func(nodes []*xnode, key string, depth int) bool
	find = func(nodes []*xnode, key string, depth int) bool {
		if depth > 12 {
			return false
		}
		for _, c := range nodes {
			k := key + "/" + c.id
			if c.id == id || find(c.children, k, depth+1) {
				path = append(path, k)
				return true
			}
		}
		return false
	}
	if !find(m.tree.roots[target], string(target), 0) {
		m.rebuildRows()
		m.setFlash(n.name+" is not shown in xray "+string(target), true)
		return
	}
	for _, k := range path[1:] {
		m.touched[k], m.expanded[k] = true, true
	}
	m.rebuildRows()
	for i, r := range m.rows {
		if r.key == path[0] {
			m.cursor = i
		}
	}
	m.offset = window(m.cursor, m.offset, len(m.rows), m.bodyH())
	m.setFlash("jumped to "+n.kind+" "+n.name+"  (b: back)", false)
}

func (m *model) back() {
	if len(m.history) == 0 {
		return
	}
	h := m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	m.mode = h.mode
	m.rebuildRows()
	if m.mode == modePorts {
		for i, r := range m.portRows() {
			if lrpID(r.conn) == h.key {
				m.pcur = i
			}
		}
		return
	}
	for i, r := range m.rows {
		if r.key == h.key {
			m.cursor = i
		}
	}
	m.offset = window(m.cursor, m.offset, len(m.rows), m.bodyH())
}
