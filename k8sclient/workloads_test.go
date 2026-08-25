package k8sclient

import (
	"testing"
	"time"

	"github.com/status/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func deploy(ns, name, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}},
		}},
	}
}

func names(svcs []config.Service) []string {
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = s.Name
	}
	return out
}

func TestDiscoverWorkloadsCoversAllThreeKinds(t *testing.T) {
	c := newTestClient(t,
		deploy("prod", "api", "api:1.0"),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cni", Namespace: "kube-system"}},
	)
	got, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"prod/api (Deployment)",
		"prod/db (StatefulSet)",
		"kube-system/cni (DaemonSet)",
	}, names(got))
}

func TestDiscoverWorkloadsMapsProbeTypeAndTarget(t *testing.T) {
	c := newTestClient(t, deploy("invinsense", "uec-ingest", "harbor/uec:2.1"))
	got, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{Interval: 45 * time.Second})
	require.NoError(t, err)
	require.Len(t, got, 1)

	s := got[0]
	assert.Equal(t, config.CheckK8sDeployment, s.Type)
	assert.Equal(t, "invinsense", s.K8sNamespace)
	assert.Equal(t, "uec-ingest", s.K8sName)
	assert.Equal(t, "invinsense", s.Group)
	assert.Equal(t, 45*time.Second, s.Interval)
	assert.Contains(t, s.Description, "harbor/uec:2.1", "the image belongs in the description")
}

func TestDiscoverWorkloadsRespectsNamespaceFilters(t *testing.T) {
	objs := []any{
		deploy("prod", "api", "i"),
		deploy("staging", "api", "i"),
		deploy("kube-system", "coredns", "i"),
	}
	c := newTestClient(t, objs...)

	only, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{Namespaces: []string{"PROD"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"prod/api (Deployment)"}, names(only), "include list is case-insensitive")

	excl, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{
		ExcludeNamespaces: []string{"kube-system", "staging"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"prod/api (Deployment)"}, names(excl))

	// Exclude wins over include for the same namespace.
	both, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{
		Namespaces:        []string{"prod"},
		ExcludeNamespaces: []string{"prod"},
	})
	require.NoError(t, err)
	assert.Empty(t, both, "an excluded namespace stays excluded even if also included")
}

func TestDiscoverWorkloadsRespectsKindFilter(t *testing.T) {
	c := newTestClient(t,
		deploy("prod", "api", "i"),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"}},
	)
	got, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{Kinds: []string{"statefulset"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"prod/db (StatefulSet)"}, names(got))
}

func TestDiscoverWorkloadsCapsGeneration(t *testing.T) {
	var objs []any
	for i := 0; i < 40; i++ {
		objs = append(objs, deploy("prod", "app-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "i"))
	}
	c := newTestClient(t, objs...)
	got, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{MaxProbes: 5})
	require.NoError(t, err)
	assert.Len(t, got, 5, "the cap must actually bound generation")
}

func TestDiscoverWorkloadsGroupPrefix(t *testing.T) {
	c := newTestClient(t, deploy("prod", "api", "i"))
	got, err := c.DiscoverWorkloads(WorkloadDiscoveryOptions{GroupPrefix: "k8s/"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "k8s/prod", got[0].Group)
}

func TestWorkloadProbeNameIsStable(t *testing.T) {
	// The name is the reconcile key: it must depend only on identity, never on
	// mutable fields like the image, or every deploy would orphan a probe.
	a := WorkloadProbeName("Deployment", "prod", "api")
	b := WorkloadProbeName("Deployment", "prod", "api")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, WorkloadProbeName("StatefulSet", "prod", "api"))
	assert.NotEqual(t, a, WorkloadProbeName("Deployment", "staging", "api"))
}

func TestWorkloadMetaHandlesEachKind(t *testing.T) {
	ns, name, img, ok := workloadMeta(deploy("prod", "api", "i:1"))
	assert.True(t, ok)
	assert.Equal(t, []string{"prod", "api", "i:1"}, []string{ns, name, img})

	_, _, _, ok = workloadMeta(&corev1.Pod{})
	assert.False(t, ok, "a Pod is not a workload the discovery handles")
}
