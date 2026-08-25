package k8sclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Snapshot is a whole-cluster read taken from the primed informer caches.
// Building one costs no apiserver round-trips except the optional
// metrics-server call, so it is safe to serve on every page load.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version"`
	Platform    string    `json:"platform"`

	Summary    Summary         `json:"summary"`
	Nodes      []NodeInfo      `json:"nodes"`
	Namespaces []NamespaceInfo `json:"namespaces"`
	Workloads  []WorkloadInfo  `json:"workloads"`
	Problems   []ProblemPod    `json:"problems"`
	Events     []EventInfo     `json:"events"`
	Storage    StorageInfo     `json:"storage"`
}

// Summary is the headline row: one number per thing an operator scans first.
type Summary struct {
	NodesTotal int `json:"nodes_total"`
	NodesReady int `json:"nodes_ready"`

	PodsTotal     int `json:"pods_total"`
	PodsRunning   int `json:"pods_running"`
	PodsPending   int `json:"pods_pending"`
	PodsFailed    int `json:"pods_failed"`
	PodsSucceeded int `json:"pods_succeeded"`
	PodsUnhealthy int `json:"pods_unhealthy"`

	Namespaces   int `json:"namespaces"`
	Services     int `json:"services"`
	Deployments  int `json:"deployments"`
	StatefulSets int `json:"statefulsets"`
	DaemonSets   int `json:"daemonsets"`
	CronJobs     int `json:"cronjobs"`
	HPAs         int `json:"hpas"`

	WorkloadsDegraded int `json:"workloads_degraded"`
	WarningEvents15m  int `json:"warning_events_15m"`

	CPUCapacityMilli int64 `json:"cpu_capacity_milli"`
	CPURequestMilli  int64 `json:"cpu_request_milli"`
	CPUUsageMilli    int64 `json:"cpu_usage_milli"`
	MemCapacityBytes int64 `json:"mem_capacity_bytes"`
	MemRequestBytes  int64 `json:"mem_request_bytes"`
	MemUsageBytes    int64 `json:"mem_usage_bytes"`

	MetricsAvailable bool `json:"metrics_available"`
}

// NodeInfo is one row of the node table.
type NodeInfo struct {
	Name             string    `json:"name"`
	Ready            bool      `json:"ready"`
	Schedulable      bool      `json:"schedulable"`
	Roles            []string  `json:"roles"`
	Conditions       []string  `json:"conditions"` // active problem conditions only
	KubeletVersion   string    `json:"kubelet_version"`
	OSImage          string    `json:"os_image"`
	ContainerRuntime string    `json:"container_runtime"`
	InternalIP       string    `json:"internal_ip"`
	Architecture     string    `json:"architecture"`
	CreatedAt        time.Time `json:"created_at"`
	AgeSeconds       int64     `json:"age_seconds"`

	Pods    int `json:"pods"`
	PodsMax int `json:"pods_max"`

	CPUCapacityMilli int64 `json:"cpu_capacity_milli"`
	CPURequestMilli  int64 `json:"cpu_request_milli"`
	CPUUsageMilli    int64 `json:"cpu_usage_milli"`
	MemCapacityBytes int64 `json:"mem_capacity_bytes"`
	MemRequestBytes  int64 `json:"mem_request_bytes"`
	MemUsageBytes    int64 `json:"mem_usage_bytes"`
}

// NamespaceInfo is one row of the namespace table.
type NamespaceInfo struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Pods        int    `json:"pods"`
	PodsRunning int    `json:"pods_running"`
	PodsProblem int    `json:"pods_problem"`
	Workloads   int    `json:"workloads"`
	Services    int    `json:"services"`
	PVCs        int    `json:"pvcs"`
	CPUReqMilli int64  `json:"cpu_request_milli"`
	MemReqBytes int64  `json:"mem_request_bytes"`
}

// WorkloadInfo covers Deployment / StatefulSet / DaemonSet uniformly.
type WorkloadInfo struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     int32  `json:"ready"`
	Desired   int32  `json:"desired"`
	Updated   int32  `json:"updated"`
	Available int32  `json:"available"`
	Status    string `json:"status"` // operational | degraded | down | scaled-to-zero
	Image     string `json:"image"`
}

