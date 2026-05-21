package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/status/config"
	"github.com/status/k8sclient"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// k8sCheck dispatches to the right k8s_* probe.
// Caller must have ensured m.k8s != nil.
func (m *Monitor) k8sCheck(svc config.Service) {
	if m.k8s == nil {
		m.updateStatus(svc.Name, StatusDown, 0, 0, "k8s client not initialized")
		return
	}
	switch svc.Type {
	case config.CheckK8sAPIServer:
		m.checkK8sAPIServer(svc)
	case config.CheckK8sAPILatency:
		m.checkK8sAPILatency(svc)
	case config.CheckK8sNodes:
		m.checkK8sNodes(svc)
	case config.CheckK8sDeployment:
		m.checkK8sDeployment(svc)
	case config.CheckK8sStatefulSet:
		m.checkK8sStatefulSet(svc)
	case config.CheckK8sDaemonSet:
		m.checkK8sDaemonSet(svc)
	case config.CheckK8sPodsCrash:
		m.checkK8sPodsCrash(svc)
	case config.CheckK8sPVC:
		m.checkK8sPVC(svc)
	case config.CheckK8sEvents:
		m.checkK8sEvents(svc)
	case config.CheckK8sHPA:
		m.checkK8sHPA(svc)
	case config.CheckK8sCronJob:
		m.checkK8sCronJob(svc)
	default:
		m.updateStatus(svc.Name, StatusDown, 0, 0, "unknown k8s check type")
	}
}

// checkK8sAPIServer probes a non-resource URL on the apiserver: /livez or /readyz.
// Path comes from svc.K8sPath, defaults to /readyz.
func (m *Monitor) checkK8sAPIServer(svc config.Service) {
	path := svc.K8sPath
	if path == "" {
		path = "/readyz"
	}
	timeout := svc.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	start := time.Now()
	res := m.k8s.Clientset.Discovery().RESTClient().Get().AbsPath(path).Do(ctx)
	rt := time.Since(start)

	var code int
	res.StatusCode(&code)
	if err := res.Error(); err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, code, err.Error())
		return
	}
	if code == 200 {
		m.updateStatus(svc.Name, StatusOperational, rt, code, "")
		return
	}
	m.updateStatus(svc.Name, StatusDown, rt, code, fmt.Sprintf("apiserver returned %d", code))
}

// checkK8sAPILatency lists namespaces with limit=1 and measures wall time.
func (m *Monitor) checkK8sAPILatency(svc config.Service) {
	timeout := svc.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	start := time.Now()
	_, err := m.k8s.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	status := StatusOperational
	msg := ""
	switch {
	case rt > 10*time.Second:
		status = StatusDown
		msg = "apiserver list >10s"
	case rt > 2*time.Second:
		status = StatusDegraded
		msg = "apiserver list >2s"
	}
	m.updateStatus(svc.Name, status, rt, 0, msg)
}

// checkK8sNodes aggregates Ready/Pressure across all nodes.
func (m *Monitor) checkK8sNodes(svc config.Service) {
	start := time.Now()
	nodes, err := m.k8s.Nodes.List(labels.Everything())
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	total := len(nodes)
	if total == 0 {
		m.updateStatus(svc.Name, StatusDown, rt, 0, "no nodes")
		return
	}
	var ready, pressure int
	var notReadyNames []string
	for _, n := range nodes {
		rOk, p := nodeConditionSummary(n)
		if rOk {
			ready++
		} else {
			notReadyNames = append(notReadyNames, n.Name)
		}
		if p {
			pressure++
		}
	}
	pct := float64(ready) / float64(total) * 100
	thresh := svc.K8sReadyPct
	if thresh == 0 {
		thresh = 90
	}
	status := StatusOperational
	msg := fmt.Sprintf("%d/%d Ready (%.0f%%)", ready, total, pct)
	if pressure > 0 {
		status = StatusDegraded
		msg += fmt.Sprintf("; %d under pressure", pressure)
	}
	if pct < thresh {
		status = StatusDegraded
		msg += "; below threshold"
	}
	if pct < thresh*0.75 {
		status = StatusDown
	}
	if len(notReadyNames) > 0 {
		msg += "; NotReady=" + strings.Join(notReadyNames, ",")
	}
	m.updateStatus(svc.Name, status, rt, total, msg)
}

