package k8sclient

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

// newTestClient wires a Client onto a fake API server and primes every
// informer the snapshot reads, so Snapshot() runs exactly as it does live.
func newTestClient(t *testing.T, objs ...any) *Client {
	t.Helper()

	runtimeObjs := make([]any, 0, len(objs))
	runtimeObjs = append(runtimeObjs, objs...)

	cs := fake.NewSimpleClientset(toRuntimeObjects(runtimeObjs)...)
	f := informers.NewSharedInformerFactory(cs, 0)

	c := &Client{
		Clientset:    cs,
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

	// Touch each informer so Start() actually launches it.
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

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	f.Start(stop)
	f.WaitForCacheSync(stop)

	// serverVersion() memoises globally; reset it so tests do not leak state.
	verCache.mu.Lock()
	verCache.version, verCache.platform, verCache.fetched = "", "", time.Time{}
	verCache.mu.Unlock()

	return c
}

func qty(s string) resource.Quantity { return resource.MustParse(s) }

func node(name string, ready bool, cpu, mem string) *corev1.Node {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour)),
			Labels:            map[string]string{"node-role.kubernetes.io/control-plane": ""},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    qty(cpu),
				corev1.ResourceMemory: qty(mem),
				corev1.ResourcePods:   qty("110"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.31.0"},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
		},
	}
}

func pod(ns, name, nodeName string, phase corev1.PodPhase, cpu, mem string) *corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-time.Hour))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    qty(cpu),
						corev1.ResourceMemory: qty(mem),
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: phase, StartTime: &start},
	}
}

func TestSnapshotCountsNodesPodsAndRequests(t *testing.T) {
	c := newTestClient(t,
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "prod"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		node("node-a", true, "4", "8Gi"),
		node("node-b", false, "4", "8Gi"),
		pod("prod", "web-1", "node-a", corev1.PodRunning, "500m", "512Mi"),
		pod("prod", "web-2", "node-a", corev1.PodRunning, "500m", "512Mi"),
		pod("prod", "job-1", "node-a", corev1.PodSucceeded, "500m", "512Mi"),
	)

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, snap.Summary.NodesTotal)
	assert.Equal(t, 1, snap.Summary.NodesReady, "node-b is NotReady")
	assert.Equal(t, 3, snap.Summary.PodsTotal)
	assert.Equal(t, 2, snap.Summary.PodsRunning)
	assert.Equal(t, 1, snap.Summary.PodsSucceeded)

	// 8 cores capacity across two nodes.
	assert.Equal(t, int64(8000), snap.Summary.CPUCapacityMilli)
	// Only the two Running pods hold a claim; the Succeeded one does not.
	assert.Equal(t, int64(1000), snap.Summary.CPURequestMilli)
	assert.Equal(t, int64(1024*1024*1024), snap.Summary.MemRequestBytes)

	require.Len(t, snap.Nodes, 2)
	assert.Equal(t, "node-a", snap.Nodes[0].Name, "nodes are sorted by name")
	assert.Equal(t, 3, snap.Nodes[0].Pods)
	assert.Equal(t, []string{"control-plane"}, snap.Nodes[0].Roles)
	assert.False(t, snap.Nodes[1].Ready)
	assert.Contains(t, snap.Nodes[1].Conditions, "NotReady")

	require.Len(t, snap.Namespaces, 1)
	assert.Equal(t, "prod", snap.Namespaces[0].Name)
	assert.Equal(t, 3, snap.Namespaces[0].Pods)
	assert.Equal(t, 2, snap.Namespaces[0].PodsRunning)
}

func TestSnapshotFlagsCrashLoopingPods(t *testing.T) {
	crashing := pod("prod", "broken", "node-a", corev1.PodRunning, "100m", "64Mi")
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 7,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "CrashLoopBackOff",
				Message: "back-off 5m0s restarting failed container",
			},
		},
	}}

	c := newTestClient(t,
		node("node-a", true, "4", "8Gi"),
		pod("prod", "healthy", "node-a", corev1.PodRunning, "100m", "64Mi"),
		crashing,
	)

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, snap.Summary.PodsUnhealthy)
	require.Len(t, snap.Problems, 1)
	assert.Equal(t, "broken", snap.Problems[0].Name)
	assert.Equal(t, "CrashLoopBackOff", snap.Problems[0].Reason)
	assert.Equal(t, int32(7), snap.Problems[0].Restarts)
}

