package k8sclient

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractErrorsKeepsOnlyDiagnosticLines(t *testing.T) {
	in := strings.Join([]string{
		`2026-08-25T04:00:00Z INFO  starting clickhouse-server`,
		`2026-08-25T04:00:01Z DEBUG loading config from /etc/clickhouse`,
		`2026-08-25T04:00:02Z ERROR Cannot open file /var/lib/clickhouse/status: Permission denied`,
		`2026-08-25T04:00:02Z INFO  shutting down`,
	}, "\n")
	got := extractErrors(strings.NewReader(in))
	require.Len(t, got, 1, "only the diagnostic line survives")
	assert.Contains(t, got[0], "Permission denied")
}

func TestExtractErrorsDropsZeroCountNoise(t *testing.T) {
	in := strings.Join([]string{
		`{"level":"info","msg":"healthy","errors":0}`,
		`metrics: error_count=0 latency=12ms`,
		`{"level":"debug","msg":"error handler registered"}`,
	}, "\n")
	assert.Empty(t, extractErrors(strings.NewReader(in)),
		"a counter reporting zero errors is not an error")
}

func TestExtractErrorsDeduplicatesARepeatingCrash(t *testing.T) {
	line := "FATAL: connection refused to postgres:5432"
	in := strings.Repeat(line+"\n", 40)
	got := extractErrors(strings.NewReader(in))
	assert.Len(t, got, 1, "a container repeating one error should say it once")
}

func TestExtractErrorsKeepsTheMostRecent(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("ERROR failure number ")
		b.WriteByte(byte('a' + i))
		b.WriteString("\n")
	}
	got := extractErrors(strings.NewReader(b.String()))
	assert.LessOrEqual(t, len(got), maxExcerptLines)
	assert.Contains(t, got[len(got)-1], string(byte('a'+19)),
		"a crash loop's last words matter most")
}

func TestExtractErrorsTruncatesHugeLines(t *testing.T) {
	got := extractErrors(strings.NewReader("ERROR " + strings.Repeat("x", 5000)))
	require.Len(t, got, 1)
	assert.LessOrEqual(t, len([]rune(got[0])), maxLineRunes+1, "stack traces must not bury the alert")
}

func TestFailingContainerPicksTheOneThatBroke(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		}},
	}
	name, prev := failingContainer(pod)
	assert.Equal(t, "app", name, "not the healthy sidecar listed first")
	assert.True(t, prev, "a crash loop's explanation is in the previous instance")
}

func TestFailingContainerSkipsImagePullFailures(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
	}}}
	name, _ := failingContainer(pod)
	assert.Equal(t, "", name, "nothing ever ran, so there are no logs to read")
}

func TestLogExcerptRendersForAnAlert(t *testing.T) {
	var nilEx *LogExcerpt
	assert.True(t, nilEx.Empty())
	assert.Equal(t, "", nilEx.Text())

	ex := &LogExcerpt{Container: "clickhouse", Previous: true,
		Lines: []string{"Cannot open file", "Permission denied"}}
	assert.Equal(t, "clickhouse (previous): Cannot open file / Permission denied", ex.Text())
}

func TestPodIssueMessageLeadsWithTheError(t *testing.T) {
	withLog := podIssueMessage(ProblemPod{
		Name: "ch-0", Node: "w-05", Restarts: 45,
		LogExcerpt: "clickhouse (previous): Permission denied",
	})
	assert.True(t, strings.HasPrefix(withLog, "clickhouse (previous): Permission denied"),
		"the error goes first; a phone shows the start of the line")
	assert.Contains(t, withLog, "45 restarts")

	without := podIssueMessage(ProblemPod{Name: "ch-0", Node: "w-05", Restarts: 45})
	assert.Contains(t, without, "45 restarts", "state-only message still works")
}
