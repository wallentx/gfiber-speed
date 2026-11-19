package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type phaseID int

const (
	download phaseID = iota
	upload
	latency
)

var dashboardInterrupts = []os.Signal{os.Interrupt}

var labels = [...]string{"Download", "Upload", "Ping"}
var units = [...]string{"Mbps", "Mbps", "ms"}
var accents = [...]int{51, 213, 222}
var gradients = [...][5]int{{51, 45, 39, 33, 27}, {219, 213, 177, 135, 99}, {229, 222, 215, 208, 166}}

type observation struct {
	elapsed time.Duration
	value   float64
}

type trace struct {
	// Samples are append-only. A screen snapshot can read its slice while
	// the measurement callback appends beyond the snapshot's length.
	samples    []observation
	streams    int
	best, peak float64
}

func (t *trace) add(elapsed time.Duration, v float64) {
	if len(t.samples) == 0 || v < t.best {
		t.best = v
	}
	t.peak = max(t.peak, v)
	t.samples = append(t.samples, observation{elapsed, v})
}

func (t trace) latest() observation {
	if len(t.samples) == 0 {
		return observation{}
	}
	return t.samples[len(t.samples)-1]
}

func (p phaseID) timeRange(elapsed time.Duration, live bool) time.Duration {
	if !live {
		return elapsed
	}
	initial := 5 * time.Second
	if p == latency {
		initial = 3 * time.Second
	}
	if elapsed <= initial {
		return initial
	}
	// At an exact second the last sample still fits on the current axis.
	return ((elapsed-1)/time.Second + 1) * time.Second
}

type screen struct {
	active  phaseID
	started time.Time
	lines   [3]trace
}
type dashboard struct {
	name       string
	color      bool
	mu         sync.Mutex
	screen     screen
	quit, done chan struct{}
}

func newDashboard(name string, disabled bool) *dashboard {
	tty := int(os.Stdout.Fd())
	if disabled || !term.IsTerminal(tty) || !term.IsTerminal(int(os.Stderr.Fd())) || os.Getenv("TERM") == "dumb" {
		return nil
	}
	width, height, err := term.GetSize(tty)
	if err != nil || width < 36 || height < 8 {
		return nil
	}
	return &dashboard{name: name, color: os.Getenv("NO_COLOR") == "", quit: make(chan struct{}), done: make(chan struct{})}
}

func (d *dashboard) phase(p phaseID, streams int) func(time.Duration, float64) {
	if d == nil {
		if streams > 0 {
			fmt.Fprintf(os.Stderr, "%s (%d streams)...\n", labels[p], streams)
		} else {
			fmt.Fprintln(os.Stderr, "Ping...")
		}
		return nil
	}
	d.mu.Lock()
	d.screen.active = p
	d.screen.lines[p].streams = streams
	d.screen.started = time.Now()
	d.mu.Unlock()
	return func(elapsed time.Duration, v float64) {
		d.mu.Lock()
		d.screen.lines[p].add(elapsed, v)
		d.mu.Unlock()
	}
}

func (d *dashboard) start() {
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(125 * time.Millisecond)
		defer ticker.Stop()
		var previous []string
		var cols, rows int
		for {
			select {
			case <-d.quit:
				return
			case now := <-ticker.C:
				d.mu.Lock()
				s := d.screen
				d.mu.Unlock()
				width, height, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil && !s.started.IsZero() {
					if cols != width || rows != height {
						previous = nil
					}
					frame := d.frame(s, now, width, height)
					if output := changedRows(previous, frame); output != "" {
						fmt.Fprint(os.Stdout, output)
					}
					previous, cols, rows = frame, width, height
				}
			}
		}
	}()
}

func (d *dashboard) stop() {
	if d.quit == nil {
		return
	}
	close(d.quit)
	<-d.done
	d.quit = nil
	fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
}

