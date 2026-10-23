package graph

import (
	"strings"
	"time"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
	"github.com/coulof/ovn-topology/pkg/ovsdb/sbmodel"
)

// NodeMapper maps a Chassis hostname/name to a Kubernetes node name.
type NodeMapper interface {
	NodeForChassis(hostname string, chassisID string) string
	// MatchChassis also reports how the node was found: "annotation" (node annotation
	// ovn.kubernetes.io/chassis == Chassis.name, the authoritative link), "override",
	// "hostname" (a Kubernetes node matches Chassis.hostname) or "fallback-hostname" /
	// "fallback-name" (no Kubernetes match; the chassis' own value is shown).
	MatchChassis(hostname string, chassisID string) (node string, how string)
}

// StaticNodeMapper is a simple NodeMapper returning hostname or configured overrides.
type StaticNodeMapper struct {
	Overrides map[string]string
}

// NodeForChassis returns mapped node or hostname as fallback.
func (s *StaticNodeMapper) NodeForChassis(hostname string, chassisID string) string {
	node, _ := s.MatchChassis(hostname, chassisID)
	return node
}

// MatchChassis returns the override for the chassis (by name, then hostname), else its hostname.
func (s *StaticNodeMapper) MatchChassis(hostname string, chassisID string) (string, string) {
	if s.Overrides != nil {
		if node, ok := s.Overrides[chassisID]; ok && node != "" {
			return node, "override"
		}
		if node, ok := s.Overrides[hostname]; ok && node != "" {
			return node, "override"
		}
	}
	return hostname, "fallback-hostname"
}

// Snapshot is an immutable point-in-time state of the joined OVN NB and SB databases.
type Snapshot struct {
	Revision   int64
	Timestamp  time.Time
	NodeMapper NodeMapper
	WorkloadResolver WorkloadResolver

	// Northbound entities indexed by UUID
	LogicalRouters      map[string]*nbmodel.LogicalRouter
	LogicalRouterPorts map[string]*nbmodel.LogicalRouterPort
	StaticRoutes        map[string]*nbmodel.LogicalRouterStaticRoute
	Policies            map[string]*nbmodel.LogicalRouterPolicy
	NATs                map[string]*nbmodel.NAT
	GatewayChassis      map[string]*nbmodel.GatewayChassis
	HAChassisGroups     map[string]*nbmodel.HAChassisGroup
	HAChassis           map[string]*nbmodel.HAChassis
	LogicalSwitches     map[string]*nbmodel.LogicalSwitch
	LogicalSwitchPorts  map[string]*nbmodel.LogicalSwitchPort
	LoadBalancers       map[string]*nbmodel.LoadBalancer

	// Southbound entities indexed by UUID / name
	Chassis         map[string]*sbmodel.Chassis
	Encaps          map[string]*sbmodel.Encap
	PortBindings     map[string]*sbmodel.PortBinding     // Keyed by UUID
	DatapathBindings map[string]*sbmodel.DatapathBinding // Keyed by UUID
	BFDs             map[string]*sbmodel.BFD             // Keyed by UUID

	// Secondary indices for O(1) joins
	LRPByName            map[string]*nbmodel.LogicalRouterPort
	LSPByName            map[string]*nbmodel.LogicalSwitchPort
	PortBindingByPort    map[string]*sbmodel.PortBinding // Keyed by logical_port name
	CRPortBindingByLRP   map[string]*sbmodel.PortBinding // Keyed by LRP name (cr-<lrp>)
	LSPsBySwitch         map[string][]string             // Switch UUID -> LSP UUIDs
	LRPsByRouter         map[string][]string             // Router UUID -> LRP UUIDs
	AttachedSwitchesByLR map[string][]string             // LR UUID -> LS UUIDs
	AttachedRoutersByLS  map[string][]string             // LS UUID -> LR UUIDs
}

// NewEmptySnapshot initializes an empty Snapshot with all maps allocated.
func NewEmptySnapshot(revision int64, mapper NodeMapper) *Snapshot {
	if mapper == nil {
		mapper = &StaticNodeMapper{}
	}
	return &Snapshot{
		Revision:             revision,
		Timestamp:            time.Now(),
		NodeMapper:           mapper,
		WorkloadResolver:     &StaticWorkloadResolver{},
		LogicalRouters:       make(map[string]*nbmodel.LogicalRouter),
		LogicalRouterPorts:   make(map[string]*nbmodel.LogicalRouterPort),
		StaticRoutes:         make(map[string]*nbmodel.LogicalRouterStaticRoute),
		Policies:             make(map[string]*nbmodel.LogicalRouterPolicy),
		NATs:                 make(map[string]*nbmodel.NAT),
		GatewayChassis:       make(map[string]*nbmodel.GatewayChassis),
		HAChassisGroups:      make(map[string]*nbmodel.HAChassisGroup),
		HAChassis:            make(map[string]*nbmodel.HAChassis),
		LogicalSwitches:      make(map[string]*nbmodel.LogicalSwitch),
		LogicalSwitchPorts:   make(map[string]*nbmodel.LogicalSwitchPort),
		LoadBalancers:        make(map[string]*nbmodel.LoadBalancer),
		Chassis:              make(map[string]*sbmodel.Chassis),
		Encaps:               make(map[string]*sbmodel.Encap),
		PortBindings:         make(map[string]*sbmodel.PortBinding),
		DatapathBindings:     make(map[string]*sbmodel.DatapathBinding),
		BFDs:                 make(map[string]*sbmodel.BFD),
		LRPByName:            make(map[string]*nbmodel.LogicalRouterPort),
		LSPByName:            make(map[string]*nbmodel.LogicalSwitchPort),
		PortBindingByPort:    make(map[string]*sbmodel.PortBinding),
		CRPortBindingByLRP:   make(map[string]*sbmodel.PortBinding),
		LSPsBySwitch:         make(map[string][]string),
		LRPsByRouter:         make(map[string][]string),
		AttachedSwitchesByLR: make(map[string][]string),
		AttachedRoutersByLS:  make(map[string][]string),
	}
}

