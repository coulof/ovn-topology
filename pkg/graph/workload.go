package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NetworkAttachmentStatus represents an interface attachment entry from Multus network-status.
type NetworkAttachmentStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Mac       string   `json:"mac,omitempty"`
	Default   bool     `json:"default,omitempty"`
}

// WorkloadInfo represents enriched Kubernetes workload identity for an LSP.
type WorkloadInfo struct {
	Kind               string // "VirtualMachine", "Pod", "Deployment", "DaemonSet", "StatefulSet"
	WorkloadName       string
	PodName            string
	Namespace          string
	Owner              string // e.g. "VirtualMachine/vm-onewater", "Deployment/coredns"
	VMI                string // VirtualMachineInstance name for KubeVirt
	NetworkAttachment  string // NetworkAttachmentDefinition for multi-NIC interfaces
	Attachments        []NetworkAttachmentStatus
	Node               string
}

// WorkloadResolver resolves Pod and KubeVirt VM metadata.
type WorkloadResolver interface {
	Resolve(namespace, podOrVM string) *WorkloadInfo
	Sync(ctx context.Context) error
}

// StaticWorkloadResolver is a mock implementation for tests and offline fixtures.
type StaticWorkloadResolver struct {
	mu        sync.RWMutex
	workloads map[string]*WorkloadInfo // Keyed by "namespace/name" or "name"
}

// NewStaticWorkloadResolver creates an empty StaticWorkloadResolver.
func NewStaticWorkloadResolver() *StaticWorkloadResolver {
	return &StaticWorkloadResolver{
		workloads: make(map[string]*WorkloadInfo),
	}
}

// Add registers a static workload entry.
func (r *StaticWorkloadResolver) Add(info WorkloadInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s/%s", info.Namespace, info.PodName)
	r.workloads[key] = &info
	r.workloads[info.PodName] = &info
	if info.WorkloadName != "" {
		vmKey := fmt.Sprintf("%s/%s", info.Namespace, info.WorkloadName)
		r.workloads[vmKey] = &info
		r.workloads[info.WorkloadName] = &info
	}
}

// Resolve looks up workload info.
func (r *StaticWorkloadResolver) Resolve(namespace, podOrVM string) *WorkloadInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", namespace, podOrVM)
	if info, ok := r.workloads[key]; ok {
		return info
	}
	if info, ok := r.workloads[podOrVM]; ok {
		return info
	}
	return nil
}

// Sync is a no-op for StaticWorkloadResolver.
func (r *StaticWorkloadResolver) Sync(ctx context.Context) error {
	return nil
}

// KubeWorkloadResolver reads live Pods from the Kubernetes API to resolve workload identities.
type KubeWorkloadResolver struct {
	client kubernetes.Interface
	logger *slog.Logger

	mu        sync.RWMutex
	workloads map[string]*WorkloadInfo
}

// NewKubeWorkloadResolver creates a new KubeWorkloadResolver.
func NewKubeWorkloadResolver(client kubernetes.Interface, logger *slog.Logger) *KubeWorkloadResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &KubeWorkloadResolver{
		client:    client,
		logger:    logger,
		workloads: make(map[string]*WorkloadInfo),
	}
}

// Resolve looks up a pod or VM by namespace and name.
func (r *KubeWorkloadResolver) Resolve(namespace, podOrVM string) *WorkloadInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", namespace, podOrVM)
	if info, ok := r.workloads[key]; ok {
		return info
	}
	if info, ok := r.workloads[podOrVM]; ok {
		return info
	}
	return nil
}

// Sync scans all Kubernetes pods and builds the workload identity index.
func (r *KubeWorkloadResolver) Sync(ctx context.Context) error {
	if r.client == nil {
		return nil
	}

	pods, err := r.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods for workload resolution: %w", err)
	}

	ptrs := make([]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		ptrs[i] = &pods.Items[i]
	}
	r.SetPods(ptrs)
	return nil
}

