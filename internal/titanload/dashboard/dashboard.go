// Package dashboard renders titanload's live terminal UI (ANSI, no curses
// dependency) or plain progress lines when stdout is not a terminal.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/titanedge/titanedge/internal/titanload/report"
	"github.com/titanedge/titanedge/internal/titanload/stats"
)

// WorkerView is one remote worker as seen by the coordinator.
type WorkerView struct {
	Name     string
	State    string
	Snapshot *stats.Snapshot
}

// Source is anything that produces cumulative snapshots: a local engine or
// the distributed coordinator.
type Source interface {
	Snapshot() *stats.Snapshot
	Workers() []WorkerView
	Done() <-chan struct{}
}

// Options configure rendering.
type Options struct {
	Info        report.RunInfo
	Duration    time.Duration
	MaxRequests uint64
	SLO         report.SLO
	Interactive bool
	Interval    time.Duration
	Out         io.Writer
	Exporter    *Exporter
}

const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	cyan    = "\x1b[36m"
	magenta = "\x1b[35m"
)

var sparks = []rune("▁▂▃▄▅▆▇█")

// IsTerminal reports whether stdout is an interactive terminal.
func IsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

type state struct {
	opts       Options
	prev       *stats.Snapshot
	prevAt     time.Time
	prevWorker map[string]*stats.Snapshot
	rpsHist    []float64
	p99Hist    []float64
	peak       float64
}

// Run renders until src is done or ctx is cancelled.
func Run(ctx context.Context, src Source, opts Options) {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
		if !opts.Interactive {
			opts.Interval = time.Second
		}
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	st := &state{opts: opts, prevWorker: map[string]*stats.Snapshot{}, prevAt: time.Now()}

	if opts.Interactive {
		enableVT()
		fmt.Fprint(opts.Out, "\x1b[?1049h\x1b[?25l") // alt screen, hide cursor
		defer fmt.Fprint(opts.Out, "\x1b[?25h\x1b[?1049l")
	}

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-src.Done():
			// A final sliver of an interval would show a misleading RPS;
			// the summary that follows covers the tail anyway.
			if time.Since(st.prevAt) >= opts.Interval/2 {
				st.tick(src)
			}
			return
		case <-ticker.C:
			st.tick(src)
		}
	}
}

func (s *state) tick(src Source) {
	now := time.Now()
	total := src.Snapshot()
	interval := total.Sub(s.prev)
	dt := now.Sub(s.prevAt).Seconds()
	rps := 0.0
	if dt > 0 {
		rps = float64(interval.Requests) / dt
	}
	s.peak = max(s.peak, rps)
	p99 := interval.Percentile(0.99)
	s.rpsHist = push(s.rpsHist, rps, 120)
	s.p99Hist = push(s.p99Hist, float64(p99), 120)

	workers := src.Workers()
	if s.opts.Exporter != nil {
		s.opts.Exporter.Update(total, interval, rps, len(workers))
	}

	if s.opts.Interactive {
		s.render(total, interval, rps, workers, now, dt)
	} else {
		fmt.Fprintf(s.opts.Out, "[%s] rps=%s p50=%s p95=%s p99=%s err=%.3f%% total=%s\n",
			clock(total.Elapsed), report.Num(rps),
			report.FmtDur(interval.Percentile(.5)), report.FmtDur(interval.Percentile(.95)),
			report.FmtDur(p99), total.ErrorRate()*100, report.Num(float64(total.Requests)))
	}

	s.prev, s.prevAt = total, now
	for _, w := range workers {
		s.prevWorker[w.Name] = w.Snapshot
	}
}