func nodeConditionSummary(n *corev1.Node) (ready, pressure bool) {
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			ready = c.Status == corev1.ConditionTrue
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if c.Status == corev1.ConditionTrue {
				pressure = true
			}
		case corev1.NodeNetworkUnavailable:
			if c.Status == corev1.ConditionTrue {
				ready = false
			}
		}
	}
	return
}

func (m *Monitor) checkK8sDeployment(svc config.Service) {
	ns := svc.K8sNamespace
	if ns == "" {
		ns = "default"
	}
	start := time.Now()
	d, err := m.k8s.Deployments.Deployments(ns).Get(svc.K8sName)
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	ready := d.Status.ReadyReplicas
	avail := d.Status.AvailableReplicas
	status := StatusOperational
	msg := fmt.Sprintf("%d/%d ready, %d available", ready, desired, avail)
	switch {
	case desired == 0:
		// scaled down on purpose
	case ready == 0:
		status = StatusDown
	case avail < desired:
		status = StatusDegraded
	}
	m.updateStatus(svc.Name, status, rt, int(ready), msg)
}

func (m *Monitor) checkK8sStatefulSet(svc config.Service) {
	ns := svc.K8sNamespace
	if ns == "" {
		ns = "default"
	}
	start := time.Now()
	s, err := m.k8s.StatefulSets.StatefulSets(ns).Get(svc.K8sName)
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	ready := s.Status.ReadyReplicas
	status := StatusOperational
	msg := fmt.Sprintf("%d/%d ready", ready, desired)
	switch {
	case desired == 0:
	case ready == 0:
		status = StatusDown
	case ready < desired:
		status = StatusDegraded
	}
	m.updateStatus(svc.Name, status, rt, int(ready), msg)
}

func (m *Monitor) checkK8sDaemonSet(svc config.Service) {
	ns := svc.K8sNamespace
	if ns == "" {
		ns = "default"
	}
	start := time.Now()
	d, err := m.k8s.DaemonSets.DaemonSets(ns).Get(svc.K8sName)
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	desired := d.Status.DesiredNumberScheduled
	ready := d.Status.NumberReady
	status := StatusOperational
	msg := fmt.Sprintf("%d/%d ready", ready, desired)
	switch {
	case desired == 0:
	case ready == 0:
		status = StatusDown
	case ready < desired:
		status = StatusDegraded
	}
	m.updateStatus(svc.Name, status, rt, int(ready), msg)
}

// checkK8sPodsCrash counts pods in CrashLoopBackOff / ImagePullBackOff in a namespace.
func (m *Monitor) checkK8sPodsCrash(svc config.Service) {
	ns := svc.K8sNamespace
	start := time.Now()
	var pods []*corev1.Pod
	var err error
	if ns == "" {
		pods, err = m.k8s.Pods.List(labels.Everything())
	} else {
		pods, err = m.k8s.Pods.Pods(ns).List(labels.Everything())
	}
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	var bad []string
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				r := cs.State.Waiting.Reason
				if r == "CrashLoopBackOff" || r == "ImagePullBackOff" || r == "ErrImagePull" || r == "CreateContainerConfigError" {
					bad = append(bad, p.Namespace+"/"+p.Name+":"+r)
					break
				}
			}
		}
	}
	thresh := svc.K8sCrashThresh
	if thresh == 0 {
		thresh = 1
	}
	count := len(bad)
	status := StatusOperational
	msg := fmt.Sprintf("%d unhealthy pods", count)
	if count >= thresh {
		status = StatusDegraded
		if len(bad) <= 5 {
			msg += ": " + strings.Join(bad, ", ")
		} else {
			msg += ": " + strings.Join(bad[:5], ", ") + ", ..."
		}
	}
	if count >= thresh*3 {
		status = StatusDown
	}
	m.updateStatus(svc.Name, status, rt, count, msg)
}