func (d *dashboard) ink(color int, s string) string {
	if !d.color {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", color, s)
}

func axisLabel(span time.Duration) string {
	if span > 0 && span < 100*time.Millisecond {
		switch {
		case span >= time.Millisecond:
			return span.Round(time.Millisecond).String()
		case span >= time.Microsecond:
			return span.Round(time.Microsecond).String()
		default:
			return span.String()
		}
	}
	return fmt.Sprintf("%.1fs", span.Seconds())
}

// graph uses a 2-by-4 dot grid per character. The plot spans the phase's
// displayed time range horizontally and zero to its largest observed sample vertically.
func graph(t trace, span time.Duration, width, height int) []string {
	pixels := make([]byte, width*height)
	dots := [2][4]byte{{1, 2, 4, 64}, {8, 16, 32, 128}}
	plot := func(x, y int) { pixels[(y/4)*width+x/2] |= dots[x%2][y%4] }
	prevX, prevY := -1, 0
	for _, sample := range t.samples {
		x := 0
		if span > 0 {
			x = int(math.Round(float64(sample.elapsed) / float64(span) * float64(2*width-1)))
		}
		y := height*4 - 1
		if t.peak > 0 {
			y -= int(math.Round(sample.value / t.peak * float64(y)))
		}
		if prevX < 0 {
			plot(x, y)
		} else {
			steps := max(1, x-prevX, int(math.Abs(float64(y-prevY))))
			for j := 1; j <= steps; j++ {
				plot(prevX+(x-prevX)*j/steps, prevY+(y-prevY)*j/steps)
			}
		}
		prevX, prevY = x, y
	}
	rows := make([]string, height)
	for r := range rows {
		var row strings.Builder
		for c := 0; c < width; c++ {
			v := pixels[r*width+c]
			switch {
			case v != 0:
				row.WriteRune(0x2800 + rune(v))
			case r == height-1 && c%4 == 0:
				row.WriteRune('⢀')
			default:
				row.WriteByte(' ')
			}
		}
		rows[r] = row.String()
	}
	return rows
}

// Three columns of five bits define each numeral. Half-blocks pack two
// vertical pixels into one cell so a readable display uses only three rows.
var numerals = [...][3]byte{
	{31, 17, 31}, {0, 31, 0}, {29, 21, 23}, {17, 21, 31}, {7, 4, 31},
	{23, 21, 29}, {31, 21, 29}, {1, 1, 31}, {31, 21, 31}, {23, 21, 31},
}

func digits(value string) [3]string {
	var out [3]string
	for row := range out {
		var b strings.Builder
		for _, r := range value {
			if r == '.' {
				if row == 2 {
					b.WriteRune('▀')
				} else {
					b.WriteByte(' ')
				}
				b.WriteByte(' ')
				continue
			}
			for _, col := range numerals[r-'0'] {
				b.WriteRune([4]rune{' ', '▀', '▄', '█'}[(col>>(row*2))&3])
			}
			b.WriteByte(' ')
		}
		out[row] = b.String()
	}
	return out
}

// scoreRows uses a fixed column so digit changes never resize the time axis.
func scoreRows(value, unit string, width, height int) []string {
	rows := make([]string, height)
	if height >= 3 && len(value)*4-strings.Count(value, ".")*2+1+len(unit) <= width {
		glyphs := digits(value)
		glyphs[2] += " " + unit
		copy(rows[(height-3)/2:], glyphs[:])
	} else {
		rows[(height-1)/2] = value + " " + unit
	}
	return rows
}

// Use the same panel height throughout a run. Each completed curve keeps its
// rows when the next phase opens and when the report is left in scrollback.
func (d *dashboard) panels(s screen, now time.Time, width, rows int, complete bool) []string {
	axis := rows >= 21
	height := (rows-2)/3 - 1
	if rows >= 18 {
		height = (rows-6)/3 - 1
	}
	if axis {
		height--
	}
	height = min(5, max(1, height))
	scoreWidth := min(34, width/2)
	if height < 3 {
		scoreWidth = min(16, scoreWidth)
	}
	plotWidth := max(1, width-scoreWidth-2)
	lines := []string{d.ink(250, "GFIBER") + "  /  " + d.ink(252, d.name)}
	for p := download; p <= s.active; p++ {
		t := s.lines[p]
		icon := "✓"
		latest := t.latest()
		elapsed := latest.elapsed
		live := p == s.active && !complete
		if live {
			elapsed = max(elapsed, now.Sub(s.started))
			icon = string([]rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")[(elapsed/(125*time.Millisecond))%10])
		}
		span := p.timeRange(elapsed, live)
		meta := fmt.Sprintf("%d streams", t.streams)
		if p == latency {
			meta = fmt.Sprintf("%d replies", len(t.samples))
		}
		timing := fmt.Sprintf("%.1fs", elapsed.Seconds())
		if !axis {
			timing = "0–" + axisLabel(span)
		}
		lines = append(lines, d.ink(accents[p], icon+" "+strings.ToUpper(labels[p]))+d.ink(247, fmt.Sprintf("   %s   %s", meta, timing)))
		value := "..."
		if len(t.samples) > 0 {
			score := latest.value
			if p == latency && !live {
				score = t.best
			}
			value = fmt.Sprintf("%.2f", score)
		}
		readout := scoreRows(value, units[p], scoreWidth, height)
		for i, row := range graph(t, span, plotWidth, height) {
			line := d.ink(gradients[p][i], row)
			if readout[i] != "" {
				line += "  " + d.ink(accents[p], readout[i])
			}
			lines = append(lines, line)
		}
		if axis {
			left := "0s"
			right := axisLabel(span)
			scale := left + strings.Repeat(" ", max(1, plotWidth-len(left)-len(right))) + right
			annotation := fmt.Sprintf("peak %.2f %s", t.peak, units[p])
			if p == latency && len(t.samples) > 0 {
				annotation = fmt.Sprintf("min %.2f ms", t.best)
			}
			lines = append(lines, d.ink(246, scale+"  "+annotation))
		}
	}
	return lines
}

func (d *dashboard) frame(s screen, now time.Time, cols, rows int) []string {
	width := min(104, cols-4)
	if width < 32 || rows < 8 {
		return []string{labels[s.active]}
	}
	lines := d.panels(s, now, width, rows, false)
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return lines
}

// Update changed rows in a single terminal write. A resize invalidates the
// previous coordinates, so the caller passes nil to repaint the whole view.
func changedRows(previous, frame []string) string {
	var b strings.Builder
	if previous == nil {
		b.WriteString("\x1b[H\x1b[J")
	}
	for i, line := range frame {
		if i < len(previous) && previous[i] == line {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", i+1, line)
	}
	if len(frame) < len(previous) {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[J", len(frame)+1)
	}
	return b.String()
}

func (d *dashboard) report(r result, cols, rows int) string {
	width := min(104, max(1, cols-4))
	lines := d.panels(d.screen, time.Time{}, width, rows, true)
	lines = append(lines, d.ink(240, strings.Repeat("─", width)))
	for i, v := range []float64{r.Download, r.Upload, r.Ping} {
		name := labels[i]
		if phaseID(i) == latency {
			name = "Min ping"
		}
		lines = append(lines, d.ink(accents[i], fmt.Sprintf("%-9s", name))+fmt.Sprintf(" %9.2f %-4s", v, units[i]))
	}
	return "  " + strings.Join(lines, "\n  ") + "\n"
}

func (d *dashboard) summary(r result) error {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		cols, rows = 80, 24
	}
	_, err = fmt.Fprint(os.Stdout, d.report(r, cols, rows))
	return err
}