func (s *state) render(total, iv *stats.Snapshot, rps float64, workers []WorkerView, now time.Time, dt float64) {
	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
		width = w
	}
	o := s.opts
	var b strings.Builder
	ln := func(format string, a ...any) {
		fmt.Fprintf(&b, format, a...)
		b.WriteString("\x1b[K\n")
	}
	rule := dim + strings.Repeat("─", min(width-1, 110)) + reset

	b.WriteString("\x1b[H")
	title := fmt.Sprintf("%s%s TITANLOAD %s", bold, cyan, reset)
	ln("%s %s▸%s %s  %s[%s]%s", title, dim, reset, o.Info.Target, magenta, o.Info.Mode, reset)
	target := "unlimited"
	if o.Info.Rate > 0 {
		target = report.Num(o.Info.Rate) + " rps"
	}
	workersTxt := ""
	if o.Info.Workers > 1 || len(workers) > 0 {
		workersTxt = fmt.Sprintf("  workers %d", max(o.Info.Workers, len(workers)))
	}
	ln(" concurrency %d  target %s%s  elapsed %s%s%s", o.Info.Concurrency, target, workersTxt, bold, clock(total.Elapsed), reset)
	ln("%s", rule)

	errColor := green
	switch er := total.ErrorRate(); {
	case er > 0.01:
		errColor = red
	case er > 0.001:
		errColor = yellow
	}
	ln(" %sRPS%s        %s%12s%s   avg %s   peak %s", bold, reset, green+bold, report.Num(rps), reset,
		report.Num(total.RPS()), report.Num(s.peak))
	ln(" %sRequests%s   %12s   ok %s   failed %s%s (%.3f%%)%s", bold, reset,
		report.Num(float64(total.Requests)), report.Num(float64(total.Requests-total.Failures)),
		errColor, report.Num(float64(total.Failures)), total.ErrorRate()*100, reset)
	if dt > 0 {
		ln(" %sThroughput%s %12s/s", bold, reset, report.Bytes(float64(iv.Bytes)/dt))
	}
	ln("%s", rule)

	p99Color := green
	if o.SLO.P99 > 0 && iv.Percentile(.99) > o.SLO.P99 {
		p99Color = red
	}
	ln(" %sLatency (last %.1fs)%s", bold, dt, reset)
	ln("   p50 %-10s p90 %-10s p95 %-10s %sp99 %-10s%s p99.9 %-10s",
		report.FmtDur(iv.Percentile(.50)), report.FmtDur(iv.Percentile(.90)), report.FmtDur(iv.Percentile(.95)),
		p99Color, report.FmtDur(iv.Percentile(.99)), reset, report.FmtDur(iv.Percentile(.999)))
	ln(" %sLatency (overall)%s", bold, reset)
	ln("   min %-10s mean %-9s p99 %-10s max %-10s",
		report.FmtDur(total.Min()), report.FmtDur(total.Mean()), report.FmtDur(total.Percentile(.99)), report.FmtDur(total.Max()))
	ln("%s", rule)

	sparkW := min(width-12, 100)
	ln(" %sRPS%s   %s%s%s", bold, reset, green, sparkline(s.rpsHist, sparkW), reset)
	ln(" %sp99%s   %s%s%s", bold, reset, yellow, sparkline(s.p99Hist, sparkW), reset)
	ln("%s", rule)

	ln(" %sDistribution%s", bold, reset)
	for _, row := range distribution(total, min(width-30, 60)) {
		ln("   %s", row)
	}
	ln("%s", rule)

	ln(" %sStatus%s  2xx %s%s%s  3xx %s  4xx %s  5xx %s%s%s  transport %s",
		bold, reset, green, report.Num(float64(total.Classes[stats.Class2xx])), reset,
		report.Num(float64(total.Classes[stats.Class3xx])), report.Num(float64(total.Classes[stats.Class4xx])),
		red, report.Num(float64(total.Classes[stats.Class5xx])), reset,
		report.Num(float64(total.Classes[stats.ClassOther])))
	if kinds := total.ErrKindsSorted(); len(kinds) > 0 {
		parts := make([]string, 0, 5)
		for i, kv := range kinds {
			if i == 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s=%s", kv.K, report.Num(float64(kv.V))))
		}
		ln(" %sErrors%s  %s%s%s", bold, reset, red, strings.Join(parts, "  "), reset)
	}

	if len(workers) > 0 {
		ln("%s", rule)
		ln(" %s%-22s %-10s %12s %10s %10s %8s%s", bold, "WORKER", "STATE", "RPS", "p99", "requests", "err%", reset)
		for i, w := range workers {
			if i == 20 {
				ln("   … %d more", len(workers)-20)
				break
			}
			wrps := 0.0
			wp99 := time.Duration(0)
			if w.Snapshot != nil {
				d := w.Snapshot.Sub(s.prevWorker[w.Name])
				if dt > 0 {
					wrps = float64(d.Requests) / dt
				}
				wp99 = d.Percentile(.99)
				ln(" %-22s %-10s %12s %10s %10s %7.2f%%", trunc(w.Name, 22), w.State, report.Num(wrps),
					report.FmtDur(wp99), report.Num(float64(w.Snapshot.Requests)), w.Snapshot.ErrorRate()*100)
			} else {
				ln(" %-22s %-10s %12s %10s %10s %8s", trunc(w.Name, 22), w.State, "-", "-", "-", "-")
			}
		}
	}

	ln("%s", rule)
	ln(" %s", progress(total, o, now, min(width-20, 60)))
	ln(" %sCtrl+C to stop early%s", dim, reset)
	b.WriteString("\x1b[J")
	fmt.Fprint(o.Out, b.String())
}

