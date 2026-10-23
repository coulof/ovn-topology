package graph

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestWatchKubernetesFollowsChanges: workloads and nodes that appear after the daemon
// started are resolved, and each identity change advances the store revision so
// clients refetch. Status-only pod updates do not.
func TestWatchKubernetesFollowsChanges(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "hv-01", Annotations: map[string]string{OvnChassisAnnotation: "sys-id-1"}}})
	mapper := NewKubeNodeMapper(client, nil)
	resolver := NewKubeWorkloadResolver(client, nil)
	store := NewStore(mapper, resolver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchKubernetes(ctx, client, mapper, resolver, store, 10*time.Millisecond, nil)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for: %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("initial node mapping", func() bool {
		n, how := mapper.MatchChassis("", "sys-id-1")
		return n == "hv-01" && how == "annotation"
	})

	rev := store.Current().Revision
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "virt-launcher-vm-late-x1y2z", Namespace: "default",
			Labels: map[string]string{"kubevirt.io": "virt-launcher", "vm.kubevirt.io/name": "vm-late"}},
		Spec: corev1.PodSpec{NodeName: "hv-01"},
	}
	if err := client.Tracker().Add(pod); err != nil { // the fake's object store, as a watch event
		t.Fatal(err)
	}
	waitFor("a VM started after the daemon is resolved", func() bool {
		wl := resolver.Resolve("default", "virt-launcher-vm-late-x1y2z")
		return wl != nil && wl.Owner == "VirtualMachine/vm-late" && wl.Node == "hv-01"
	})
	waitFor("the revision advances", func() bool { return store.Current().Revision > rev })

	// A status-only change is not an identity change, so it does not bump the revision.
	running := pod.DeepCopy()
	running.Status.Phase = corev1.PodRunning
	if resolver.SetPods([]*corev1.Pod{running}) {
		t.Errorf("a status-only pod change must not count as an identity change")
	}

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "hv-02", Annotations: map[string]string{OvnChassisAnnotation: "sys-id-2"}}}
	if err := client.Tracker().Add(node); err != nil {
		t.Fatal(err)
	}
	waitFor("a node added later maps by its chassis annotation", func() bool {
		n, how := mapper.MatchChassis("", "sys-id-2")
		return n == "hv-02" && how == "annotation"
	})
}
