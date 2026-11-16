// Package chart renders simple, dependency-free SVG charts for EXPERIMENTS.md.
package chart

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// Series is a named line or bar group.
type Series struct {
	Name string
	X    []float64
	Y    []float64
}

// Chart is a line or bar chart.
type Chart struct {
	Title, XLabel, YLabel string
	LogX, LogY            bool
	Series                []Series
	Bars                  bool     // bar chart over Categories (uses Series[i].Y per category)
	Categories            []string // bar categories
	W, H                  int
}

var palette = []string{"#2563eb", "#dc2626", "#16a34a", "#d97706", "#7c3aed", "#0891b2", "#db2777", "#4b5563"}

// Save writes the SVG to path.
func (c Chart) Save(path string) error { return os.WriteFile(path, []byte(c.SVG()), 0o644) }

// SVG renders the chart.
func (c Chart) SVG() string {
	if c.W == 0 {
		c.W = 720
	}
	if c.H == 0 {
		c.H = 400
	}
	const ml, mr, mt, mb = 70, 20, 40, 60
	pw, ph := float64(c.W-ml-mr), float64(c.H-mt-mb)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="sans-serif" font-size="12">`, c.W, c.H)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="white"/>`)
	fmt.Fprintf(&b, `<text x="%d" y="22" font-size="15" font-weight="bold">%s</text>`, ml, esc(c.Title))
	if c.Bars {
		c.bars(&b, ml, mt, pw, ph)
	} else {
		c.lines(&b, ml, mt, pw, ph)
	}
	fmt.Fprintf(&b, `<text x="%f" y="%d" text-anchor="middle">%s</text>`, float64(ml)+pw/2, c.H-12, esc(c.XLabel))
	fmt.Fprintf(&b, `<text transform="translate(16,%f) rotate(-90)" text-anchor="middle">%s</text>`, float64(mt)+ph/2, esc(c.YLabel))
	b.WriteString("</svg>\n")
	return b.String()
}

func (c Chart) tx(v float64) float64 {
	if c.LogX {
		return math.Log10(math.Max(v, 1e-12))
	}
	return v
}

func (c Chart) ty(v float64) float64 {
	if c.LogY {
		return math.Log10(math.Max(v, 1e-12))
	}
	return v
}

func (c Chart) lines(b *strings.Builder, ml, mt int, pw, ph float64) {
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, s := range c.Series {
		for i := range s.X {
			x, y := c.tx(s.X[i]), c.ty(s.Y[i])
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
	}
	if !c.LogY {
		minY = math.Min(0, minY)
	}
	if maxX == minX {
		maxX = minX + 1
	}
	if maxY == minY {
		maxY = minY + 1
	}
	maxY += (maxY - minY) * 0.05
	px := func(x float64) float64 { return float64(ml) + (c.tx(x)-minX)/(maxX-minX)*pw }
	py := func(y float64) float64 { return float64(mt) + ph - (c.ty(y)-minY)/(maxY-minY)*ph }
	axes(b, ml, mt, pw, ph)
	for i := 0; i <= 4; i++ {
		v := minY + (maxY-minY)*float64(i)/4
		y := float64(mt) + ph - float64(i)/4*ph
		label := v
		if c.LogY {
			label = math.Pow(10, v)
		}
		fmt.Fprintf(b, `<line x1="%d" x2="%f" y1="%f" y2="%f" stroke="#e5e7eb"/><text x="%d" y="%f" text-anchor="end">%s</text>`, ml, float64(ml)+pw, y, y, ml-6, y+4, num(label))
	}
	ticks := map[float64]bool{}
	for _, s := range c.Series {
		for _, x := range s.X {
			ticks[x] = true
		}
	}
	for x := range ticks {
		fmt.Fprintf(b, `<text x="%f" y="%f" text-anchor="middle">%s</text>`, px(x), float64(mt)+ph+16, num(x))
	}
	for si, s := range c.Series {
		col := palette[si%len(palette)]
		var pts []string
		for i := range s.X {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", px(s.X[i]), py(s.Y[i])))
		}
		fmt.Fprintf(b, `<polyline fill="none" stroke="%s" stroke-width="2" points="%s"/>`, col, strings.Join(pts, " "))
		for i := range s.X {
			fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="3.5" fill="%s"><title>%s: %s, %s</title></circle>`, px(s.X[i]), py(s.Y[i]), col, esc(s.Name), num(s.X[i]), num(s.Y[i]))
		}
		fmt.Fprintf(b, `<rect x="%f" y="%d" width="12" height="12" fill="%s"/><text x="%f" y="%d">%s</text>`, float64(ml)+pw-170, mt+4+si*18, col, float64(ml)+pw-152, mt+14+si*18, esc(s.Name))
	}
}

func (c Chart) bars(b *strings.Builder, ml, mt int, pw, ph float64) {
	maxY := 0.0
	for _, s := range c.Series {
		for _, y := range s.Y {
			maxY = math.Max(maxY, y)
		}
	}
	if maxY == 0 {
		maxY = 1
	}
	maxY *= 1.1
	axes(b, ml, mt, pw, ph)
	for i := 0; i <= 4; i++ {
		v := maxY * float64(i) / 4
		y := float64(mt) + ph - float64(i)/4*ph
		fmt.Fprintf(b, `<line x1="%d" x2="%f" y1="%f" y2="%f" stroke="#e5e7eb"/><text x="%d" y="%f" text-anchor="end">%s</text>`, ml, float64(ml)+pw, y, y, ml-6, y+4, num(v))
	}
	n := len(c.Categories)
	if n == 0 {
		return
	}
	group := pw / float64(n)
	bw := group * 0.8 / float64(len(c.Series))
	for ci, cat := range c.Categories {
		gx := float64(ml) + group*float64(ci) + group*0.1
		for si, s := range c.Series {
			if ci >= len(s.Y) {
				continue
			}
			h := s.Y[ci] / maxY * ph
			x := gx + bw*float64(si)
			fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"><title>%s %s: %s</title></rect>`, x, float64(mt)+ph-h, bw-1, h, palette[si%len(palette)], esc(s.Name), esc(cat), num(s.Y[ci]))
			fmt.Fprintf(b, `<text x="%.1f" y="%.1f" text-anchor="middle" font-size="10">%s</text>`, x+bw/2, float64(mt)+ph-h-3, num(s.Y[ci]))
		}
		fmt.Fprintf(b, `<text transform="translate(%.1f,%.1f) rotate(30)" font-size="11">%s</text>`, gx+group*0.3, float64(mt)+ph+14, esc(cat))
	}
	if len(c.Series) > 1 {
		for si, s := range c.Series {
			fmt.Fprintf(b, `<rect x="%f" y="%d" width="12" height="12" fill="%s"/><text x="%f" y="%d">%s</text>`, float64(ml)+pw-170, mt+4+si*18, palette[si%len(palette)], float64(ml)+pw-152, mt+14+si*18, esc(s.Name))
		}
	}
}

func axes(b *strings.Builder, ml, mt int, pw, ph float64) {
	fmt.Fprintf(b, `<line x1="%d" y1="%d" x2="%d" y2="%f" stroke="#111"/><line x1="%d" y1="%f" x2="%f" y2="%f" stroke="#111"/>`, ml, mt, ml, float64(mt)+ph, ml, float64(mt)+ph, float64(ml)+pw, float64(mt)+ph)
}

func num(v float64) string {
	switch {
	case v == math.Trunc(v) && math.Abs(v) < 1e7:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 100:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 1:
		return fmt.Sprintf("%.2g", v)
	default:
		return fmt.Sprintf("%.2g", v)
	}
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