func progress(total *stats.Snapshot, o Options, _ time.Time, w int) string {
	frac := 0.0
	switch {
	case o.Duration > 0:
		frac = total.Elapsed.Seconds() / o.Duration.Seconds()
	case o.MaxRequests > 0:
		frac = float64(total.Requests) / float64(o.MaxRequests)
	}
	frac = min(max(frac, 0), 1)
	filled := int(frac * float64(w))
	return fmt.Sprintf("%s%s%s%s %3.0f%%", cyan, strings.Repeat("█", filled), strings.Repeat("░", w-filled), reset, frac*100)
}

var distEdges = []struct {
	label string
	upper time.Duration
}{
	{"<1ms", time.Millisecond}, {"1-5ms", 5 * time.Millisecond}, {"5-10ms", 10 * time.Millisecond},
	{"10-50ms", 50 * time.Millisecond}, {"50-100ms", 100 * time.Millisecond},
	{"100-500ms", 500 * time.Millisecond}, {"0.5-1s", time.Second}, {">1s", time.Duration(1 << 62)},
}

func distribution(sn *stats.Snapshot, w int) []string {
	counts := make([]uint64, len(distEdges))
	var total uint64
	for i, c := range sn.Hist {
		if c == 0 {
			continue
		}
		upper := time.Duration(stats.BucketUpper(i) * float64(time.Microsecond))
		for j, e := range distEdges {
			if upper <= e.upper {
				counts[j] += c
				break
			}
		}
		total += c
	}
	rows := make([]string, 0, len(distEdges))
	for j, e := range distEdges {
		frac := 0.0
		if total > 0 {
			frac = float64(counts[j]) / float64(total)
		}
		bar := int(frac * float64(w))
		rows = append(rows, fmt.Sprintf("%-10s %s%s%s %6.2f%%", e.label, cyan, strings.Repeat("■", bar), reset, frac*100))
	}
	return rows
}

func sparkline(vals []float64, w int) string {
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	hi := 0.0
	for _, v := range vals {
		hi = max(hi, v)
	}
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if hi > 0 {
			i = int(v / hi * float64(len(sparks)-1))
		}
		b.WriteRune(sparks[i])
	}
	return b.String()
}

func push(s []float64, v float64, limit int) []float64 {
	s = append(s, v)
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	return s
}

func clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
