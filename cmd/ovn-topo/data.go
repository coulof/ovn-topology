package main

// Data layer: daemon / fixture fetches and the indexes the views and diagnostics share.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/graph"
)

// RouterPortConnection represents an L3 interconnection between an LR port and an LS.
type RouterPortConnection struct {
	RouterID      string
	RouterName    string
	VPCName       string
	PortName      string
	MAC           string
	Networks      []string
	SwitchID      string
	SwitchName    string
	PeerPortName  string
	ActiveGateway string
	IsShared      bool
}

// viaJSON round-trips v through JSON so in-process fixture data has exactly the
// shapes the daemon sends ([]interface{}, float64…), keeping one code path.
func viaJSON[T any](v *T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var fresh T
	if err := json.Unmarshal(b, &fresh); err != nil {
		return err
	}
	*v = fresh
	return nil
}

// l2PageSize is the daemon's maximum page size for view=l2.
const l2PageSize = 200

var errNotModified = errors.New("not modified")

func httpGetJSON(ctx context.Context, rawURL, etag string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach daemon at %s: %w", rawURL, err)
	}
	defer func() { _ = r.Body.Close() }()
	switch r.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		return errNotModified
	default:
		return &httpStatusError{code: r.StatusCode, url: rawURL, body: bodyExcerpt(r.Body)}
	}
	return json.NewDecoder(r.Body).Decode(out)
}

// httpStatusError keeps the status and a body excerpt so a 404 from whatever
// happens to listen on the daemon port can be told apart from the daemon.
type httpStatusError struct {
	code      int
	url, body string
}

func (e *httpStatusError) Error() string {
	msg := fmt.Sprintf("daemon returned HTTP %d for %s", e.code, e.url)
	if e.body != "" {
		msg += ": " + e.body
	}
	return msg
}

func bodyExcerpt(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 200))
	return strings.Join(strings.Fields(string(b)), " ")
}

// explainNotFound probes /version after a 404: ovn-topologyd serves every
// /api/v1 route it knows, so a 404 usually means another service owns the port.
func explainNotFound(ctx context.Context, daemon string, err error) error {
	var se *httpStatusError
	if !errors.As(err, &se) || se.code != http.StatusNotFound {
		return err
	}
	var v api.VersionInfo
	if verr := httpGetJSON(ctx, apiURL(daemon, "/version", nil), "", &v); verr != nil || v.ApiVersion == "" {
		host := daemon
		if u, perr := url.Parse(daemon); perr == nil && u.Host != "" {
			host = u.Host
		}
		return fmt.Errorf("%w\nthe service at %s is not ovn-topologyd (check what listens on that port / your port-forward)", err, host)
	}
	return fmt.Errorf("%w\novn-topologyd %s (API %s) answered /version but not this route; is the TUI newer than the daemon?", err, v.Version, v.ApiVersion)
}