func TestSnapshotRanksUnhealthyWorkloadsFirst(t *testing.T) {
	two := int32(2)
	healthy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "aaa-healthy", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &two,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "nginx:1.27"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2},
	}
	broken := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "zzz-broken", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Replicas: &two},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0, AvailableReplicas: 0},
	}

	c := newTestClient(t, healthy, broken)
	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)

	require.Len(t, snap.Workloads, 2)
	assert.Equal(t, "zzz-broken", snap.Workloads[0].Name,
		"a down workload outranks an alphabetically earlier healthy one")
	assert.Equal(t, "down", snap.Workloads[0].Status)
	assert.Equal(t, "operational", snap.Workloads[1].Status)
	assert.Equal(t, "nginx:1.27", snap.Workloads[1].Image)
	assert.Equal(t, 1, snap.Summary.WorkloadsDegraded)
}

func TestSnapshotOnlyKeepsRecentWarningEvents(t *testing.T) {
	c := newTestClient(t,
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "prod"},
			Type:           corev1.EventTypeWarning,
			Reason:         "FailedScheduling",
			Message:        "0/3 nodes are available",
			Count:          4,
			LastTimestamp:  metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-1"},
		},
		&corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Name: "e2", Namespace: "prod"},
			Type:          corev1.EventTypeWarning,
			Reason:        "Ancient",
			LastTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		&corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Name: "e3", Namespace: "prod"},
			Type:          corev1.EventTypeNormal,
			Reason:        "Pulled",
			LastTimestamp: metav1.NewTime(time.Now()),
		},
	)

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, snap.Summary.WarningEvents15m)
	require.Len(t, snap.Events, 1)
	assert.Equal(t, "FailedScheduling", snap.Events[0].Reason)
	assert.Equal(t, "pod/web-1", snap.Events[0].Object)
}

func TestSnapshotSummarisesStorage(t *testing.T) {
	claim := func(name string, phase corev1.PersistentVolumeClaimPhase, size string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: qty(size)},
				},
			},
			Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
		}
	}
	c := newTestClient(t,
		claim("data-0", corev1.ClaimBound, "10Gi"),
		claim("data-1", corev1.ClaimPending, "5Gi"),
		claim("data-2", corev1.ClaimLost, "1Gi"),
	)

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 3, snap.Storage.Total)
	assert.Equal(t, 1, snap.Storage.Bound)
	assert.Equal(t, 1, snap.Storage.Pending)
	assert.Equal(t, 1, snap.Storage.Lost)
	assert.Equal(t, int64(16)*1024*1024*1024, snap.Storage.RequestedBytes)
}

func TestSnapshotWithoutMetricsServerStillSucceeds(t *testing.T) {
	c := newTestClient(t, node("node-a", true, "2", "4Gi"))
	c.Metrics = nil // metrics-server not installed

	snap, err := c.Snapshot(context.Background())
	require.NoError(t, err)
	assert.False(t, snap.Summary.MetricsAvailable)
	assert.Zero(t, snap.Summary.CPUUsageMilli)
	assert.Equal(t, int64(2000), snap.Summary.CPUCapacityMilli)
}

func TestWorkloadStatus(t *testing.T) {
	cases := []struct {
		name                  string
		ready, desired, avail int32
		want                  string
	}{
		{"all ready", 3, 3, 3, "operational"},
		{"none ready", 0, 3, 0, "down"},
		{"partially ready", 1, 3, 1, "degraded"},
		{"ready but unavailable", 3, 3, 1, "degraded"},
		{"intentionally scaled down", 0, 0, 0, "scaled-to-zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, workloadStatus(tc.ready, tc.desired, tc.avail))
		})
	}
}

func TestPodRequestsTakesInitContainerMax(t *testing.T) {
	p := pod("prod", "app", "node-a", corev1.PodRunning, "200m", "128Mi")
	p.Spec.InitContainers = []corev1.Container{{
		Name: "migrate",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    qty("900m"),
				corev1.ResourceMemory: qty("64Mi"),
			},
		},
	}}

	cpu, mem := podRequests(p)
	assert.Equal(t, int64(900), cpu, "init container's larger CPU wins")
	assert.Equal(t, int64(128*1024*1024), mem, "regular container's larger memory wins")
}

func TestTruncateAddsEllipsis(t *testing.T) {
	assert.Equal(t, "short", truncate("  short  ", 10))
	assert.Equal(t, "abcde…", truncate("abcdefghij", 5))
}