func (m *Monitor) checkK8sPVC(svc config.Service) {
	ns := svc.K8sNamespace
	start := time.Now()
	var items []*corev1.PersistentVolumeClaim
	var err error
	if ns == "" {
		items, err = m.k8s.PVCs.List(labels.Everything())
	} else {
		items, err = m.k8s.PVCs.PersistentVolumeClaims(ns).List(labels.Everything())
	}
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	var pending, lost int
	for _, p := range items {
		switch p.Status.Phase {
		case corev1.ClaimPending:
			pending++
		case corev1.ClaimLost:
			lost++
		}
	}
	status := StatusOperational
	msg := fmt.Sprintf("%d bound", len(items)-pending-lost)
	if pending > 0 {
		status = StatusDegraded
		msg += fmt.Sprintf("; %d Pending", pending)
	}
	if lost > 0 {
		status = StatusDown
		msg += fmt.Sprintf("; %d Lost", lost)
	}
	m.updateStatus(svc.Name, status, rt, len(items), msg)
}

// checkK8sEvents measures Warning event rate over last 15 min.
func (m *Monitor) checkK8sEvents(svc config.Service) {
	ns := svc.K8sNamespace
	start := time.Now()
	var items []*corev1.Event
	var err error
	if ns == "" {
		items, err = m.k8s.Events.List(labels.Everything())
	} else {
		items, err = m.k8s.Events.Events(ns).List(labels.Everything())
	}
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	cutoff := time.Now().Add(-15 * time.Minute)
	var warnings int
	var samples []string
	for _, e := range items {
		if e.Type != "Warning" {
			continue
		}
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.EventTime.Time
		}
		if ts.Before(cutoff) {
			continue
		}
		warnings++
		if len(samples) < 3 {
			samples = append(samples, e.Reason+":"+e.InvolvedObject.Name)
		}
	}
	perMin := float64(warnings) / 15.0
	thresh := svc.K8sWarnPerMin
	if thresh == 0 {
		thresh = 2.0
	}
	status := StatusOperational
	msg := fmt.Sprintf("%d warnings/15m (%.2f/min)", warnings, perMin)
	if perMin >= thresh {
		status = StatusDegraded
		if len(samples) > 0 {
			msg += "; " + strings.Join(samples, ", ")
		}
	}
	if perMin >= thresh*3 {
		status = StatusDown
	}
	m.updateStatus(svc.Name, status, rt, warnings, msg)
}

func (m *Monitor) checkK8sHPA(svc config.Service) {
	ns := svc.K8sNamespace
	if ns == "" {
		ns = "default"
	}
	start := time.Now()
	h, err := m.k8s.HPAs.HorizontalPodAutoscalers(ns).Get(svc.K8sName)
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	cur := h.Status.CurrentReplicas
	desired := h.Status.DesiredReplicas
	status := StatusOperational
	msg := fmt.Sprintf("%d current / %d desired", cur, desired)
	if cur != desired {
		status = StatusDegraded
	}
	if desired > 0 && cur == 0 {
		status = StatusDown
	}
	m.updateStatus(svc.Name, status, rt, int(cur), msg)
}

func (m *Monitor) checkK8sCronJob(svc config.Service) {
	ns := svc.K8sNamespace
	if ns == "" {
		ns = "default"
	}
	start := time.Now()
	cj, err := m.k8s.CronJobs.CronJobs(ns).Get(svc.K8sName)
	rt := time.Since(start)
	if err != nil {
		m.updateStatus(svc.Name, StatusDown, rt, 0, err.Error())
		return
	}
	maxAge := 24 * time.Hour
	if svc.K8sMaxAge != "" {
		if d, err := time.ParseDuration(svc.K8sMaxAge); err == nil {
			maxAge = d
		}
	}
	if cj.Status.LastSuccessfulTime == nil {
		m.updateStatus(svc.Name, StatusDegraded, rt, 0, "no successful run yet")
		return
	}
	age := time.Since(cj.Status.LastSuccessfulTime.Time)
	status := StatusOperational
	msg := fmt.Sprintf("last success %s ago", age.Truncate(time.Second))
	if age > maxAge {
		status = StatusDegraded
		msg += fmt.Sprintf(" (>%s)", maxAge)
	}
	if age > maxAge*3 {
		status = StatusDown
	}
	m.updateStatus(svc.Name, status, rt, 0, msg)
}

// Compile-time guard: ensures k8sclient package is in the module graph.
var _ = (*k8sclient.Client)(nil)
