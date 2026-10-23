package graph

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
)

// BuildGraph routes the graph request to the appropriate view builder.
func (s *Snapshot) BuildGraph(view string, focus string, depth int, page int, pageSize int) (*api.GraphResponse, error) {
	if depth <= 0 {
		depth = 1
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	} else if pageSize > 200 {
		pageSize = 200
	}

	switch view {
	case "", "l3":
		return s.BuildL3View(focus, depth), nil
	case "l2":
		return s.BuildL2View(focus, page, pageSize)
	case "chassis":
		return s.BuildChassisView(focus, depth), nil
	default:
		return nil, fmt.Errorf("unsupported view: %s", view)
	}
}

// BuildL3View generates the L3 aggregated topology view.
func (s *Snapshot) BuildL3View(focus string, depth int) *api.GraphResponse {
	nodes := make(map[string]api.GraphNode)
	edges := make(map[string]api.GraphEdge)

	// Build compound VPC nodes and LR nodes
	for lrUUID, lr := range s.LogicalRouters {
		vpcName := lr.Name
		if extVPC, ok := lr.ExternalIDs["vpc"]; ok && extVPC != "" {
			vpcName = extVPC
		}
		vpcID := PrefixVPC + vpcName

		// Add compound VPC container node
		if _, ok := nodes[vpcID]; !ok {
			isDefault := vpcName == "ovn-cluster" || vpcName == "default" || lr.Name == "ovn_cluster"
			nodes[vpcID] = api.GraphNode{
				Id:   vpcID,
				Type: api.GraphNodeTypeVPC,
				Name: vpcName,
				Properties: &map[string]interface{}{
					"isDefault": isDefault,
				},
				K8s: ExtractK8sMetadata(lr.ExternalIDs),
			}
		}

		lrID := PrefixLR + lrUUID
		lrProps := map[string]interface{}{
			"natCount":    len(lr.Nat),
			"routeCount":  len(lr.StaticRoutes),
			"policyCount": len(lr.Policies),
			"lbCount":     len(lr.LoadBalancer),
			"enabled":     lr.Enabled == nil || *lr.Enabled,
		}

		// Gateway information on LRPs
		var gatewayRoles []map[string]interface{}
		for _, lrpUUID := range lr.Ports {
			lrp, ok := s.LogicalRouterPorts[lrpUUID]
			if !ok {
				continue
			}
			activeChassis := ""
			if crPB, ok := s.CRPortBindingByLRP[lrp.Name]; ok && crPB.Chassis != nil {
				activeChassis = s.resolveChassisName(*crPB.Chassis)
			}

			if len(lrp.GatewayChassis) > 0 || lrp.HaChassisGroup != nil {
				role := map[string]interface{}{
					"lrp":           lrp.Name,
					"activeChassis": activeChassis,
				}
				gatewayRoles = append(gatewayRoles, role)
			}
		}
		if len(gatewayRoles) > 0 {
			lrProps["gateways"] = gatewayRoles
		}

		parent := vpcID
		nodes[lrID] = api.GraphNode{
			Id:         lrID,
			Type:       api.GraphNodeTypeLR,
			Name:       lr.Name,
			Parent:     &parent,
			Properties: &lrProps,
			K8s:        ExtractK8sMetadata(lr.ExternalIDs),
		}
	}

	// Build LS nodes
	for lsUUID, ls := range s.LogicalSwitches {
		lsID := PrefixLS + lsUUID
		attachedRouters := s.AttachedRoutersByLS[lsUUID]

		// Compound parent: only nest if attached to exactly 1 LR
		var parent *string
		if len(attachedRouters) == 1 {
			lrUUID := attachedRouters[0]
			if lr, ok := s.LogicalRouters[lrUUID]; ok {
				vpcName := lr.Name
				if extVPC, ok := lr.ExternalIDs["vpc"]; ok && extVPC != "" {
					vpcName = extVPC
				}
				p := PrefixVPC + vpcName
				parent = &p
			}
		}

		// Port counts
		totalPorts := len(ls.Ports)
		boundCount := 0
		unboundCount := 0

		for _, lspUUID := range ls.Ports {
			lsp, ok := s.LogicalSwitchPorts[lspUUID]
			if !ok {
				continue
			}
			if lsp.Type == "router" || lsp.Type == "localnet" {
				continue
			}
			if pb, ok := s.PortBindingByPort[lsp.Name]; ok && pb.Chassis != nil && *pb.Chassis != "" {
				boundCount++
			} else {
				unboundCount++
			}
		}

		subnet := ls.Name
		if extSubnet, ok := ls.ExternalIDs["subnet"]; ok && extSubnet != "" {
			subnet = extSubnet
		}

		lsProps := map[string]interface{}{
			"portCount":        totalPorts,
			"boundPortCount":   boundCount,
			"unboundPortCount": unboundCount,
			"subnet":           subnet,
		}

		nodes[lsID] = api.GraphNode{
			Id:         lsID,
			Type:       api.GraphNodeTypeLS,
			Name:       ls.Name,
			Parent:     parent,
			Properties: &lsProps,
			K8s:        ExtractK8sMetadata(ls.ExternalIDs),
		}

		// Check for localnet port connecting LS to external
		for _, lspUUID := range ls.Ports {
			lsp, ok := s.LogicalSwitchPorts[lspUUID]
			if !ok || lsp.Type != "localnet" {
				continue
			}
			netName := lsp.Options["network_name"]
			if netName == "" {
				netName = "external"
			}
			extID := PrefixExternal + netName
			if _, ok := nodes[extID]; !ok {
				nodes[extID] = api.GraphNode{
					Id:   extID,
					Type: api.GraphNodeTypeExternal,
					Name: netName,
				}
			}

			edgeID := fmt.Sprintf("edge:%s-%s-localnet", lsID, extID)
			edges[edgeID] = api.GraphEdge{
				Id:     edgeID,
				Source: lsID,
				Target: extID,
				Type:   api.Localnet,
			}
		}
	}

	// Build router-peer edges between LRs and LSs
	for lsUUID, ls := range s.LogicalSwitches {
		lsID := PrefixLS + lsUUID
		for _, lspUUID := range ls.Ports {
			lsp, ok := s.LogicalSwitchPorts[lspUUID]
			if !ok || lsp.Type != "router" {
				continue
			}
			routerPortName := lsp.Options["router-port"]
			if routerPortName == "" {
				continue
			}
			lrp, ok := s.LRPByName[routerPortName]
			if !ok {
				continue
			}

			// Find owning LR
			for lrUUID, lr := range s.LogicalRouters {
				containsLRP := false
				for _, pUUID := range lr.Ports {
					if pUUID == lrp.UUID {
						containsLRP = true
						break
					}
				}
				if !containsLRP {
					continue
				}

				lrID := PrefixLR + lrUUID
				edgeID := fmt.Sprintf("edge:%s-%s-router-peer", lrID, lsID)
				label := strings.Join(lrp.Networks, ", ")

				props := map[string]interface{}{
					"lrp":      lrp.Name,
					"lrpMac":   lrp.MAC,
					"networks": lrp.Networks,
					"lsp":      lsp.Name,
				}

				if crPB, ok := s.CRPortBindingByLRP[lrp.Name]; ok && crPB.Chassis != nil {
					resolvedGW := s.resolveChassisName(*crPB.Chassis)
					props["activeGateway"] = resolvedGW
					props["isGateway"] = true
				}

				edges[edgeID] = api.GraphEdge{
					Id:         edgeID,
					Source:     lrID,
					Target:     lsID,
					Type:       api.RouterPeer,
					Label:      &label,
					Properties: &props,
				}
			}
		}
	}

	// Apply focus and depth filtering if focus is requested
	if focus != "" {
		nodes, edges = filterGraphByFocus(nodes, edges, focus, depth)
	}

	nodeList := make([]api.GraphNode, 0, len(nodes))
	for _, n := range nodes {
		nodeList = append(nodeList, n)
	}
	sort.Slice(nodeList, func(i, j int) bool {
		return nodeList[i].Id < nodeList[j].Id
	})

	edgeList := make([]api.GraphEdge, 0, len(edges))
	for _, e := range edges {
		edgeList = append(edgeList, e)
	}
	sort.Slice(edgeList, func(i, j int) bool {
		return edgeList[i].Id < edgeList[j].Id
	})

	return &api.GraphResponse{
		Revision: int(s.Revision),
		View:     api.GraphResponseViewL3,
		Nodes:    nodeList,
		Edges:    edgeList,
	}
}

