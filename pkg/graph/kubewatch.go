package graph

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// WatchKubernetes keeps the node mapper and the workload resolver current with
// read-only Node and Pod informers (list + watch). When an identity-relevant change
// lands, the store revision is bumped so SSE and If-None-Match clients refetch;
// changes are coalesced over debounce. It blocks until ctx is done.
func WatchKubernetes(ctx context.Context, client kubernetes.Interface, mapper *KubeNodeMapper,
	resolver *KubeWorkloadResolver, store *Store, debounce time.Duration, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	factory := informers.NewSharedInformerFactory(client, 0)
	nodes := factory.Core().V1().Nodes()
	pods := factory.Core().V1().Pods()
	// Keep only what identity resolution reads: pods are the largest objects watched.
	_ = pods.Informer().SetTransform(trimPod)

	dirty := make(chan struct{}, 1)
	poke := func(any) {
		select {
		case dirty <- struct{}{}:
		default:
		}
	}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    poke,
		UpdateFunc: func(_, n any) { poke(n) },
		DeleteFunc: poke,
	}
	for _, inf := range []cache.SharedIndexInformer{nodes.Informer(), pods.Informer()} {
		if _, err := inf.AddEventHandler(handler); err != nil {
			logger.Warn("kubernetes informer handler not registered", "error", err)
		}
	}

	factory.Start(ctx.Done())
	for typ, ok := range factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			logger.Warn("kubernetes informer cache not synced", "type", typ.String())
		}
	}

	refresh := func() {
		ns, err := nodes.Lister().List(labels.Everything())
		if err != nil {
			logger.Warn("listing cached nodes failed", "error", err)
			return
		}
		ps, err := pods.Lister().List(labels.Everything())
		if err != nil {
			logger.Warn("listing cached pods failed", "error", err)
			return
		}
		nodeChanged := mapper.SetNodes(ns)
		podChanged := resolver.SetPods(ps)
		if nodeChanged || podChanged {
			logger.Debug("kubernetes identity changed", "nodes", len(ns), "pods", len(ps))
			store.Apply(func(*Snapshot) []string { return []string{"*"} })
		}
	}
	refresh()

	for {
		select {
		case <-ctx.Done():
			return
		case <-dirty:
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
			// Pokes that arrived while waiting are covered by this refresh.
			select {
			case <-dirty:
			default:
			}
			refresh()
		}
	}
}

// trimPod drops the parts of a Pod that identity resolution never reads.
func trimPod(obj any) (any, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil // tombstones
	}
	pod.ManagedFields = nil
	pod.Spec = corev1.PodSpec{NodeName: pod.Spec.NodeName}
	pod.Status = corev1.PodStatus{}
	return pod, nil
}