// ProblemPod is a pod an operator would want to look at right now.
type ProblemPod struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Node      string    `json:"node"`
	Phase     string    `json:"phase"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Restarts  int32     `json:"restarts"`
	StartedAt time.Time `json:"started_at"`
}

// EventInfo is a recent Warning event.
type EventInfo struct {
	Namespace string    `json:"namespace"`
	Reason    string    `json:"reason"`
	Object    string    `json:"object"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	LastSeen  time.Time `json:"last_seen"`
}

// StorageInfo aggregates PVC state.
type StorageInfo struct {
	Total          int   `json:"total"`
	Bound          int   `json:"bound"`
	Pending        int   `json:"pending"`
	Lost           int   `json:"lost"`
	RequestedBytes int64 `json:"requested_bytes"`
}

const (
	maxProblemPods = 50
	maxEvents      = 50
	maxWorkloads   = 500
	eventWindow    = 15 * time.Minute
)

// versionCache memoises the /version discovery call; the cluster version does
// not change between upgrades, and this keeps Snapshot() cache-only in steady
// state.
type versionCache struct {
	mu       sync.Mutex
	version  string
	platform string
	fetched  time.Time
}

var verCache versionCache

// Snapshot builds a full cluster view. Every list read hits the informer
// cache. Metrics-server is best-effort: when it is absent or erroring the
// snapshot still returns with Summary.MetricsAvailable=false.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	if c == nil || c.Factory == nil {
		return nil, fmt.Errorf("k8s client not initialized")
	}

	nodes, err := c.Nodes.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	pods, err := c.Pods.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	nsList, err := c.Namespaces.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	svcs, err := c.Services.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	deploys, err := c.Deployments.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	stss, err := c.StatefulSets.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	dss, err := c.DaemonSets.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list daemonsets: %w", err)
	}
	cjs, err := c.CronJobs.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list cronjobs: %w", err)
	}
	hpas, err := c.HPAs.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list hpas: %w", err)
	}
	pvcs, err := c.PVCs.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list pvcs: %w", err)
	}
	events, err := c.Events.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	now := time.Now()
	snap := &Snapshot{GeneratedAt: now}
	snap.Version, snap.Platform = c.serverVersion(ctx)

	// --- pass 1: pods. Indexes everything the node and namespace tables need.
	type nsAgg struct {
		pods, running, problem, svcs, workloads, pvcs int
		cpuReq, memReq                                int64
	}
	perNS := map[string]*nsAgg{}
	perNodePods := map[string]int{}
	perNodeCPUReq := map[string]int64{}
	perNodeMemReq := map[string]int64{}

	getNS := func(n string) *nsAgg {
		a, ok := perNS[n]
		if !ok {
			a = &nsAgg{}
			perNS[n] = a
		}
		return a
	}

	for _, p := range pods {
		agg := getNS(p.Namespace)
		agg.pods++
		snap.Summary.PodsTotal++

		switch p.Status.Phase {
		case corev1.PodRunning:
			snap.Summary.PodsRunning++
			agg.running++
		case corev1.PodPending:
			snap.Summary.PodsPending++
		case corev1.PodFailed:
			snap.Summary.PodsFailed++
		case corev1.PodSucceeded:
			snap.Summary.PodsSucceeded++
		}

		// Requests only count for pods that hold a scheduling claim.
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			cpu, mem := podRequests(p)
			agg.cpuReq += cpu
			agg.memReq += mem
			snap.Summary.CPURequestMilli += cpu
			snap.Summary.MemRequestBytes += mem
			if p.Spec.NodeName != "" {
				perNodeCPUReq[p.Spec.NodeName] += cpu
				perNodeMemReq[p.Spec.NodeName] += mem
			}
		}
		if p.Spec.NodeName != "" {
			perNodePods[p.Spec.NodeName]++
		}

		if reason, msg, bad := podProblem(p); bad {
			snap.Summary.PodsUnhealthy++
			agg.problem++
			if len(snap.Problems) < maxProblemPods {
				var started time.Time
				if p.Status.StartTime != nil {
					started = p.Status.StartTime.Time
				}
				snap.Problems = append(snap.Problems, ProblemPod{
					Namespace: p.Namespace,
					Name:      p.Name,
					Node:      p.Spec.NodeName,
					Phase:     string(p.Status.Phase),
					Reason:    reason,
					Message:   truncate(msg, 200),
					Restarts:  podRestarts(p),
					StartedAt: started,
				})
			}
		}
	}

	for _, s := range svcs {
		getNS(s.Namespace).svcs++
	}
	snap.Summary.Services = len(svcs)

	// --- pass 2: nodes.
	for _, n := range nodes {
		ready, problems := nodeConditions(n)
		cpuCap := n.Status.Capacity.Cpu().MilliValue()
		memCap := n.Status.Capacity.Memory().Value()
		podsMax := int(n.Status.Capacity.Pods().Value())

		snap.Summary.NodesTotal++
		if ready {
			snap.Summary.NodesReady++
		}
		snap.Summary.CPUCapacityMilli += cpuCap
		snap.Summary.MemCapacityBytes += memCap

		ni := NodeInfo{
			Name:             n.Name,
			Ready:            ready,
			Schedulable:      !n.Spec.Unschedulable,
			Roles:            nodeRoles(n),
			Conditions:       problems,
			KubeletVersion:   n.Status.NodeInfo.KubeletVersion,
			OSImage:          n.Status.NodeInfo.OSImage,
			ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
			Architecture:     n.Status.NodeInfo.Architecture,
			InternalIP:       nodeInternalIP(n),
			CreatedAt:        n.CreationTimestamp.Time,
			AgeSeconds:       int64(now.Sub(n.CreationTimestamp.Time).Seconds()),
			Pods:             perNodePods[n.Name],
			PodsMax:          podsMax,
			CPUCapacityMilli: cpuCap,
			CPURequestMilli:  perNodeCPUReq[n.Name],
			MemCapacityBytes: memCap,
			MemRequestBytes:  perNodeMemReq[n.Name],
		}
		snap.Nodes = append(snap.Nodes, ni)
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })

	// --- pass 3: workloads.
	for _, d := range deploys {
		getNS(d.Namespace).workloads++
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		snap.Workloads = append(snap.Workloads, WorkloadInfo{
			Kind:      "Deployment",
			Namespace: d.Namespace,
			Name:      d.Name,
			Ready:     d.Status.ReadyReplicas,
			Desired:   desired,
			Updated:   d.Status.UpdatedReplicas,
			Available: d.Status.AvailableReplicas,
			Status:    workloadStatus(d.Status.ReadyReplicas, desired, d.Status.AvailableReplicas),
			Image:     primaryImage(d.Spec.Template),
		})
	}
	snap.Summary.Deployments = len(deploys)

	for _, s := range stss {
		getNS(s.Namespace).workloads++
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		snap.Workloads = append(snap.Workloads, WorkloadInfo{
			Kind:      "StatefulSet",
			Namespace: s.Namespace,
			Name:      s.Name,
			Ready:     s.Status.ReadyReplicas,
			Desired:   desired,
			Updated:   s.Status.UpdatedReplicas,
			Available: s.Status.ReadyReplicas,
			Status:    workloadStatus(s.Status.ReadyReplicas, desired, s.Status.ReadyReplicas),
			Image:     primaryImage(s.Spec.Template),
		})
	}
	snap.Summary.StatefulSets = len(stss)

	for _, d := range dss {
		getNS(d.Namespace).workloads++
		snap.Workloads = append(snap.Workloads, WorkloadInfo{
			Kind:      "DaemonSet",
			Namespace: d.Namespace,
			Name:      d.Name,
			Ready:     d.Status.NumberReady,
			Desired:   d.Status.DesiredNumberScheduled,
			Updated:   d.Status.UpdatedNumberScheduled,
			Available: d.Status.NumberAvailable,
			Status:    workloadStatus(d.Status.NumberReady, d.Status.DesiredNumberScheduled, d.Status.NumberAvailable),
			Image:     primaryImage(d.Spec.Template),
		})
	}
	snap.Summary.DaemonSets = len(dss)
	snap.Summary.CronJobs = len(cjs)
	snap.Summary.HPAs = len(hpas)

	for _, w := range snap.Workloads {
		if w.Status == "degraded" || w.Status == "down" {
			snap.Summary.WorkloadsDegraded++
		}
	}
	// Unhealthy first, then kind/namespace/name — the table is read top-down.
	sort.Slice(snap.Workloads, func(i, j int) bool {
		a, b := snap.Workloads[i], snap.Workloads[j]
		ra, rb := workloadRank(a.Status), workloadRank(b.Status)
		if ra != rb {
			return ra < rb
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	if len(snap.Workloads) > maxWorkloads {
		snap.Workloads = snap.Workloads[:maxWorkloads]
	}

	// --- pass 4: storage.
	for _, p := range pvcs {
		getNS(p.Namespace).pvcs++
		snap.Storage.Total++
		switch p.Status.Phase {
		case corev1.ClaimBound:
			snap.Storage.Bound++
		case corev1.ClaimPending:
			snap.Storage.Pending++
		case corev1.ClaimLost:
			snap.Storage.Lost++
		}
		if q, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			snap.Storage.RequestedBytes += q.Value()
		}
	}

	// --- pass 5: recent Warning events.
	cutoff := now.Add(-eventWindow)
	for _, e := range events {
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		ts := eventTime(e)
		if ts.Before(cutoff) {
			continue
		}
		snap.Summary.WarningEvents15m++
		snap.Events = append(snap.Events, EventInfo{
			Namespace: e.Namespace,
			Reason:    e.Reason,
			Object:    fmt.Sprintf("%s/%s", strings.ToLower(e.InvolvedObject.Kind), e.InvolvedObject.Name),
			Message:   truncate(e.Message, 200),
			Count:     e.Count,
			LastSeen:  ts,
		})
	}
	sort.Slice(snap.Events, func(i, j int) bool { return snap.Events[i].LastSeen.After(snap.Events[j].LastSeen) })
	if len(snap.Events) > maxEvents {
		snap.Events = snap.Events[:maxEvents]
	}

	// --- pass 6: namespaces.
	for _, ns := range nsList {
		a := perNS[ns.Name]
		if a == nil {
			a = &nsAgg{}
		}
		snap.Namespaces = append(snap.Namespaces, NamespaceInfo{
			Name:        ns.Name,
			Phase:       string(ns.Status.Phase),
			Pods:        a.pods,
			PodsRunning: a.running,
			PodsProblem: a.problem,
			Workloads:   a.workloads,
			Services:    a.svcs,
			PVCs:        a.pvcs,
			CPUReqMilli: a.cpuReq,
			MemReqBytes: a.memReq,
		})
	}
	sort.Slice(snap.Namespaces, func(i, j int) bool { return snap.Namespaces[i].Name < snap.Namespaces[j].Name })
	snap.Summary.Namespaces = len(snap.Namespaces)

	// --- optional: live usage from metrics-server.
	c.applyNodeMetrics(ctx, snap)

	return snap, nil
}

// applyNodeMetrics fills in live CPU/memory usage. Failure is not fatal: the
// snapshot degrades to capacity+requests only.
func (c *Client) applyNodeMetrics(ctx context.Context, snap *Snapshot) {
	if c.Metrics == nil {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	list, err := c.Metrics.MetricsV1beta1().NodeMetricses().List(mctx, metav1.ListOptions{})
	if err != nil || list == nil {
		return
	}
	usage := make(map[string][2]int64, len(list.Items))
	for _, m := range list.Items {
		cpu := m.Usage[corev1.ResourceCPU]
		mem := m.Usage[corev1.ResourceMemory]
		usage[m.Name] = [2]int64{cpu.MilliValue(), mem.Value()}
	}
	if len(usage) == 0 {
		return
	}
	snap.Summary.MetricsAvailable = true
	for i := range snap.Nodes {
		u, ok := usage[snap.Nodes[i].Name]
		if !ok {
			continue
		}
		snap.Nodes[i].CPUUsageMilli = u[0]
		snap.Nodes[i].MemUsageBytes = u[1]
		snap.Summary.CPUUsageMilli += u[0]
		snap.Summary.MemUsageBytes += u[1]
	}
}

// serverVersion returns the cached cluster version, refreshing at most hourly.
func (c *Client) serverVersion(ctx context.Context) (version, platform string) {
	verCache.mu.Lock()
	defer verCache.mu.Unlock()
	if verCache.version != "" && time.Since(verCache.fetched) < time.Hour {
		return verCache.version, verCache.platform
	}
	// ServerVersion has no ctx-aware variant; the discovery client carries its
	// own timeout from rest.Config.
	_ = ctx
	info, err := c.Clientset.Discovery().ServerVersion()
	if err != nil || info == nil {
		return verCache.version, verCache.platform // may be "" on first failure
	}
	verCache.version = info.GitVersion
	verCache.platform = info.Platform
	verCache.fetched = time.Now()
	return verCache.version, verCache.platform
}

// --- helpers ---

func podRequests(p *corev1.Pod) (cpuMilli, memBytes int64) {
	add := func(rl corev1.ResourceList) (int64, int64) {
		var c, m int64
		if q, ok := rl[corev1.ResourceCPU]; ok {
			c = q.MilliValue()
		}
		if q, ok := rl[corev1.ResourceMemory]; ok {
			m = q.Value()
		}
		return c, m
	}
	for _, ctr := range p.Spec.Containers {
		c, m := add(ctr.Resources.Requests)
		cpuMilli += c
		memBytes += m
	}
	// Init containers do not add to the pod's steady-state request; they take
	// the max of themselves vs the sum of regular containers.
	for _, ctr := range p.Spec.InitContainers {
		c, m := add(ctr.Resources.Requests)
		if c > cpuMilli {
			cpuMilli = c
		}
		if m > memBytes {
			memBytes = m
		}
	}
	return
}

func podRestarts(p *corev1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

// podProblem reports whether a pod is in a state an operator should see, and
// why. Succeeded pods and pods still legitimately starting are not problems.
func podProblem(p *corev1.Pod) (reason, message string, bad bool) {
	switch p.Status.Phase {
	case corev1.PodSucceeded:
		return "", "", false
	case corev1.PodFailed:
		r := p.Status.Reason
		if r == "" {
			r = "Failed"
		}
		return r, p.Status.Message, true
	}

	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
				"CreateContainerConfigError", "CreateContainerError",
				"InvalidImageName", "RunContainerError":
				return w.Reason, w.Message, true
			}
		}
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return "Terminated:" + t.Reason, t.Message, true
		}
	}

	if p.Status.Phase == corev1.PodPending {
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				return cond.Reason, cond.Message, true
			}
		}
		// Pending but schedulable and young: still pulling images, not a problem.
		if p.Status.StartTime != nil && time.Since(p.Status.StartTime.Time) > 5*time.Minute {
			return "Pending", "pending for over 5m", true
		}
		return "", "", false
	}

	// Running but a container is not ready long after start.
	if p.Status.Phase == corev1.PodRunning {
		for _, cs := range p.Status.ContainerStatuses {
			if !cs.Ready && cs.State.Running != nil &&
				time.Since(cs.State.Running.StartedAt.Time) > 5*time.Minute {
				return "NotReady", "container " + cs.Name + " not ready", true
			}
		}
	}
	return "", "", false
}