// Clone creates a shallow copy of maps for copy-on-write modifications.
func (s *Snapshot) Clone(newRevision int64) *Snapshot {
	next := NewEmptySnapshot(newRevision, s.NodeMapper)
	next.WorkloadResolver = s.WorkloadResolver

	for k, v := range s.LogicalRouters {
		next.LogicalRouters[k] = v
	}
	for k, v := range s.LogicalRouterPorts {
		next.LogicalRouterPorts[k] = v
	}
	for k, v := range s.StaticRoutes {
		next.StaticRoutes[k] = v
	}
	for k, v := range s.Policies {
		next.Policies[k] = v
	}
	for k, v := range s.NATs {
		next.NATs[k] = v
	}
	for k, v := range s.GatewayChassis {
		next.GatewayChassis[k] = v
	}
	for k, v := range s.HAChassisGroups {
		next.HAChassisGroups[k] = v
	}
	for k, v := range s.HAChassis {
		next.HAChassis[k] = v
	}
	for k, v := range s.LogicalSwitches {
		next.LogicalSwitches[k] = v
	}
	for k, v := range s.LogicalSwitchPorts {
		next.LogicalSwitchPorts[k] = v
	}
	for k, v := range s.LoadBalancers {
		next.LoadBalancers[k] = v
	}
	for k, v := range s.Chassis {
		next.Chassis[k] = v
	}
	for k, v := range s.Encaps {
		next.Encaps[k] = v
	}
	for k, v := range s.PortBindings {
		next.PortBindings[k] = v
	}
	for k, v := range s.DatapathBindings {
		next.DatapathBindings[k] = v
	}
	for k, v := range s.BFDs {
		next.BFDs[k] = v
	}

	return next
}

// Typed ID format helpers
const (
	PrefixLR       = "lr:"
	PrefixLS       = "ls:"
	PrefixLRP      = "lrp:"
	PrefixLSP      = "lsp:"
	PrefixChassis  = "chassis:"
	PrefixNAT      = "nat:"
	PrefixRoute    = "route:"
	PrefixPolicy   = "policy:"
	PrefixLB       = "lb:"
	PrefixHAGroup  = "hagroup:"
	PrefixExternal = "ext:"
	PrefixVPC      = "vpc:"
)

// ExtractK8sMetadata extracts standard Kubernetes annotations and labels from NB external_ids.
func ExtractK8sMetadata(extIds map[string]string) *api.K8sMetadata {
	if len(extIds) == 0 {
		return nil
	}
	k8s := &api.K8sMetadata{}
	hasAny := false

	if ns, ok := extIds["namespace"]; ok && ns != "" {
		k8s.Namespace = &ns
		hasAny = true
	} else if ns, ok := extIds["k8s.ovn.org/namespace"]; ok && ns != "" {
		k8s.Namespace = &ns
		hasAny = true
	}

	if pod, ok := extIds["pod"]; ok && pod != "" {
		k8s.Pod = &pod
		hasAny = true
	} else if pod, ok := extIds["k8s.ovn.org/pod"]; ok && pod != "" {
		k8s.Pod = &pod
		hasAny = true
	}

	// Handle "namespace/pod" format commonly set by Kube-OVN in the "pod" field
	if k8s.Namespace == nil && k8s.Pod != nil && strings.Contains(*k8s.Pod, "/") {
		parts := strings.SplitN(*k8s.Pod, "/", 2)
		ns := parts[0]
		k8s.Namespace = &ns
		pod := parts[1]
		k8s.Pod = &pod
	}

	if node, ok := extIds["node"]; ok && node != "" {
		k8s.Node = &node
		hasAny = true
	}

	if subnet, ok := extIds["ls"]; ok && subnet != "" {
		k8s.Subnet = &subnet
		hasAny = true
	} else if subnet, ok := extIds["subnet"]; ok && subnet != "" {
		k8s.Subnet = &subnet
		hasAny = true
	}

	if vpc, ok := extIds["vpc"]; ok && vpc != "" {
		k8s.Vpc = &vpc
		hasAny = true
	}

	if vendor, ok := extIds["vendor"]; ok && vendor != "" {
		k8s.Vendor = &vendor
		hasAny = true
	}

	if !hasAny {
		return nil
	}
	return k8s
}
