package k8sclient

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/status/config"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// WorkloadDiscoveryOptions controls which workloads become probes.
//
// Hand-listing 48 deployments in a config file is a file that is wrong the day
// after it is written. This turns "monitor every application" into a rule the
// cluster answers for itself.
type WorkloadDiscoveryOptions struct {
	// Namespaces to cover. Empty means every namespace not excluded.
	Namespaces []string
	// ExcludeNamespaces is applied after Namespaces.
	ExcludeNamespaces []string
	// Kinds to include: deployment, statefulset, daemonset. Empty means all three.
	Kinds []string
	// Interval between checks for a generated probe.
	Interval time.Duration
	// GroupPrefix is prepended to the namespace to form the display group.
	GroupPrefix string
	// MaxProbes caps how many probes are generated, so a very large cluster
	// cannot balloon the monitor's goroutine count without warning.
	MaxProbes int
}

const (
	defaultWorkloadInterval = 60 * time.Second
	defaultMaxWorkloads     = 400
)

func (o *WorkloadDiscoveryOptions) applyDefaults() {
	if o.Interval <= 0 {
		o.Interval = defaultWorkloadInterval
	}
	if o.MaxProbes <= 0 {
		o.MaxProbes = defaultMaxWorkloads
	}
	if len(o.Kinds) == 0 {
		o.Kinds = []string{"deployment", "statefulset", "daemonset"}
	}
}

func (o WorkloadDiscoveryOptions) wantsKind(kind string) bool {
	for _, k := range o.Kinds {
		if strings.EqualFold(strings.TrimSpace(k), kind) {
			return true
		}
	}
	return false
}

// namespaceInScope applies the include/exclude namespace rules.
func (o WorkloadDiscoveryOptions) namespaceInScope(ns string) bool {
	for _, ex := range o.ExcludeNamespaces {
		if strings.EqualFold(strings.TrimSpace(ex), ns) {
			return false
		}
	}
	if len(o.Namespaces) == 0 {
		return true
	}
	for _, in := range o.Namespaces {
		if strings.EqualFold(strings.TrimSpace(in), ns) {
			return true
		}
	}
	return false
}

// DiscoverWorkloads turns every in-scope Deployment, StatefulSet and DaemonSet
// into a probe. Reads come from the informer caches, so this is cheap enough to
// run at startup and again on every workload change.
func (c *Client) DiscoverWorkloads(opts WorkloadDiscoveryOptions) ([]config.Service, error) {
	if c == nil || c.Factory == nil {
		return nil, fmt.Errorf("k8s client not initialized")
	}
	opts.applyDefaults()

	var out []config.Service
	truncated := false

	add := func(svc config.Service) {
		if len(out) >= opts.MaxProbes {
			truncated = true
			return
		}
		out = append(out, svc)
	}

	if opts.wantsKind("deployment") {
		items, err := c.Deployments.List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("list deployments: %w", err)
		}
		for _, d := range items {
			if !opts.namespaceInScope(d.Namespace) {
				continue
			}
			add(workloadProbe(config.CheckK8sDeployment, "Deployment",
				d.Namespace, d.Name, primaryImage(d.Spec.Template), opts))
		}
	}
	if opts.wantsKind("statefulset") {
		items, err := c.StatefulSets.List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("list statefulsets: %w", err)
		}
		for _, s := range items {
			if !opts.namespaceInScope(s.Namespace) {
				continue
			}
			add(workloadProbe(config.CheckK8sStatefulSet, "StatefulSet",
				s.Namespace, s.Name, primaryImage(s.Spec.Template), opts))
		}
	}
	if opts.wantsKind("daemonset") {
		items, err := c.DaemonSets.List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("list daemonsets: %w", err)
		}
		for _, d := range items {
			if !opts.namespaceInScope(d.Namespace) {
				continue
			}
			add(workloadProbe(config.CheckK8sDaemonSet, "DaemonSet",
				d.Namespace, d.Name, primaryImage(d.Spec.Template), opts))
		}
	}

	if truncated {
		// Never silently drop coverage: a capped run must say so, or the page
		// reads as "everything is monitored" when it is not.
		log.Printf("workload discovery: capped at %d probes — some workloads are NOT monitored; raise cluster.auto_workloads.max_probes",
			opts.MaxProbes)
	}
	return out, nil
}