func nodeConditions(n *corev1.Node) (ready bool, problems []string) {
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			ready = c.Status == corev1.ConditionTrue
			if !ready {
				problems = append(problems, "NotReady")
			}
		default:
			// Every non-Ready condition is phrased as a problem when True.
			if c.Status == corev1.ConditionTrue {
				problems = append(problems, string(c.Type))
			}
		}
	}
	if n.Spec.Unschedulable {
		problems = append(problems, "Unschedulable")
	}
	return
}

func nodeRoles(n *corev1.Node) []string {
	var roles []string
	for k, v := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			roles = append(roles, r)
		} else if k == "kubernetes.io/role" && v != "" {
			roles = append(roles, v)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		roles = []string{"worker"}
	}
	return roles
}

func nodeInternalIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

func workloadStatus(ready, desired, available int32) string {
	switch {
	case desired == 0:
		return "scaled-to-zero"
	case ready == 0:
		return "down"
	case ready < desired || available < desired:
		return "degraded"
	default:
		return "operational"
	}
}

func workloadRank(status string) int {
	switch status {
	case "down":
		return 0
	case "degraded":
		return 1
	case "operational":
		return 2
	default:
		return 3
	}
}

func primaryImage(t corev1.PodTemplateSpec) string {
	if len(t.Spec.Containers) == 0 {
		return ""
	}
	return t.Spec.Containers[0].Image
}

func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.Time.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.Time.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
