package web

import (
	"fmt"
	"html/template"
	"math"
	"time"

	sprig "github.com/Masterminds/sprig/v3"
	"github.com/status/notify"
)

// templateFuncs is the FuncMap shared by every HTML template: the sprig set
// plus a handful of status-page-specific formatters.
//
// sprig.FuncMap() exposes `env` and `expandenv`, and this process holds
// STATUS_API_KEY, STATUS_NTFY_TOKEN and the VAPID private key in its
// environment — a template on a public status page must not be able to read
// any of them. Sprig's Hermetic variant removes those two but also drops
// `date`/`now`, which templates legitimately want, so take the full map and
// delete exactly the two that leak.
func templateFuncs() template.FuncMap {
	f := sprig.FuncMap()
	delete(f, "env")
	delete(f, "expandenv")

	// :fire: → 🔥 anywhere a template renders operator-authored text.
	f["emojify"] = notify.Emojify
	f["statusEmoji"] = notify.StatusEmoji

	f["humanBytes"] = humanBytes
	f["humanMillicores"] = humanMillicores
	f["humanDuration"] = humanDuration
	f["percent"] = percent

	return f
}

// humanBytes renders a byte count the way kubectl does.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// humanMillicores renders CPU as cores once the value is large enough to read.
func humanMillicores(m int64) string {
	if m < 1000 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%.2f", float64(m)/1000)
}

// humanDuration renders an age the way kubectl's AGE column does.
func humanDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// percent computes part/total as a percentage, guarding division by zero.
func percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}