// WorkloadProbeName is the display name a generated probe carries. It is also
// the reconcile key, so it must be derived only from stable identity.
func WorkloadProbeName(kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s (%s)", namespace, name, kind)
}

func workloadProbe(t config.CheckType, kind, ns, name, image string, opts WorkloadDiscoveryOptions) config.Service {
	group := ns
	if opts.GroupPrefix != "" {
		group = opts.GroupPrefix + ns
	}
	desc := kind
	if image != "" {
		desc = kind + " · " + image
	}
	return config.Service{
		Name:         WorkloadProbeName(kind, ns, name),
		Group:        group,
		Type:         t,
		K8sNamespace: ns,
		K8sName:      name,
		Interval:     opts.Interval,
		Timeout:      10 * time.Second,
		Description:  desc,
	}
}

// WatchWorkloads keeps generated probes in step with the cluster: a new
// Deployment appears as a probe within seconds, a deleted one stops being
// reported as down forever.
//
// reserved holds names defined in config.yaml, which are never overridden.
func (c *Client) WatchWorkloads(ctx context.Context, rec Reconciler, reserved map[string]bool, opts WorkloadDiscoveryOptions) error {
	if c == nil || c.Factory == nil {
		return fmt.Errorf("k8s client not initialized")
	}
	opts.applyDefaults()

	install := func(informer cache.SharedIndexInformer, kind string, probe config.CheckType) error {
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				ns, name, image, ok := workloadMeta(obj)
				if !ok || !opts.namespaceInScope(ns) {
					return
				}
				svc := workloadProbe(probe, kind, ns, name, image, opts)
				if reserved[svc.Name] {
					return
				}
				if rec.AddService(svc) {
					log.Printf("workload discovery: added %q", svc.Name)
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				ns, name, image, ok := workloadMeta(newObj)
				if !ok || !opts.namespaceInScope(ns) {
					return
				}
				svc := workloadProbe(probe, kind, ns, name, image, opts)
				if reserved[svc.Name] {
					return
				}
				// Only rebuild when the probe's own definition changed.
				// The informer resyncs every few minutes and replays every
				// object; tearing the probe down each time would reset its
				// history and uptime for no reason. AddService is a no-op for
				// a name already registered, so this still covers the case
				// where the probe went missing.
				if oldNs, oldName, oldImage, okOld := workloadMeta(oldObj); okOld &&
					oldNs == ns && oldName == name && oldImage == image {
					rec.AddService(svc)
					return
				}
				rec.RemoveService(svc.Name)
				rec.AddService(svc)
			},
			DeleteFunc: func(obj any) {
				if t, isT := obj.(cache.DeletedFinalStateUnknown); isT {
					obj = t.Obj
				}
				ns, name, _, ok := workloadMeta(obj)
				if !ok {
					return
				}
				probeName := WorkloadProbeName(kind, ns, name)
				if reserved[probeName] {
					return
				}
				if rec.RemoveService(probeName) {
					log.Printf("workload discovery: removed %q (deleted)", probeName)
				}
			},
		})
		return err
	}

	if opts.wantsKind("deployment") {
		if err := install(c.Factory.Apps().V1().Deployments().Informer(),
			"Deployment", config.CheckK8sDeployment); err != nil {
			return err
		}
	}
	if opts.wantsKind("statefulset") {
		if err := install(c.Factory.Apps().V1().StatefulSets().Informer(),
			"StatefulSet", config.CheckK8sStatefulSet); err != nil {
			return err
		}
	}
	if opts.wantsKind("daemonset") {
		if err := install(c.Factory.Apps().V1().DaemonSets().Informer(),
			"DaemonSet", config.CheckK8sDaemonSet); err != nil {
			return err
		}
	}
	return nil
}

// workloadMeta pulls namespace/name/image out of any of the three kinds.
func workloadMeta(obj any) (ns, name, image string, ok bool) {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		return v.Namespace, v.Name, primaryImage(v.Spec.Template), true
	case *appsv1.StatefulSet:
		return v.Namespace, v.Name, primaryImage(v.Spec.Template), true
	case *appsv1.DaemonSet:
		return v.Namespace, v.Name, primaryImage(v.Spec.Template), true
	}
	return "", "", "", false
}
