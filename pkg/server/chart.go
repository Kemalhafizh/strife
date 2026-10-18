package server

import (
	"fmt"
	"html/template"
	"math"
	"strings"

	"github.com/kemalhafizh/strife/pkg/checker"
)

// Charts are inline SVG rendered on the server: the dashboard runs no
// script, and its content security policy loads nothing. Colors come from
// the page's CSS classes, so they follow its light and dark themes.
// Verdicts are never told apart by color alone: a violation's column is
// tall, an inconclusive one half as tall, a passing one a stub, which
// reads the same in grayscale and to color-blind readers.

// trendWindow is how many runs of a series its charts show.
const trendWindow = 60

const (
	plotW   = 640 // the width runs share, between slotMin and slotMax each
	slotMin = 8
	slotMax = 26 // a 24px column and its 2px gap
	padL    = 44 // y-axis labels
	padR    = 64 // the last value's label
	padTop  = 18
	padBot  = 24 // x-axis labels
	stripH  = 56 // plot height of the verdict chart
	valueH  = 120
	tickPad = 6
)

// layout spreads n runs over plotW: each gets a slot, and a column 2px
// narrower (the surface gap), never wider than 24px.
func layout(n int) (slot, col float64) {
	slot = math.Max(slotMin, math.Min(slotMax, plotW/float64(max(n, 1))))
	return slot, math.Min(24, slot-2)
}

// verdictHeight is the column height of a verdict, as a share of the plot.
func verdictHeight(v checker.Verdict) float64 {
	switch v {
	case checker.Violation:
		return 1
	case checker.Unknown:
		return 0.55
	}
	return 0.22
}

// verdictClass names a verdict's mark class; anything unexpected is
// "unknown" rather than raw pushed text.
func verdictClass(v checker.Verdict) string {
	switch v {
	case checker.Linearizable, checker.Violation:
		return "m-" + string(v)
	}
	return "m-unknown"
}

// column draws a bar from the baseline up to y, rounded at the data end
// only (4px), square at the baseline.
func column(b *strings.Builder, class string, x, y, w, base float64) {
	h := base - y
	r := math.Min(4, math.Min(w/2, h))
	if h <= 0 {
		return
	}
	fmt.Fprintf(b, `<path class="%s" d="M%.1f %.1fV%.1fQ%.1f %.1f %.1f %.1fH%.1fQ%.1f %.1f %.1f %.1fV%.1fZ"/>`,
		class, x, base, y+r, x, y, x+r, y, x+w-r, x+w, y, x+w, y+r, base)
}

func runTitle(r *Run) string {
	parts := []string{r.Received.UTC().Format("2006-01-02 15:04"), string(r.Verdict)}
	if r.Kind == "campaign" {
		parts = append(parts, fmt.Sprintf("%d of %d seeds violating", r.Violations, r.Seeds))
	}
	if r.Change != "" {
		parts = append(parts, r.Change)
	}
	if r.Commit != "" {
		parts = append(parts, "commit "+clip(r.Commit, 12))
	}
	return template.HTMLEscapeString(strings.Join(parts, " · "))
}

func svgOpen(b *strings.Builder, n, plotH int, label string) {
	slot, _ := layout(n)
	w := padL + int(math.Ceil(float64(max(n, 1))*slot)) + padR
	h := padTop + plotH + padBot
	fmt.Fprintf(b, `<svg class="chart" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`,
		w, h, w, h, template.HTMLEscapeString(label))
}

// xAxis draws the baseline and when the first and the last run arrived:
// their dates, or their times when both came the same day.
func xAxis(b *strings.Builder, runs []*Run, base float64) {
	slot, _ := layout(len(runs))
	right := padL + float64(len(runs))*slot
	fmt.Fprintf(b, `<line class="ax" x1="%d" y1="%.1f" x2="%.1f" y2="%.1f"/>`, padL, base+0.5, right, base+0.5)
	if len(runs) == 0 {
		return
	}
	first, last := runs[0].Received.UTC(), runs[len(runs)-1].Received.UTC()
	layoutFmt := "Jan 2"
	if first.Format("2006-01-02") == last.Format("2006-01-02") {
		layoutFmt = "15:04 UTC"
	}
	y := base + 16
	fmt.Fprintf(b, `<text class="tk" x="%d" y="%.1f">%s</text>`, padL, y, first.Format(layoutFmt))
	if len(runs) > 1 {
		fmt.Fprintf(b, `<text class="tk" x="%.1f" y="%.1f" text-anchor="end">%s</text>`, right, y, last.Format(layoutFmt))
	}
}

// verdictChart shows each run of a series, oldest left.
func verdictChart(runs []*Run) template.HTML {
	var b strings.Builder
	svgOpen(&b, len(runs), stripH, "Verdict of each run, oldest first")
	base := float64(padTop + stripH)
	slot, col := layout(len(runs))
	for i, r := range runs {
		x := padL + float64(i)*slot
		top := base - verdictHeight(r.Verdict)*stripH
		fmt.Fprintf(&b, `<g><title>%s</title><rect class="hit" x="%.1f" y="%d" width="%.1f" height="%d"/>`, runTitle(r), x, padTop, slot, stripH)
		column(&b, verdictClass(r.Verdict), x, top, col, base)
		b.WriteString(`</g>`)
	}
	xAxis(&b, runs, base)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// niceCeil rounds v up to 1, 2 or 5 times a power of ten.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

// compact writes 1284 as 1.3K and 4200000 as 4.2M.
func compact(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e6:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v/1e6), ".0") + "M"
	case a >= 1e3:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v/1e3), ".0") + "K"
	}
	return fmt.Sprintf("%.0f", v)
}

