// Package k8sclient provides shared client-go connectivity and informers
// for k8s_* probe types in the monitor package.
//
// Single in-cluster only: rest.InClusterConfig() reads the projected
// ServiceAccount token from the pod. Token rotation is handled by kubelet.
package k8sclient

import (
	"context"
	"fmt"
	"time"

	appsv1listers "k8s.io/client-go/listers/apps/v1"
	autoscalingv2listers "k8s.io/client-go/listers/autoscaling/v2"
	batchv1listers "k8s.io/client-go/listers/batch/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Client bundles the typed clientset, metrics-server client, and a primed
// SharedInformerFactory with listers for every resource the probes need.
type Client struct {
	Config *rest.Config
	// Interfaces rather than concrete clientsets: the whole-cluster Snapshot
	// is worth testing, and that needs fakes to be substitutable here.
	Clientset kubernetes.Interface
	Metrics   metricsclient.Interface

	Factory informers.SharedInformerFactory

	Nodes        corev1listers.NodeLister
	Pods         corev1listers.PodLister
	PVCs         corev1listers.PersistentVolumeClaimLister
	Events       corev1listers.EventLister
	Namespaces   corev1listers.NamespaceLister
	Services     corev1listers.ServiceLister
	Deployments  appsv1listers.DeploymentLister
	StatefulSets appsv1listers.StatefulSetLister
	DaemonSets   appsv1listers.DaemonSetLister
	HPAs         autoscalingv2listers.HorizontalPodAutoscalerLister
	CronJobs     batchv1listers.CronJobLister
}

// New builds an in-cluster client with bumped QPS/Burst and starts informers.
// Returns nil + error if not running inside a pod.
func New(ctx context.Context, resync time.Duration) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	cfg.QPS = 25
	cfg.Burst = 50
	cfg.UserAgent = "status-monitor/1.0"

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes clientset: %w", err)
	}
	mc, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics clientset: %w", err)
	}

	f := informers.NewSharedInformerFactory(cs, resync)
	c := &Client{
		Config:       cfg,
		Clientset:    cs,
		Metrics:      mc,
		Factory:      f,
		Nodes:        f.Core().V1().Nodes().Lister(),
		Pods:         f.Core().V1().Pods().Lister(),
		PVCs:         f.Core().V1().PersistentVolumeClaims().Lister(),
		Events:       f.Core().V1().Events().Lister(),
		Namespaces:   f.Core().V1().Namespaces().Lister(),
		Services:     f.Core().V1().Services().Lister(),
		Deployments:  f.Apps().V1().Deployments().Lister(),
		StatefulSets: f.Apps().V1().StatefulSets().Lister(),
		DaemonSets:   f.Apps().V1().DaemonSets().Lister(),
		HPAs:         f.Autoscaling().V2().HorizontalPodAutoscalers().Lister(),
		CronJobs:     f.Batch().V1().CronJobs().Lister(),
	}

	// Touch each informer so the factory starts them.
	_ = f.Core().V1().Nodes().Informer()
	_ = f.Core().V1().Pods().Informer()
	_ = f.Core().V1().PersistentVolumeClaims().Informer()
	_ = f.Core().V1().Events().Informer()
	_ = f.Core().V1().Namespaces().Informer()
	_ = f.Core().V1().Services().Informer()
	_ = f.Apps().V1().Deployments().Informer()
	_ = f.Apps().V1().StatefulSets().Informer()
	_ = f.Apps().V1().DaemonSets().Informer()
	_ = f.Autoscaling().V2().HorizontalPodAutoscalers().Informer()
	_ = f.Batch().V1().CronJobs().Informer()

	f.Start(ctx.Done())
	if !waitForCaches(ctx, f, 60*time.Second) {
		return nil, fmt.Errorf("informer caches did not sync within 60s")
	}
	return c, nil
}

func waitForCaches(ctx context.Context, f informers.SharedInformerFactory, timeout time.Duration) bool {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for kind, synced := range f.WaitForCacheSync(c.Done()) {
		if !synced {
			fmt.Printf("k8s informer %v not synced\n", kind)
			return false
		}
	}
	return true
}
