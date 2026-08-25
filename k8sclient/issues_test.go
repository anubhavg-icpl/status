package k8sclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keysOf(issues []Issue) []string {
	out := make([]string, len(issues))
	for i, s := range issues {
		out[i] = s.Key
	}
	return out
}

func TestIssuesFromNilSnapshotIsEmpty(t *testing.T) {
	var s *Snapshot
	assert.Empty(t, s.Issues())
	assert.Empty(t, (&Snapshot{}).Issues())
}

func TestIssuesDetectsNotReadyNode(t *testing.T) {
	s := &Snapshot{Nodes: []NodeInfo{
		{Name: "work-02", Ready: false, Conditions: []string{"NotReady", "MemoryPressure"}},
		{Name: "work-01", Ready: true},
	}}
	issues := s.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, "node/work-02/NotReady", issues[0].Key)
	assert.Equal(t, SeverityCritical, issues[0].Severity)
	assert.Contains(t, issues[0].Message, "MemoryPressure")
}

func TestIssuesReportsPressureOnlyOnReadyNodes(t *testing.T) {
	s := &Snapshot{Nodes: []NodeInfo{
		{Name: "n1", Ready: true, Conditions: []string{"DiskPressure"}},
		{Name: "n2", Ready: true, Conditions: []string{"Unschedulable"}},
	}}
	issues := s.Issues()
	assert.ElementsMatch(t,
		[]string{"node/n1/DiskPressure", "node/n2/Unschedulable"}, keysOf(issues))
	for _, i := range issues {
		assert.Equal(t, SeverityMajor, i.Severity,
			"a Ready node under pressure is not an outage yet")
	}
}

func TestIssuesGradesWorkloads(t *testing.T) {
	s := &Snapshot{Workloads: []WorkloadInfo{
		{Kind: "Deployment", Namespace: "prod", Name: "api", Status: "down", Desired: 3},
		{Kind: "StatefulSet", Namespace: "prod", Name: "db", Status: "degraded", Ready: 2, Desired: 3},
		{Kind: "DaemonSet", Namespace: "kube-system", Name: "cni", Status: "operational"},
		{Kind: "Deployment", Namespace: "prod", Name: "batch", Status: "scaled-to-zero"},
	}}
	issues := s.Issues()

	require.Len(t, issues, 2, "healthy and intentionally-zero workloads are not issues")
	assert.Equal(t, "deployment/prod/api/down", issues[0].Key)
	assert.Equal(t, SeverityCritical, issues[0].Severity, "critical sorts first")
	assert.Equal(t, "statefulset/prod/db/degraded", issues[1].Key)
	assert.Equal(t, SeverityMajor, issues[1].Severity)
}

func TestIssuesIncludesProblemPodsWithContext(t *testing.T) {
	s := &Snapshot{Problems: []ProblemPod{{
		Namespace: "prod", Name: "api-abc", Node: "work-01",
		Reason: "CrashLoopBackOff", Message: "back-off 5m0s", Restarts: 12,
	}}}
	issues := s.Issues()
	require.Len(t, issues, 1)
	assert.Equal(t, "pod/prod/api-abc/CrashLoopBackOff", issues[0].Key)
	assert.Equal(t, SeverityMajor, issues[0].Severity)
	assert.Contains(t, issues[0].Message, "12 restarts")
	assert.Contains(t, issues[0].Message, "node work-01")
	assert.Contains(t, issues[0].Message, "back-off 5m0s")
}

func TestIssuesFlagsStorage(t *testing.T) {
	s := &Snapshot{Storage: StorageInfo{Total: 4, Bound: 2, Pending: 1, Lost: 1}}
	issues := s.Issues()
	require.Len(t, issues, 2)
	assert.Equal(t, "pvc/cluster/lost", issues[0].Key, "Lost outranks Pending")
	assert.Equal(t, SeverityCritical, issues[0].Severity)
	assert.Equal(t, "pvc/cluster/pending", issues[1].Key)
}

func TestIssuesHealthyClusterHasNone(t *testing.T) {
	s := &Snapshot{
		Nodes:     []NodeInfo{{Name: "n1", Ready: true}},
		Workloads: []WorkloadInfo{{Kind: "Deployment", Name: "api", Status: "operational"}},
		Storage:   StorageInfo{Total: 3, Bound: 3},
	}
	assert.Empty(t, s.Issues())
}

func TestIssueKeysAreStableAcrossReconciles(t *testing.T) {
	build := func(restarts int32) []Issue {
		return (&Snapshot{Problems: []ProblemPod{{
			Namespace: "prod", Name: "api", Reason: "CrashLoopBackOff", Restarts: restarts,
		}}}).Issues()
	}
	// The restart count moves every reconcile; the key must not, or the same
	// failure would re-alert forever.
	assert.Equal(t, build(3)[0].Key, build(99)[0].Key)
	assert.NotEqual(t, build(3)[0].Message, build(99)[0].Message)
}

func TestSeverityRank(t *testing.T) {
	assert.Greater(t, SeverityRank(SeverityCritical), SeverityRank(SeverityMajor))
	assert.Greater(t, SeverityRank(SeverityMajor), SeverityRank(SeverityMinor))
	assert.Equal(t, SeverityRank(SeverityMinor), SeverityRank("typo"),
		"an unrecognised severity must not silently escalate paging")
	assert.Equal(t, SeverityRank(SeverityCritical), SeverityRank("  CRITICAL "))
}

func TestIssueTitle(t *testing.T) {
	assert.Equal(t, "Deployment prod/api: NoReadyReplicas",
		Issue{Kind: "Deployment", Namespace: "prod", Name: "api", Reason: "NoReadyReplicas"}.Title())
	assert.Equal(t, "Node work-02: NotReady",
		Issue{Kind: "Node", Name: "work-02", Reason: "NotReady"}.Title())
}

func TestPodSeverityTreatsImageErrorsAsMajor(t *testing.T) {
	for _, r := range []string{"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
		"CreateContainerConfigError", "Unschedulable"} {
		assert.Equal(t, SeverityMajor, podSeverity(r), "reason %q", r)
	}
	assert.Equal(t, SeverityMinor, podSeverity("NotReady"))
}
