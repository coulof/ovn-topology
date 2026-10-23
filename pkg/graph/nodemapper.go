package graph

import (
	"context"
	"log/slog"
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const OvnChassisAnnotation = "ovn.kubernetes.io/chassis"

// KubeNodeMapper dynamically maps OVN chassis names to Kubernetes Node names.
type KubeNodeMapper struct {
	client kubernetes.Interface
	logger *slog.Logger

	mu             sync.RWMutex
	chassisToNode  map[string]string
	hostnameToNode map[string]string
}

// NewKubeNodeMapper creates a new KubeNodeMapper.
func NewKubeNodeMapper(client kubernetes.Interface, logger *slog.Logger) *KubeNodeMapper {
	if logger == nil {
		logger = slog.Default()
	}
	return &KubeNodeMapper{
		client:         client,
		logger:         logger,
		chassisToNode:  make(map[string]string),
		hostnameToNode: make(map[string]string),
	}
}

// NodeForChassis resolves a chassis name and hostname to a Kubernetes node name.
func (m *KubeNodeMapper) NodeForChassis(hostname string, chassisID string) string {
	node, _ := m.MatchChassis(hostname, chassisID)
	return node
}

// MatchChassis resolves the node and reports which rule matched. The node annotation
// (system-id) is authoritative; the hostname is informational in OVN and only a fallback.
func (m *KubeNodeMapper) MatchChassis(hostname string, chassisID string) (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if node, ok := m.chassisToNode[chassisID]; ok && node != "" {
		return node, "annotation"
	}
	if node, ok := m.hostnameToNode[hostname]; ok && node != "" {
		return node, "hostname"
	}
	if hostname != "" {
		return hostname, "fallback-hostname"
	}
	return chassisID, "fallback-name"
}

// Sync reads all Kubernetes nodes and updates the internal mapping table.
func (m *KubeNodeMapper) Sync(ctx context.Context) error {
	if m.client == nil {
		return nil
	}

	nodes, err := m.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	ptrs := make([]*corev1.Node, len(nodes.Items))
	for i := range nodes.Items {
		ptrs[i] = &nodes.Items[i]
	}
	m.SetNodes(ptrs)
	return nil
}

// SetNodes replaces the mapping tables from a full node list and reports whether
// the chassis → node mapping changed.
func (m *KubeNodeMapper) SetNodes(nodes []*corev1.Node) bool {
	chassisToNode := make(map[string]string, len(nodes))
	hostnameToNode := make(map[string]string, len(nodes))
	for _, node := range nodes {
		hostnameToNode[node.Name] = node.Name
		if chassisID, ok := node.Annotations[OvnChassisAnnotation]; ok && chassisID != "" {
			chassisToNode[chassisID] = node.Name
		}
		// Also check addresses for hostnames
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeHostName && addr.Address != "" {
				hostnameToNode[addr.Address] = node.Name
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	changed := !maps.Equal(m.chassisToNode, chassisToNode) || !maps.Equal(m.hostnameToNode, hostnameToNode)
	m.chassisToNode, m.hostnameToNode = chassisToNode, hostnameToNode
	m.logger.Debug("synchronized kubernetes node chassis mappings", "node_count", len(nodes), "changed", changed)
	return changed
}
