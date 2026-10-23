package graph

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkloadResolver(t *testing.T) {
	ctx := context.Background()

	fakeClient := fake.NewSimpleClientset(
		// 1. KubeVirt VM launcher pod
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "virt-launcher-vm-onewater-abcde",
				Namespace: "onewater",
				Labels: map[string]string{
					"kubevirt.io":         "virt-launcher",
					"vm.kubevirt.io/name": "vm-onewater",
					"vmi.kubevirt.io/id":  "vm-onewater",
				},
			},
			Spec: corev1.PodSpec{NodeName: "node1"},
		},
		// 2. Deployment pod via ReplicaSet
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "coredns-7c65d6cfc9-xyz12",
				Namespace: "kube-system",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "coredns-7c65d6cfc9",
					},
				},
			},
			Spec: corev1.PodSpec{NodeName: "node1"},
		},
		// 3. DaemonSet pod
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-ovn-cni-node1",
				Namespace: "kube-system",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "DaemonSet",
						Name:       "kube-ovn-cni",
					},
				},
			},
			Spec: corev1.PodSpec{NodeName: "node1"},
		},
		// 4. Standalone pod
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "standalone-test",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{NodeName: "node2"},
		},
	)

	resolver := NewKubeWorkloadResolver(fakeClient, nil)
	if err := resolver.Sync(ctx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// 1. Resolve VM
	vmInfo := resolver.Resolve("onewater", "virt-launcher-vm-onewater-abcde")
	if vmInfo == nil {
		t.Fatalf("expected VM workload info, got nil")
	}
	if vmInfo.Kind != "VirtualMachine" || vmInfo.WorkloadName != "vm-onewater" {
		t.Errorf("unexpected VM info: %+v", vmInfo)
	}
	if vmInfo.Owner != "VirtualMachine/vm-onewater" {
		t.Errorf("unexpected VM owner: %s", vmInfo.Owner)
	}

	// Also resolve VM by vmName
	vmByName := resolver.Resolve("onewater", "vm-onewater")
	if vmByName == nil || vmByName.Kind != "VirtualMachine" {
		t.Errorf("expected resolution by VM name, got %+v", vmByName)
	}

	// 2. Resolve Deployment
	depInfo := resolver.Resolve("kube-system", "coredns-7c65d6cfc9-xyz12")
	if depInfo == nil {
		t.Fatalf("expected Deployment workload info, got nil")
	}
	if depInfo.Kind != "Deployment" || depInfo.WorkloadName != "coredns" {
		t.Errorf("unexpected Deployment info: %+v", depInfo)
	}
	if depInfo.Owner != "Deployment/coredns" {
		t.Errorf("unexpected Deployment owner: %s", depInfo.Owner)
	}

	// 3. Resolve DaemonSet
	dsInfo := resolver.Resolve("kube-system", "kube-ovn-cni-node1")
	if dsInfo == nil || dsInfo.Kind != "DaemonSet" || dsInfo.WorkloadName != "kube-ovn-cni" {
		t.Errorf("unexpected DaemonSet info: %+v", dsInfo)
	}

	// 4. Resolve Standalone Pod
	podInfo := resolver.Resolve("default", "standalone-test")
	if podInfo == nil || podInfo.Kind != "Pod" || podInfo.WorkloadName != "standalone-test" {
		t.Errorf("unexpected Standalone Pod info: %+v", podInfo)
	}
}

func TestMultiNICWorkloadResolution(t *testing.T) {
	ctx := context.Background()

	netStatus := `[
		{"name": "k8s-pod-network", "interface": "eth0", "ips": ["10.52.0.98"], "mac": "c6:b0:7e:fd:ab:1b", "default": true},
		{"name": "onewater/onewater", "interface": "pod37a8eec1ce1", "ips": ["192.168.54.37"], "mac": "32:36:ab:92:a9:0a"},
		{"name": "twowater/twowater", "interface": "pod8fe0d3f1ac5", "ips": ["192.168.55.51"], "mac": "be:91:0e:cd:33:71"}
	]`

	fakeClient := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "virt-launcher-test-2-nics-2-vpc-k827k",
				Namespace: "default",
				Labels: map[string]string{
					"kubevirt.io":            "virt-launcher",
					"harvesterhci.io/vmName": "test-2-nics-2-vpc",
					"vm.kubevirt.io/name":    "test-2-nics-2-vpc",
					"vmi.kubevirt.io/id":     "test-2-nics-2-vpc",
				},
				Annotations: map[string]string{
					"k8s.v1.cni.cncf.io/network-status": netStatus,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "kubevirt.io/v1",
						Kind:       "VirtualMachineInstance",
						Name:       "test-2-nics-2-vpc",
					},
				},
			},
			Spec: corev1.PodSpec{NodeName: "hv-01"},
		},
	)

	resolver := NewKubeWorkloadResolver(fakeClient, nil)
	if err := resolver.Sync(ctx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	info := resolver.Resolve("default", "test-2-nics-2-vpc")
	if info == nil {
		t.Fatalf("expected workload info for test-2-nics-2-vpc, got nil")
	}
	if info.Kind != "VirtualMachine" || info.WorkloadName != "test-2-nics-2-vpc" {
		t.Errorf("expected VirtualMachine/test-2-nics-2-vpc, got kind=%s name=%s", info.Kind, info.WorkloadName)
	}
	if info.Owner != "VirtualMachine/test-2-nics-2-vpc" {
		t.Errorf("expected owner VirtualMachine/test-2-nics-2-vpc, got %s", info.Owner)
	}
	if len(info.Attachments) != 3 {
		t.Fatalf("expected 3 attachments, got %d", len(info.Attachments))
	}

	// Test MatchAttachment by MAC
	att1 := info.MatchAttachment("32:36:ab:92:a9:0a", nil, "")
	if att1 != "onewater/onewater" {
		t.Errorf("expected onewater/onewater by MAC, got %s", att1)
	}

	att2 := info.MatchAttachment("BE:91:0E:CD:33:71", nil, "")
	if att2 != "twowater/twowater" {
		t.Errorf("expected twowater/twowater by MAC (case-insensitive), got %s", att2)
	}

	// Test MatchAttachment by IP
	att3 := info.MatchAttachment("", []string{"192.168.54.37"}, "")
	if att3 != "onewater/onewater" {
		t.Errorf("expected onewater/onewater by IP, got %s", att3)
	}

	// Test MatchAttachment by subnet
	att4 := info.MatchAttachment("", nil, "subnet-twowater")
	if att4 != "twowater/twowater" {
		t.Errorf("expected twowater/twowater by subnet, got %s", att4)
	}
}

func TestStaticWorkloadResolver(t *testing.T) {
	resolver := NewStaticWorkloadResolver()
	resolver.Add(WorkloadInfo{
		Kind:         "VirtualMachine",
		WorkloadName: "test-vm",
		PodName:      "virt-launcher-test-vm",
		Namespace:    "default",
		Owner:        "VirtualMachine/test-vm",
	})

	info := resolver.Resolve("default", "test-vm")
	if info == nil || info.Kind != "VirtualMachine" {
		t.Errorf("expected test-vm resolution, got %+v", info)
	}
}