// BuildL2View generates the detailed L2 view for a single Logical Switch.
func (s *Snapshot) BuildL2View(focusLS string, page int, pageSize int) (*api.GraphResponse, error) {
	if focusLS == "" {
		return nil, fmt.Errorf("focus parameter is required for l2 view (e.g. focus=ls:<uuid> or ls:<name>)")
	}

	targetUUID := strings.TrimPrefix(focusLS, PrefixLS)
	var ls *nbmodel.LogicalSwitch
	if found, ok := s.LogicalSwitches[targetUUID]; ok {
		ls = found
	} else {
		for _, candidate := range s.LogicalSwitches {
			if candidate.Name == targetUUID || candidate.Name == focusLS {
				ls = candidate
				targetUUID = candidate.UUID
				break
			}
		}
	}

	if ls == nil {
		return nil, fmt.Errorf("logical switch not found: %s", focusLS)
	}

	lsID := PrefixLS + targetUUID
	nodes := make([]api.GraphNode, 0)
	edges := make([]api.GraphEdge, 0)

	// Add LS node
	nodes = append(nodes, api.GraphNode{
		Id:         lsID,
		Type:       api.GraphNodeTypeLS,
		Name:       ls.Name,
		Properties: &map[string]interface{}{"portCount": len(ls.Ports)},
		K8s:        ExtractK8sMetadata(ls.ExternalIDs),
	})

	totalItems := len(ls.Ports)
	totalPages := (totalItems + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if startIdx > totalItems {
		startIdx = totalItems
	}
	if endIdx > totalItems {
		endIdx = totalItems
	}

	pageLSPUUIDs := ls.Ports[startIdx:endIdx]
	chassisReferenced := make(map[string]struct{})

	for _, lspUUID := range pageLSPUUIDs {
		lsp, ok := s.LogicalSwitchPorts[lspUUID]
		if !ok {
			continue
		}
		lspID := PrefixLSP + lspUUID

		boundChassis := ""
		boundState := "unbound"
		if pb, ok := s.PortBindingByPort[lsp.Name]; ok && pb.Chassis != nil && *pb.Chassis != "" {
			boundChassis = *pb.Chassis
			boundState = "bound"
			chassisReferenced[boundChassis] = struct{}{}
		}

		lspProps := map[string]interface{}{
			"type":         lsp.Type,
			"addresses":    lsp.Addresses,
			"boundState":   boundState,
			"boundChassis": boundChassis,
			"enabled":      lsp.Enabled == nil || *lsp.Enabled,
			"up":           lsp.Up != nil && *lsp.Up,
		}

		k8sMeta := s.enrichLSPK8s(lsp, boundChassis)

		nodes = append(nodes, api.GraphNode{
			Id:         lspID,
			Type:       api.GraphNodeTypeLSP,
			Name:       lsp.Name,
			Properties: &lspProps,
			K8s:        k8sMeta,
		})

		// Edge from LS contains LSP
		containsEdgeID := fmt.Sprintf("edge:%s-%s-contains", lsID, lspID)
		edges = append(edges, api.GraphEdge{
			Id:     containsEdgeID,
			Source: lsID,
			Target: lspID,
			Type:   api.Contains,
		})

		// Edge bound-on to Chassis if bound
		if boundChassis != "" {
			chassisID := PrefixChassis + boundChassis
			boundEdgeID := fmt.Sprintf("edge:%s-%s-bound-on", lspID, chassisID)
			edges = append(edges, api.GraphEdge{
				Id:     boundEdgeID,
				Source: lspID,
				Target: chassisID,
				Type:   api.BoundOn,
			})
		}
	}

	// Include referenced Chassis nodes, in a stable order (map iteration is random;
	// it made the L2 golden test flaky and reordered rows between responses).
	referenced := make([]string, 0, len(chassisReferenced))
	for name := range chassisReferenced {
		referenced = append(referenced, name)
	}
	sort.Strings(referenced)
	for _, chassisName := range referenced {
		chassisID := PrefixChassis + chassisName
		hostname := chassisName
		if ch, ok := s.Chassis[chassisName]; ok {
			hostname = ch.Hostname
		}
		nodeName := s.NodeMapper.NodeForChassis(hostname, chassisName)
		nodes = append(nodes, api.GraphNode{
			Id:   chassisID,
			Type: api.GraphNodeTypeChassis,
			Name: hostname,
			Properties: &map[string]interface{}{
				"chassis":  chassisName,
				"hostname": hostname,
				"nodeName": nodeName,
			},
		})
	}

	return &api.GraphResponse{
		Revision: int(s.Revision),
		View:     api.GraphResponseViewL2,
		Nodes:    nodes,
		Edges:    edges,
		Pagination: &api.PaginationInfo{
			Page:       page,
			PageSize:   pageSize,
			TotalItems: totalItems,
			TotalPages: totalPages,
		},
	}, nil
}

// BuildChassisView returns all chassis and their interconnections.
func (s *Snapshot) BuildChassisView(focus string, depth int) *api.GraphResponse {
	nodes := make([]api.GraphNode, 0, len(s.Chassis))

	seenChassis := make(map[string]struct{})
	for _, ch := range s.Chassis {
		if _, seen := seenChassis[ch.Name]; seen {
			continue
		}
		seenChassis[ch.Name] = struct{}{}

		chassisID := PrefixChassis + ch.Name
		nodeName, nodeMatch := s.NodeMapper.MatchChassis(ch.Hostname, ch.Name)
		hostnameShared := 0
		for _, other := range s.Chassis {
			if other.Hostname == ch.Hostname {
				hostnameShared++
			}
		}

		// Count bindings
		bindingCount := 0
		for _, pb := range s.PortBindings {
			if pb.Chassis != nil && (*pb.Chassis == ch.Name || *pb.Chassis == ch.UUID) {
				bindingCount++
			}
		}

		encapList := make([]map[string]interface{}, 0, len(ch.Encaps))
		for _, encUUID := range ch.Encaps {
			if enc, ok := s.Encaps[encUUID]; ok {
				encapList = append(encapList, map[string]interface{}{
					"type": enc.Type,
					"ip":   enc.IP,
				})
			}
		}

		props := map[string]interface{}{
			"hostname":       ch.Hostname,
			"name":           ch.Name, // the OVS system-id: the chassis identity
			"nodeName":       nodeName,
			"nodeMatch":      nodeMatch,
			"hostnameShared": hostnameShared,
			"isInterconn":    ch.OtherConfig["is-interconn"] == "true",
			"bindingCount":   bindingCount,
			"encaps":         encapList,
			"uuid":           ch.UUID,
		}

		nodes = append(nodes, api.GraphNode{
			Id:         chassisID,
			Type:       api.GraphNodeTypeChassis,
			Name:       ch.Hostname,
			Properties: &props,
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Id < nodes[j].Id
	})

	return &api.GraphResponse{
		Revision: int(s.Revision),
		View:     api.GraphResponseViewChassis,
		Nodes:    nodes,
		Edges:    []api.GraphEdge{},
	}
}

// filterGraphByFocus filters the graph to reachable nodes within `depth` hops of `focus`.
func filterGraphByFocus(nodes map[string]api.GraphNode, edges map[string]api.GraphEdge, focus string, depth int) (map[string]api.GraphNode, map[string]api.GraphEdge) {
	if _, ok := nodes[focus]; !ok {
		// Attempt prefix match or name match
		found := ""
		for id, n := range nodes {
			if n.Name == focus || strings.TrimPrefix(id, PrefixLR) == focus || strings.TrimPrefix(id, PrefixLS) == focus {
				found = id
				break
			}
		}
		if found != "" {
			focus = found
		} else {
			return nodes, edges
		}
	}

	visited := map[string]int{focus: 0}
	queue := []string{focus}

	adj := make(map[string][]string)
	for _, edge := range edges {
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
		adj[edge.Target] = append(adj[edge.Target], edge.Source)
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		d := visited[curr]
		if d >= depth {
			continue
		}
		for _, neighbor := range adj[curr] {
			if _, seen := visited[neighbor]; !seen {
				visited[neighbor] = d + 1
				queue = append(queue, neighbor)
			}
		}
	}

	filteredNodes := make(map[string]api.GraphNode)
	for id := range visited {
		if node, ok := nodes[id]; ok {
			filteredNodes[id] = node
			if node.Parent != nil {
				if parentNode, ok := nodes[*node.Parent]; ok {
					filteredNodes[*node.Parent] = parentNode
				}
			}
		}
	}

	filteredEdges := make(map[string]api.GraphEdge)
	for id, edge := range edges {
		if _, sOk := filteredNodes[edge.Source]; sOk {
			if _, tOk := filteredNodes[edge.Target]; tOk {
				filteredEdges[id] = edge
			}
		}
	}

	return filteredNodes, filteredEdges
}

func (s *Snapshot) enrichLSPK8s(lsp *nbmodel.LogicalSwitchPort, boundChassis string) *api.K8sMetadata {
	k8sMeta := ExtractK8sMetadata(lsp.ExternalIDs)
	if boundChassis != "" {
		if k8sMeta == nil {
			k8sMeta = &api.K8sMetadata{}
		}
		if k8sMeta.Node == nil {
			hostname := ""
			if ch, ok := s.Chassis[boundChassis]; ok {
				hostname = ch.Hostname
			}
			mappedNode := s.NodeMapper.NodeForChassis(hostname, boundChassis)
			if mappedNode != "" {
				k8sMeta.Node = &mappedNode
			} else if hostname != "" {
				k8sMeta.Node = &hostname
			}
		}
	}

	// Node join port resolution (node-<hostname>)
	if strings.HasPrefix(lsp.Name, "node-") {
		if k8sMeta == nil {
			k8sMeta = &api.K8sMetadata{}
		}
		nodeName := strings.TrimPrefix(lsp.Name, "node-")
		k8sMeta.Node = &nodeName
		kind := "Node"
		k8sMeta.WorkloadKind = &kind
		k8sMeta.WorkloadName = &nodeName
		owner := "Node/" + nodeName
		k8sMeta.Owner = &owner
	} else if s.WorkloadResolver != nil {
		ns := ""
		pod := ""
		if k8sMeta != nil {
			if k8sMeta.Namespace != nil {
				ns = *k8sMeta.Namespace
			}
			if k8sMeta.Pod != nil {
				pod = *k8sMeta.Pod
			}
		}
		if pod == "" {
			pod = lsp.Name
		}
		if strings.Contains(pod, "/") {
			parts := strings.SplitN(pod, "/", 2)
			if ns == "" {
				ns = parts[0]
			}
			pod = parts[1]
		}

		if wl := s.WorkloadResolver.Resolve(ns, pod); wl != nil {
			if k8sMeta == nil {
				k8sMeta = &api.K8sMetadata{}
			}
			k8sMeta.WorkloadKind = &wl.Kind
			k8sMeta.WorkloadName = &wl.WorkloadName
			k8sMeta.Owner = &wl.Owner
			if wl.VMI != "" {
				k8sMeta.Vmi = &wl.VMI
			}
			if k8sMeta.Namespace == nil && wl.Namespace != "" {
				k8sMeta.Namespace = &wl.Namespace
			}
			var lspMAC string
			var lspIPs []string
			for _, addr := range lsp.Addresses {
				parts := strings.Fields(addr)
				if len(parts) >= 1 && strings.Contains(parts[0], ":") {
					lspMAC = parts[0]
				}
				if len(parts) >= 2 {
					lspIPs = append(lspIPs, parts[1:]...)
				}
			}
			subnetName := ""
			if k8sMeta.Subnet != nil {
				subnetName = *k8sMeta.Subnet
			}
			attName := wl.MatchAttachment(lspMAC, lspIPs, subnetName)
			if attName != "" {
				k8sMeta.NetworkAttachment = &attName
			}
			if wl.Node != "" && k8sMeta.Node == nil {
				k8sMeta.Node = &wl.Node
			}
		}
	}
	return k8sMeta
}

// GetNodeDetail returns the full details for a single node.
func (s *Snapshot) GetNodeDetail(id string) (*api.NodeDetail, error) {
	var rawObj interface{}
	var extIds map[string]string
	var nodeType string
	var nodeName string
	var related []api.RelatedObject
	var detailK8s *api.K8sMetadata

	switch {
	case strings.HasPrefix(id, PrefixLR):
		uuid := strings.TrimPrefix(id, PrefixLR)
		lr, ok := s.LogicalRouters[uuid]
		if !ok {
			return nil, fmt.Errorf("logical router not found: %s", id)
		}
		rawObj = lr
		extIds = lr.ExternalIDs
		nodeType = "LR"
		nodeName = lr.Name

		// Attached switches
		for _, lsUUID := range s.AttachedSwitchesByLR[uuid] {
			lsName := lsUUID
			if ls, ok := s.LogicalSwitches[lsUUID]; ok {
				lsName = ls.Name
			}
			related = append(related, api.RelatedObject{
				Id:           PrefixLS + lsUUID,
				Type:         "LS",
				Relationship: "attached-switch",
				Name:         &lsName,
			})
		}
		// Ports
		for _, pUUID := range lr.Ports {
			pName := pUUID
			if p, ok := s.LogicalRouterPorts[pUUID]; ok {
				pName = p.Name
			}
			related = append(related, api.RelatedObject{
				Id:           PrefixLRP + pUUID,
				Type:         "LRP",
				Relationship: "port",
				Name:         &pName,
			})
		}
		// NATs
		for _, natUUID := range lr.Nat {
			related = append(related, api.RelatedObject{
				Id:           PrefixNAT + natUUID,
				Type:         "NAT",
				Relationship: "nat",
			})
		}

	case strings.HasPrefix(id, PrefixLS):
		uuid := strings.TrimPrefix(id, PrefixLS)
		ls, ok := s.LogicalSwitches[uuid]
		if !ok {
			return nil, fmt.Errorf("logical switch not found: %s", id)
		}
		rawObj = ls
		extIds = ls.ExternalIDs
		nodeType = "LS"
		nodeName = ls.Name

		// Attached routers
		for _, lrUUID := range s.AttachedRoutersByLS[uuid] {
			lrName := lrUUID
			if lr, ok := s.LogicalRouters[lrUUID]; ok {
				lrName = lr.Name
			}
			related = append(related, api.RelatedObject{
				Id:           PrefixLR + lrUUID,
				Type:         "LR",
				Relationship: "attached-router",
				Name:         &lrName,
			})
		}
		// Ports
		for _, pUUID := range ls.Ports {
			pName := pUUID
			if p, ok := s.LogicalSwitchPorts[pUUID]; ok {
				pName = p.Name
			}
			related = append(related, api.RelatedObject{
				Id:           PrefixLSP + pUUID,
				Type:         "LSP",
				Relationship: "port",
				Name:         &pName,
			})
		}

	case strings.HasPrefix(id, PrefixLSP):
		uuid := strings.TrimPrefix(id, PrefixLSP)
		lsp, ok := s.LogicalSwitchPorts[uuid]
		if !ok {
			return nil, fmt.Errorf("logical switch port not found: %s", id)
		}
		rawObj = lsp
		extIds = lsp.ExternalIDs
		nodeType = "LSP"
		nodeName = lsp.Name

		boundChassis := ""
		if pb, ok := s.PortBindingByPort[lsp.Name]; ok && pb.Chassis != nil {
			boundChassis = *pb.Chassis
			chassisID := PrefixChassis + *pb.Chassis
			related = append(related, api.RelatedObject{
				Id:           chassisID,
				Type:         "Chassis",
				Relationship: "bound-on",
				Name:         pb.Chassis,
			})
		}
		detailK8s = s.enrichLSPK8s(lsp, boundChassis)

	case strings.HasPrefix(id, PrefixLRP):
		uuid := strings.TrimPrefix(id, PrefixLRP)
		lrp, ok := s.LogicalRouterPorts[uuid]
		if !ok {
			return nil, fmt.Errorf("logical router port not found: %s", id)
		}
		rawObj = lrp
		extIds = lrp.ExternalIDs
		nodeType = "LRP"
		nodeName = lrp.Name

		if crPB, ok := s.CRPortBindingByLRP[lrp.Name]; ok && crPB.Chassis != nil {
			chassisID := PrefixChassis + *crPB.Chassis
			gwName := s.resolveChassisName(*crPB.Chassis)
			related = append(related, api.RelatedObject{
				Id:           chassisID,
				Type:         "Chassis",
				Relationship: "active-gateway",
				Name:         &gwName,
			})
		}

	case strings.HasPrefix(id, PrefixChassis):
		chassisName := strings.TrimPrefix(id, PrefixChassis)
		ch, ok := s.Chassis[chassisName]
		if !ok {
			// Try finding by name/hostname
			for _, candidate := range s.Chassis {
				if candidate.Name == chassisName || candidate.Hostname == chassisName {
					ch = candidate
					break
				}
			}
		}
		if ch == nil {
			return nil, fmt.Errorf("chassis not found: %s", id)
		}
		rawObj = ch
		extIds = ch.ExternalIDs
		nodeType = "Chassis"
		nodeName = ch.Hostname

		// Find bound ports
		for _, pb := range s.PortBindings {
			if pb.Chassis != nil && *pb.Chassis == ch.Name {
				name := pb.LogicalPort
				related = append(related, api.RelatedObject{
					Id:           PrefixLSP + pb.LogicalPort,
					Type:         "PortBinding",
					Relationship: "bound-port",
					Name:         &name,
				})
			}
		}

	default:
		return nil, fmt.Errorf("unsupported node id prefix: %s", id)
	}

	rawMap := toMap(rawObj)

	if detailK8s == nil {
		detailK8s = ExtractK8sMetadata(extIds)
	}

	detail := &api.NodeDetail{
		Id:          id,
		Type:        nodeType,
		Name:        nodeName,
		Raw:         rawMap,
		ExternalIds: extIds,
		K8s:         detailK8s,
		Related:     &related,
	}

	return detail, nil
}

// GetChassisList returns full status for all chassis.
func (s *Snapshot) GetChassisList() *api.ChassisList {
	items := make([]api.ChassisDetail, 0, len(s.Chassis))

	for _, ch := range s.Chassis {
		nodeName := s.NodeMapper.NodeForChassis(ch.Hostname, ch.Name)

		encaps := make([]api.EncapDetail, 0, len(ch.Encaps))
		for _, encUUID := range ch.Encaps {
			if enc, ok := s.Encaps[encUUID]; ok {
				encaps = append(encaps, api.EncapDetail{
					Type:    enc.Type,
					Ip:      enc.IP,
					Options: &enc.Options,
				})
			}
		}

		boundPorts := make([]string, 0)
		for _, pb := range s.PortBindings {
			if pb.Chassis != nil && *pb.Chassis == ch.Name {
				boundPorts = append(boundPorts, pb.LogicalPort)
			}
		}
		sort.Strings(boundPorts)

		gatewayRoles := make([]api.GatewayRole, 0)
		for _, lrp := range s.LogicalRouterPorts {
			for _, gwUUID := range lrp.GatewayChassis {
				gw, ok := s.GatewayChassis[gwUUID]
				if !ok || gw.ChassisName != ch.Name {
					continue
				}
				isActive := false
				if crPB, ok := s.CRPortBindingByLRP[lrp.Name]; ok && crPB.Chassis != nil {
					if *crPB.Chassis == ch.Name || *crPB.Chassis == ch.UUID {
						isActive = true
					}
				}
				routerName := ""
				for lrUUID, ports := range s.LRPsByRouter {
					for _, pUUID := range ports {
						if pUUID == lrp.UUID {
							if lr, ok := s.LogicalRouters[lrUUID]; ok {
								routerName = lr.Name
							}
							break
						}
					}
				}
				gatewayRoles = append(gatewayRoles, api.GatewayRole{
					LrpName:    lrp.Name,
					RouterName: routerName,
					Priority:   gw.Priority,
					Active:     isActive,
				})
			}
		}

		haGroups := make([]api.HAGroupRole, 0)
		for _, hag := range s.HAChassisGroups {
			for _, hacUUID := range hag.HaChassis {
				hac, ok := s.HAChassis[hacUUID]
				if !ok || hac.ChassisName != ch.Name {
					continue
				}
				haGroups = append(haGroups, api.HAGroupRole{
					GroupName: hag.Name,
					Priority:  hac.Priority,
				})
			}
		}

		items = append(items, api.ChassisDetail{
			Name:         ch.Name,
			Hostname:     ch.Hostname,
			NodeName:     &nodeName,
			Encaps:       encaps,
			BindingCount: len(boundPorts),
			BoundPorts:   &boundPorts,
			GatewayRoles: gatewayRoles,
			HaGroups:     haGroups,
			OtherConfig:  &ch.OtherConfig,
			ExternalIds:  &ch.ExternalIDs,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Hostname < items[j].Hostname
	})

	return &api.ChassisList{
		Items: items,
	}
}

func (s *Snapshot) resolveChassisName(chassisID string) string {
	if chassisID == "" {
		return ""
	}
	name := chassisID
	if ch, ok := s.Chassis[chassisID]; ok {
		name = ch.Hostname
	} else {
		for _, ch := range s.Chassis {
			if ch.UUID == chassisID {
				name = ch.Hostname
				break
			}
		}
	}
	if s.NodeMapper != nil {
		if node := s.NodeMapper.NodeForChassis(name, chassisID); node != "" {
			return node
		}
	}
	return name
}

// toMap returns an OVSDB model row keyed by its OVSDB column names (the
// `ovsdb:"…"` struct tags, e.g. _uuid, other_config), as NodeDetail.raw promises,
// with JSON-plain values. Values that are not tagged structs keep their JSON keys.
func toMap(v interface{}) map[string]interface{} {
	res := make(map[string]interface{})
	if v == nil {
		return res
	}
	src := v
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() == reflect.Struct {
		cols := make(map[string]interface{})
		for i := 0; i < rv.NumField(); i++ {
			if col := strings.Split(rv.Type().Field(i).Tag.Get("ovsdb"), ",")[0]; col != "" {
				cols[col] = rv.Field(i).Interface()
			}
		}
		if len(cols) > 0 {
			src = cols
		}
	}
	data, err := json.Marshal(src)
	if err != nil {
		return res
	}
	_ = json.Unmarshal(data, &res)
	if res == nil {
		res = make(map[string]interface{})
	}
	return res
}
