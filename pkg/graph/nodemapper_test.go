package graph

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMatchChassisPrefersSystemIDAnnotation(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "hv-01", Annotations: map[string]string{OvnChassisAnnotation: "43519f69"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "hv-02"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: "hv-02.lab"}}}},
	)
	m := NewKubeNodeMapper(client, nil)
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ hostname, name, node, how string }{
		{"whatever", "43519f69", "hv-01", "annotation"},         // the system-id wins over the hostname
		{"hv-02.lab", "aaaa", "hv-02", "hostname"},              // hostname address of a node without annotation
		{"gone-host", "bbbb", "gone-host", "fallback-hostname"}, // no Kubernetes node (stale / remote chassis)
		{"", "cccc", "cccc", "fallback-name"},
	} {
		if node, how := m.MatchChassis(tc.hostname, tc.name); node != tc.node || how != tc.how {
			t.Errorf("MatchChassis(%q, %q) = %s/%s, want %s/%s", tc.hostname, tc.name, node, how, tc.node, tc.how)
		}
	}
}