func apiURL(daemonURL, path string, q url.Values) string {
	u := strings.TrimRight(daemonURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// fetchGraphCmd loads the L3 graph. With etag set, a 304 yields graphUnchangedMsg.
func (m model) fetchGraphCmd() tea.Cmd {
	etag := ""
	if m.graphResp != nil {
		etag = strconv.Itoa(m.graphResp.Revision)
	}
	fixture, daemon := m.fixturePath, m.daemonURL
	return func() tea.Msg {
		if fixture != "" {
			snap := graph.LoadTestSnapshot(nil)
			resp, err := snap.BuildGraph("l3", "", 1, 1, 100)
			if err == nil {
				err = viaJSON(resp)
			}
			return graphLoadedMsg{resp: resp, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp api.GraphResponse
		err := httpGetJSON(ctx, apiURL(daemon, "/graph", url.Values{"view": {"l3"}}), etag, &resp)
		if errors.Is(err, errNotModified) {
			return graphUnchangedMsg{}
		}
		if err != nil {
			return graphLoadedMsg{err: explainNotFound(ctx, daemon, err)}
		}
		return graphLoadedMsg{resp: &resp}
	}
}

// fetchL2All returns every LSP of a switch, following pagination.
func fetchL2All(fixture, daemon, lsID string) ([]api.GraphNode, error) {
	var lsps []api.GraphNode
	var snap *graph.Snapshot
	if fixture != "" {
		snap = graph.LoadTestSnapshot(nil)
	}
	for page := 1; ; page++ {
		var resp *api.GraphResponse
		if snap != nil {
			r, err := snap.BuildGraph("l2", lsID, 1, page, l2PageSize)
			if err == nil {
				err = viaJSON(r)
			}
			if err != nil {
				return nil, err
			}
			resp = r
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var r api.GraphResponse
			err := httpGetJSON(ctx, apiURL(daemon, "/graph", url.Values{
				"view": {"l2"}, "focus": {lsID}, "page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(l2PageSize)},
			}), "", &r)
			cancel()
			if err != nil {
				return nil, err
			}
			resp = &r
		}
		for _, n := range resp.Nodes {
			if n.Type == api.GraphNodeTypeLSP {
				lsps = append(lsps, n)
			}
		}
		if resp.Pagination == nil || page >= resp.Pagination.TotalPages {
			return lsps, nil
		}
	}
}

func (m model) fetchL2Cmd(lsID string) tea.Cmd {
	fixture, daemon := m.fixturePath, m.daemonURL
	return func() tea.Msg {
		lsps, err := fetchL2All(fixture, daemon, lsID)
		return l2LoadedMsg{lsID: lsID, lsps: lsps, err: err}
	}
}

func (m model) fetchChassisCmd() tea.Cmd {
	fixture, daemon := m.fixturePath, m.daemonURL
	return func() tea.Msg {
		if fixture != "" {
			snap := graph.LoadTestSnapshot(nil)
			resp, err := snap.BuildGraph("chassis", "", 1, 1, 100)
			if err == nil {
				err = viaJSON(resp)
			}
			return chassisLoadedMsg{resp: resp, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp api.GraphResponse
		if err := httpGetJSON(ctx, apiURL(daemon, "/graph", url.Values{"view": {"chassis"}}), "", &resp); err != nil {
			return chassisLoadedMsg{err: err}
		}
		return chassisLoadedMsg{resp: &resp}
	}
}

// fetchNodeDetailCmd loads /node/{id} for the describe screen.
func (m model) fetchNodeDetailCmd(id string) tea.Cmd {
	fixture, daemon := m.fixturePath, m.daemonURL
	return func() tea.Msg {
		if fixture != "" {
			d, err := graph.LoadTestSnapshot(nil).GetNodeDetail(id)
			if err == nil {
				err = viaJSON(d)
			}
			return nodeDetailMsg{id: id, detail: d, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var d api.NodeDetail
		if err := httpGetJSON(ctx, apiURL(daemon, "/node/"+url.PathEscape(id), nil), "", &d); err != nil {
			return nodeDetailMsg{id: id, err: err}
		}
		return nodeDetailMsg{id: id, detail: &d}
	}
}

func (m *model) applyGraph(resp *api.GraphResponse) {
	m.err = nil
	m.graphResp = resp

	m.nodeMap = make(map[string]api.GraphNode, len(resp.Nodes))
	for _, n := range resp.Nodes {
		m.nodeMap[n.Id] = n
	}

	// Group nodes by visual section
	m.vpcGroups = make(map[string][]api.GraphNode)
	m.sharedNodes = make([]api.GraphNode, 0)
	m.externalNodes = make([]api.GraphNode, 0)

	for _, n := range resp.Nodes {
		if n.Type == api.GraphNodeTypeVPC {
			continue
		}
		if n.Type == api.GraphNodeTypeExternal {
			m.externalNodes = append(m.externalNodes, n)
		} else if n.Parent != nil && *n.Parent != "" {
			vpcName := strings.TrimPrefix(*n.Parent, graph.PrefixVPC)
			m.vpcGroups[vpcName] = append(m.vpcGroups[vpcName], n)
		} else {
			m.sharedNodes = append(m.sharedNodes, n)
		}
	}

	// 1. Sort VPC names deterministically (default VPC first, then alphabetical)
	m.vpcNames = make([]string, 0, len(m.vpcGroups))
	for vpcName := range m.vpcGroups {
		m.vpcNames = append(m.vpcNames, vpcName)
	}
	sort.Slice(m.vpcNames, func(i, j int) bool {
		iDef := m.vpcNames[i] == "default" || m.vpcNames[i] == "ovn-cluster"
		jDef := m.vpcNames[j] == "default" || m.vpcNames[j] == "ovn-cluster"
		if iDef != jDef {
			return iDef
		}
		return m.vpcNames[i] < m.vpcNames[j]
	})

	// Build flat navigable list matching the EXACT visual top-to-bottom order on screen
	m.nodeList = make([]api.GraphNode, 0)
	for _, vpcName := range m.vpcNames {
		nodes := m.vpcGroups[vpcName]
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Type != nodes[j].Type {
				return nodes[i].Type == api.GraphNodeTypeLR // Router first, then Switches
			}
			return nodes[i].Name < nodes[j].Name
		})
		m.vpcGroups[vpcName] = nodes
		m.nodeList = append(m.nodeList, nodes...)
	}

	sort.Slice(m.sharedNodes, func(i, j int) bool {
		return m.sharedNodes[i].Name < m.sharedNodes[j].Name
	})
	m.nodeList = append(m.nodeList, m.sharedNodes...)

	sort.Slice(m.externalNodes, func(i, j int) bool {
		return m.externalNodes[i].Name < m.externalNodes[j].Name
	})
	m.nodeList = append(m.nodeList, m.externalNodes...)

	// Index all router-peer interconnections from Edges
	m.portList = make([]RouterPortConnection, 0)
	m.routerPorts = make(map[string][]RouterPortConnection)
	m.switchPorts = make(map[string][]RouterPortConnection)

	// Count attachments to identify shared/interconnect switches
	switchAttachmentCount := make(map[string]int)
	for _, e := range resp.Edges {
		if e.Type == api.RouterPeer {
			switchAttachmentCount[e.Target]++
		}
	}

	for _, e := range resp.Edges {
		if e.Type != api.RouterPeer {
			continue
		}
		srcNode, hasSrc := m.nodeMap[e.Source]
		tgtNode, hasTgt := m.nodeMap[e.Target]
		if !hasSrc || !hasTgt {
			continue
		}

		lrpName := ""
		lrpMac := ""
		var networks []string
		lspName := ""
		activeGw := ""

		if e.Properties != nil {
			props := *e.Properties
			if p, ok := props["lrp"].(string); ok {
				lrpName = p
			}
			if mac, ok := props["lrpMac"].(string); ok {
				lrpMac = mac
			}
			if l, ok := props["lsp"].(string); ok {
				lspName = l
			}
			if gw, ok := props["activeGateway"].(string); ok {
				activeGw = gw
			}
			if nets, ok := props["networks"].([]interface{}); ok {
				for _, it := range nets {
					if s, ok := it.(string); ok {
						networks = append(networks, s)
					}
				}
			}
		}
		if len(networks) == 0 && e.Label != nil {
			networks = strings.Split(*e.Label, ", ")
		}

		vpcName := srcNode.Name
		if srcNode.Parent != nil {
			vpcName = strings.TrimPrefix(*srcNode.Parent, graph.PrefixVPC)
		}

		isShared := switchAttachmentCount[e.Target] > 1

		conn := RouterPortConnection{
			RouterID:      srcNode.Id,
			RouterName:    srcNode.Name,
			VPCName:       vpcName,
			PortName:      lrpName,
			MAC:           lrpMac,
			Networks:      networks,
			SwitchID:      tgtNode.Id,
			SwitchName:    tgtNode.Name,
			PeerPortName:  lspName,
			ActiveGateway: activeGw,
			IsShared:      isShared,
		}

		m.portList = append(m.portList, conn)
		m.routerPorts[srcNode.Id] = append(m.routerPorts[srcNode.Id], conn)
		m.switchPorts[tgtNode.Id] = append(m.switchPorts[tgtNode.Id], conn)
	}

	sort.Slice(m.portList, func(i, j int) bool {
		if m.portList[i].RouterName != m.portList[j].RouterName {
			return m.portList[i].RouterName < m.portList[j].RouterName
		}
		return m.portList[i].PortName < m.portList[j].PortName
	})

	// Offline fixture: populate workloads synchronously so the first frame is complete.
	if m.fixturePath != "" {
		for _, n := range resp.Nodes {
			if n.Type == api.GraphNodeTypeLS {
				if lsps, err := fetchL2All(m.fixturePath, m.daemonURL, n.Id); err == nil {
					m.switchWorkloads[n.Id] = lsps
				}
			}
		}
	}

	m.rebuildNodeList()
	m.rebuildTree()
}

func (m *model) rebuildNodeList() {
	var list []api.GraphNode
	for _, vpcName := range m.vpcNames {
		list = append(list, m.vpcGroups[vpcName]...)
	}
	list = append(list, m.sharedNodes...)
	list = append(list, m.externalNodes...)
	m.nodeList = list
}

// fetchSwitchesCmd refetches the ports of the given switches (nil = every switch).
// It runs after applyGraph, so new switches and port-count changes are detected
// against the fresh L3 graph.
func (m model) fetchSwitchesCmd(only map[string]bool) tea.Cmd {
	var cmds []tea.Cmd
	for _, n := range m.nodeList {
		if n.Type != api.GraphNodeTypeLS {
			continue
		}
		loaded, have := m.switchWorkloads[n.Id]
		stale := !have || pint(props(&n), "portCount") != len(loaded)
		if only == nil || only[n.Id] || stale {
			cmds = append(cmds, m.fetchL2Cmd(n.Id))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// refetchPlan maps SSE changed IDs to the switches whose ports must be reloaded
// and whether the chassis view is stale. A nil switch set means "all switches".
func (m model) refetchPlan(changed []string) (map[string]bool, bool) {
	if len(changed) == 0 || m.tree == nil {
		return nil, true
	}
	only := map[string]bool{}
	chassis := false
	for _, id := range changed {
		switch {
		case id == "*":
			return nil, true
		case strings.HasPrefix(id, graph.PrefixLS):
			only[id] = true
		case strings.HasPrefix(id, graph.PrefixLSP):
			// Port_Binding changes are reported as lsp:<logical_port name>, NB ones as lsp:<uuid>.
			n := m.tree.byID[id]
			if n == nil {
				n = m.tree.lspByName[strings.TrimPrefix(id, graph.PrefixLSP)]
			}
			if n == nil {
				return nil, true // unknown port: a new one, reload everything
			}
			for _, r := range n.refs {
				if r.label == "subnet" {
					only[r.to] = true
				}
			}
			chassis = true // bindings move ports between chassis
		case strings.HasPrefix(id, graph.PrefixChassis):
			chassis = true
		}
	}
	return only, chassis
}
