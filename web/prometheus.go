package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/status/monitor"
)

// handlePrometheus exports probe state in Prometheus text-format (v0.0.4).
// No external dep — small surface, stable output, OpenMetrics-compatible.
//
// Exposed series:
//   status_probe_up{name,group,type}              1 operational | 0 degraded/down/unknown
//   status_probe_status{name,group,type}          1 op, 2 degraded, 3 down, 0 unknown
//   status_probe_response_time_ms{name,group}     last observed latency
//   status_probe_uptime_ratio{name,group}         rolling uptime % / 100
//   status_probe_status_code{name,group}          last HTTP/DB status code, if any
//   status_overall_up                              cluster rollup, 1/0
func (s *Server) handlePrometheus(w http.ResponseWriter, _ *http.Request) {
	statuses := s.monitor.GetAllStatuses()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	var b strings.Builder
	b.WriteString("# HELP status_probe_up Probe operational state (1=op, 0=otherwise)\n")
	b.WriteString("# TYPE status_probe_up gauge\n")
	for _, st := range statuses {
		labels := promLabels(st)
		v := 0
		if st.Status == monitor.StatusOperational {
			v = 1
		}
		fmt.Fprintf(&b, "status_probe_up{%s} %d\n", labels, v)
	}

	b.WriteString("# HELP status_probe_status Probe status code: 1=op 2=degraded 3=down 0=unknown\n")
	b.WriteString("# TYPE status_probe_status gauge\n")
	for _, st := range statuses {
		fmt.Fprintf(&b, "status_probe_status{%s} %d\n", promLabels(st), statusCode(st.Status))
	}

	b.WriteString("# HELP status_probe_response_time_ms Last observed response latency in ms\n")
	b.WriteString("# TYPE status_probe_response_time_ms gauge\n")
	for _, st := range statuses {
		fmt.Fprintf(&b, "status_probe_response_time_ms{%s} %d\n", promLabels(st), st.ResponseTimeMs)
	}

	b.WriteString("# HELP status_probe_uptime_ratio Rolling uptime (0..1)\n")
	b.WriteString("# TYPE status_probe_uptime_ratio gauge\n")
	for _, st := range statuses {
		fmt.Fprintf(&b, "status_probe_uptime_ratio{%s} %.4f\n", promLabels(st), st.Uptime/100.0)
	}

	b.WriteString("# HELP status_probe_status_code Last HTTP/DB status code from probe\n")
	b.WriteString("# TYPE status_probe_status_code gauge\n")
	for _, st := range statuses {
		fmt.Fprintf(&b, "status_probe_status_code{%s} %d\n", promLabels(st), st.StatusCode)
	}

	b.WriteString("# HELP status_probe_total Total probes registered\n")
	b.WriteString("# TYPE status_probe_total gauge\n")
	fmt.Fprintf(&b, "status_probe_total %d\n", len(statuses))

	b.WriteString("# HELP status_overall_up Overall page state (1=operational, 0=otherwise)\n")
	b.WriteString("# TYPE status_overall_up gauge\n")
	overall := 0
	if s.monitor.GetOverallStatus() == monitor.StatusOperational {
		overall = 1
	}
	fmt.Fprintf(&b, "status_overall_up %d\n", overall)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func promLabels(st *monitor.ServiceStatus) string {
	return fmt.Sprintf(`name=%q,group=%q`, st.Name, st.Group)
}

func statusCode(s monitor.Status) int {
	switch s {
	case monitor.StatusOperational:
		return 1
	case monitor.StatusDegraded:
		return 2
	case monitor.StatusDown:
		return 3
	}
	return 0
}