// SetPods replaces the workload index from a full pod list and reports whether any
// workload identity changed.
func (r *KubeWorkloadResolver) SetPods(pods []*corev1.Pod) bool {
	workloads := make(map[string]*WorkloadInfo, len(pods)*2)
	for _, pod := range pods {
		info := extractWorkloadFromPod(pod)

		key := fmt.Sprintf("%s/%s", info.Namespace, info.PodName)
		workloads[key] = info
		workloads[info.PodName] = info
		if info.WorkloadName != "" {
			vmKey := fmt.Sprintf("%s/%s", info.Namespace, info.WorkloadName)
			workloads[vmKey] = info
			workloads[info.WorkloadName] = info
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !maps.EqualFunc(r.workloads, workloads, equalWorkloadInfo)
	r.workloads = workloads
	return changed
}

func equalWorkloadInfo(a, b *WorkloadInfo) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Kind != b.Kind || a.WorkloadName != b.WorkloadName || a.PodName != b.PodName ||
		a.Namespace != b.Namespace || a.Owner != b.Owner || a.VMI != b.VMI ||
		a.NetworkAttachment != b.NetworkAttachment || a.Node != b.Node {
		return false
	}
	if len(a.Attachments) != len(b.Attachments) {
		return false
	}
	for i := range a.Attachments {
		if a.Attachments[i].Name != b.Attachments[i].Name ||
			a.Attachments[i].Mac != b.Attachments[i].Mac ||
			a.Attachments[i].Default != b.Attachments[i].Default {
			return false
		}
	}
	return true
}

func extractWorkloadFromPod(pod *corev1.Pod) *WorkloadInfo {
	info := &WorkloadInfo{
		PodName:   pod.Name,
		Namespace: pod.Namespace,
		Node:      pod.Spec.NodeName,
	}

	// 1. Check for KubeVirt VM
	if pod.Labels["kubevirt.io"] == "virt-launcher" || pod.Labels["vm.kubevirt.io/name"] != "" {
		info.Kind = "VirtualMachine"
		vmName := pod.Labels["vm.kubevirt.io/name"]
		if vmName == "" {
			vmName = pod.Labels["harvesterhci.io/vmName"]
		}
		if vmName == "" {
			vmName = strings.TrimPrefix(pod.Name, "virt-launcher-")
			if idx := strings.LastIndex(vmName, "-"); idx > 0 {
				vmName = vmName[:idx] // strip random suffix
			}
		}
		info.WorkloadName = vmName
		info.VMI = pod.Labels["vmi.kubevirt.io/id"]
		if info.VMI == "" {
			info.VMI = vmName
		}
		info.Owner = fmt.Sprintf("VirtualMachine/%s", vmName)
	} else if len(pod.OwnerReferences) > 0 {
		// 2. Check OwnerReferences
		for _, owner := range pod.OwnerReferences {
			switch owner.Kind {
			case "VirtualMachineInstance":
				info.Kind = "VirtualMachine"
				info.WorkloadName = owner.Name
				info.VMI = owner.Name
				info.Owner = fmt.Sprintf("VirtualMachine/%s", owner.Name)
			case "ReplicaSet":
				// Strip template hash to get Deployment name: e.g. coredns-5596665f84 -> coredns
				depName := owner.Name
				if idx := strings.LastIndex(owner.Name, "-"); idx > 0 {
					depName = owner.Name[:idx]
				}
				info.Kind = "Deployment"
				info.WorkloadName = depName
				info.Owner = fmt.Sprintf("Deployment/%s", depName)
			case "DaemonSet", "StatefulSet", "Job":
				info.Kind = owner.Kind
				info.WorkloadName = owner.Name
				info.Owner = fmt.Sprintf("%s/%s", owner.Kind, owner.Name)
			}
			if info.Kind != "" {
				break
			}
		}
	}

	// 3. Fallback: standalone Pod
	if info.Kind == "" {
		info.Kind = "Pod"
		info.WorkloadName = pod.Name
		info.Owner = fmt.Sprintf("Pod/%s", pod.Name)
	}

	// Check multi-NIC NetworkAttachmentDefinition
	if netStatus, ok := pod.Annotations["k8s.v1.cni.cncf.io/network-status"]; ok && netStatus != "" {
		var attachments []NetworkAttachmentStatus
		if err := json.Unmarshal([]byte(netStatus), &attachments); err == nil && len(attachments) > 0 {
			info.Attachments = attachments
			for _, att := range attachments {
				if !att.Default && att.Name != "" {
					info.NetworkAttachment = att.Name
					break
				}
			}
			if info.NetworkAttachment == "" && len(attachments) > 0 {
				info.NetworkAttachment = attachments[0].Name
			}
		} else {
			info.NetworkAttachment = netStatus
		}
	}

	return info
}

// MatchAttachment finds the matching network attachment name for an LSP based on MAC, IPs, or subnet.
func (w *WorkloadInfo) MatchAttachment(mac string, ips []string, subnet string) string {
	if w == nil {
		return ""
	}
	if len(w.Attachments) == 0 {
		return w.NetworkAttachment
	}

	// 1. Match by MAC address (case-insensitive)
	if mac != "" {
		macLower := strings.ToLower(mac)
		for _, att := range w.Attachments {
			if strings.ToLower(att.Mac) == macLower {
				return att.Name
			}
		}
	}

	// 2. Match by IP address
	for _, ip := range ips {
		for _, att := range w.Attachments {
			for _, attIP := range att.IPs {
				if attIP == ip {
					return att.Name
				}
			}
		}
	}

	// 3. Match by subnet/network name suffix (e.g. subnet "subnet-onewater" matches attachment "onewater/onewater")
	if subnet != "" {
		subClean := strings.TrimPrefix(subnet, "subnet-")
		for _, att := range w.Attachments {
			attClean := att.Name
			if idx := strings.Index(attClean, "/"); idx >= 0 {
				attClean = attClean[idx+1:]
			}
			if attClean == subClean || att.Name == subnet {
				return att.Name
			}
		}
	}

	return w.NetworkAttachment
}
