// Package report renders final titanload results and evaluates SLO gates so
// titanload can fail a CI stage (exit code 2) when performance regresses.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/titanedge/titanedge/internal/titanload/stats"
)

// SLO thresholds. Zero values are ignored.
type SLO struct {
	P95       time.Duration
	P99       time.Duration
	ErrorRate float64 // fraction, e.g. 0.01 == 1%
	MinRPS    float64
}

// Check returns human-readable violations.
func (s SLO) Check(sn *stats.Snapshot) []string {
	var v []string
	if s.P95 > 0 {
		if got := sn.Percentile(0.95); got > s.P95 {
			v = append(v, fmt.Sprintf("p95 %s > %s", FmtDur(got), FmtDur(s.P95)))
		}
	}
	if s.P99 > 0 {
		if got := sn.Percentile(0.99); got > s.P99 {
			v = append(v, fmt.Sprintf("p99 %s > %s", FmtDur(got), FmtDur(s.P99)))
		}
	}
	if s.ErrorRate > 0 && sn.ErrorRate() > s.ErrorRate {
		v = append(v, fmt.Sprintf("error rate %.3f%% > %.3f%%", sn.ErrorRate()*100, s.ErrorRate*100))
	}
	if s.MinRPS > 0 && sn.RPS() < s.MinRPS {
		v = append(v, fmt.Sprintf("rps %.0f < %.0f", sn.RPS(), s.MinRPS))
	}
	return v
}

// Enabled reports whether any threshold is set.
func (s SLO) Enabled() bool { return s.P95 > 0 || s.P99 > 0 || s.ErrorRate > 0 || s.MinRPS > 0 }

// Latency in milliseconds.
type Latency struct {
	Min  float64 `json:"min_ms"`
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P90  float64 `json:"p90_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	P999 float64 `json:"p999_ms"`
	Max  float64 `json:"max_ms"`
}

// Summary is the machine-readable result (--json).
type Summary struct {
	Target        string            `json:"target"`
	Mode          string            `json:"mode"`
	Workers       int               `json:"workers"`
	Concurrency   int               `json:"concurrency"`
	TargetRate    float64           `json:"target_rate"`
	DurationSec   float64           `json:"duration_seconds"`
	Requests      uint64            `json:"requests"`
	Failures      uint64            `json:"failures"`
	ErrorRate     float64           `json:"error_rate"`
	RPS           float64           `json:"rps"`
	BytesPerSec   float64           `json:"bytes_per_second"`
	Latency       Latency           `json:"latency"`
	StatusClasses map[string]uint64 `json:"status_classes"`
	Errors        map[string]uint64 `json:"errors,omitempty"`
	SLOPassed     bool              `json:"slo_passed"`
	Violations    []string          `json:"slo_violations,omitempty"`
	FinishedAt    time.Time         `json:"finished_at"`
}

// RunInfo describes the run for display.
type RunInfo struct {
	Target      string
	Mode        string
	Workers     int
	Concurrency int
	Rate        float64
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Build assembles a Summary.
func Build(sn *stats.Snapshot, info RunInfo, slo SLO) Summary {
	violations := slo.Check(sn)
	s := Summary{
		Target: info.Target, Mode: info.Mode, Workers: max(info.Workers, 1),
		Concurrency: info.Concurrency, TargetRate: info.Rate,
		DurationSec: sn.Elapsed.Seconds(),
		Requests:    sn.Requests, Failures: sn.Failures, ErrorRate: sn.ErrorRate(),
		RPS: sn.RPS(),
		Latency: Latency{
			Min: ms(sn.Min()), Mean: ms(sn.Mean()),
			P50: ms(sn.Percentile(.50)), P90: ms(sn.Percentile(.90)), P95: ms(sn.Percentile(.95)),
			P99: ms(sn.Percentile(.99)), P999: ms(sn.Percentile(.999)), Max: ms(sn.Max()),
		},
		StatusClasses: map[string]uint64{
			"1xx": sn.Classes[stats.Class1xx], "2xx": sn.Classes[stats.Class2xx],
			"3xx": sn.Classes[stats.Class3xx], "4xx": sn.Classes[stats.Class4xx],
			"5xx": sn.Classes[stats.Class5xx], "transport_error": sn.Classes[stats.ClassOther],
		},
		Errors:     sn.ErrKinds,
		SLOPassed:  len(violations) == 0,
		Violations: violations,
		FinishedAt: time.Now().UTC(),
	}
	if sn.Elapsed > 0 {
		s.BytesPerSec = float64(sn.Bytes) / sn.Elapsed.Seconds()
	}
	return s
}

// WriteText prints a human summary.
func (s Summary) WriteText(w io.Writer) {
	line := strings.Repeat("─", 64)
	fmt.Fprintf(w, "\n%s\n TITANLOAD SUMMARY\n%s\n", line, line)
	fmt.Fprintf(w, " Target        %s (%s)\n", s.Target, s.Mode)
	fmt.Fprintf(w, " Workers       %d × %d concurrency", s.Workers, s.Concurrency)
	if s.TargetRate > 0 {
		fmt.Fprintf(w, ", target %s rps", Num(s.TargetRate))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, " Duration      %.1fs\n", s.DurationSec)
	fmt.Fprintf(w, " Requests      %s  (failed %s, %.3f%%)\n", Num(float64(s.Requests)), Num(float64(s.Failures)), s.ErrorRate*100)
	fmt.Fprintf(w, " Throughput    %s rps  |  %s/s\n", Num(s.RPS), Bytes(s.BytesPerSec))
	l := s.Latency
	fmt.Fprintf(w, " Latency       min %.2fms  mean %.2fms  max %.2fms\n", l.Min, l.Mean, l.Max)
	fmt.Fprintf(w, "               p50 %.2fms  p90 %.2fms  p95 %.2fms  p99 %.2fms  p99.9 %.2fms\n", l.P50, l.P90, l.P95, l.P99, l.P999)
	fmt.Fprintf(w, " Status        2xx %d  3xx %d  4xx %d  5xx %d  transport %d\n",
		s.StatusClasses["2xx"], s.StatusClasses["3xx"], s.StatusClasses["4xx"], s.StatusClasses["5xx"], s.StatusClasses["transport_error"])
	if len(s.Errors) > 0 {
		fmt.Fprint(w, " Errors       ")
		for k, v := range s.Errors {
			fmt.Fprintf(w, " %s=%d", k, v)
		}
		fmt.Fprintln(w)
	}
	switch {
	case len(s.Violations) > 0:
		fmt.Fprintf(w, " SLO           FAILED: %s\n", strings.Join(s.Violations, "; "))
	default:
		fmt.Fprintln(w, " SLO           passed")
	}
	fmt.Fprintln(w, line)
}

// WriteJSON writes the summary to path ("-" for stdout).
func (s Summary) WriteJSON(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "-" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Num formats large numbers with thousands separators.
func Num(f float64) string {
	n := int64(f + 0.5)
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Bytes formats a byte count.
func Bytes(f float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// FmtDur prints a latency compactly (µs / ms / s).
func FmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}