// yAxis draws gridlines at 0 and at top, labelled.
func yAxis(b *strings.Builder, n int, top float64, base float64) {
	slot, _ := layout(n)
	right := padL + float64(n)*slot
	fmt.Fprintf(b, `<line class="grid" x1="%d" y1="%d.5" x2="%.1f" y2="%d.5"/>`, padL, padTop, right, padTop)
	fmt.Fprintf(b, `<text class="tk" x="%d" y="%d" text-anchor="end">%s</text>`, padL-tickPad, padTop+4, compact(top))
	fmt.Fprintf(b, `<text class="tk" x="%d" y="%.1f" text-anchor="end">0</text>`, padL-tickPad, base+4)
}

// barChart draws one value per run as columns, the last one labelled.
func barChart(runs []*Run, value func(*Run) float64, label string) template.HTML {
	var b strings.Builder
	svgOpen(&b, len(runs), valueH, label)
	base := float64(padTop + valueH)
	top := 0.0
	for _, r := range runs {
		top = math.Max(top, value(r))
	}
	top = niceCeil(top)
	yAxis(&b, len(runs), top, base)
	slot, col := layout(len(runs))
	for i, r := range runs {
		x := padL + float64(i)*slot
		y := base - value(r)/top*valueH
		fmt.Fprintf(&b, `<g><title>%s</title><rect class="hit" x="%.1f" y="%d" width="%.1f" height="%d"/>`, runTitle(r), x, padTop, slot, valueH)
		column(&b, "acc", x, y, col, base)
		b.WriteString(`</g>`)
	}
	if n := len(runs); n > 0 {
		last := runs[n-1]
		x := padL + float64(n-1)*slot + col + 6
		y := math.Min(base-value(last)/top*valueH+4, base-2)
		fmt.Fprintf(&b, `<text class="lbl" x="%.1f" y="%.1f">%s</text>`, x, y, compact(value(last)))
	}
	xAxis(&b, runs, base)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// lineChart draws one value per run as a line over a light wash, with the
// last point marked and labelled.
func lineChart(runs []*Run, value func(*Run) float64, label string) template.HTML {
	var b strings.Builder
	svgOpen(&b, len(runs), valueH, label)
	base := float64(padTop + valueH)
	top := 0.0
	for _, r := range runs {
		top = math.Max(top, value(r))
	}
	top = niceCeil(top)
	yAxis(&b, len(runs), top, base)
	slot, col := layout(len(runs))
	pt := func(i int) (float64, float64) {
		return padL + float64(i)*slot + col/2, base - value(runs[i])/top*valueH
	}
	if n := len(runs); n > 0 {
		var line strings.Builder
		for i := range runs {
			x, y := pt(i)
			fmt.Fprintf(&line, "%.1f,%.1f ", x, y)
		}
		x0, _ := pt(0)
		xn, yn := pt(n - 1)
		fmt.Fprintf(&b, `<polygon class="wash" points="%.1f,%.1f %s%.1f,%.1f"/>`, x0, base, line.String(), xn, base)
		fmt.Fprintf(&b, `<polyline class="accl" points="%s"/>`, strings.TrimSpace(line.String()))
		for i, r := range runs {
			x, y := pt(i)
			fmt.Fprintf(&b, `<g><title>%s · %s</title><circle class="hit" cx="%.1f" cy="%.1f" r="12"/></g>`, runTitle(r), compact(value(r)), x, y)
		}
		fmt.Fprintf(&b, `<circle class="acc ring" cx="%.1f" cy="%.1f" r="4"/>`, xn, yn)
		fmt.Fprintf(&b, `<text class="lbl" x="%.1f" y="%.1f">%s</text>`, xn+10, yn+4, compact(value(runs[n-1])))
	}
	xAxis(&b, runs, base)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// verdictStrip is the series table's small version of verdictChart, newest
// first, with the same heights.
func verdictStrip(verdicts []checker.Verdict) template.HTML {
	const slot, w, h = 7, 5, 16
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="strip" width="%d" height="%d" viewBox="0 0 %d %d" aria-hidden="true">`, max(len(verdicts), 1)*slot, h, max(len(verdicts), 1)*slot, h)
	for i, v := range verdicts {
		x := float64(i * slot)
		top := h - verdictHeight(v)*h
		fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%.1f" rx="1"><title>%s</title></rect>`,
			verdictClass(v), x, top, w, h-top, template.HTMLEscapeString(string(v)))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// legendSwatch is a legend entry's mark: the verdict's column, scaled down.
func legendSwatch(v checker.Verdict) template.HTML {
	const w, h = 8, 14
	top := h - verdictHeight(v)*h
	return template.HTML(fmt.Sprintf(`<svg width="%d" height="%d" aria-hidden="true"><rect class="%s" x="0" y="%.1f" width="%d" height="%.1f" rx="1"/></svg>`,
		w, h, verdictClass(v), top, w, h-top))
}
