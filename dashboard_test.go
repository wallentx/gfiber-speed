package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDashboardFramesFit(t *testing.T) {
	d := dashboard{name: "Raleigh-Durham"}
	s := screen{active: latency, started: time.Unix(0, 0)}
	tr := &s.lines[latency]
	tr.add(100*time.Millisecond, 1.5)
	tr.add(200*time.Millisecond, 2.5)
	for _, cols := range []int{36, 44, 48, 80} {
		for _, rows := range []int{8, 11, 13, 16, 24} {
			lines := d.frame(s, s.started.Add(time.Second), cols, rows)
			f := strings.Join(lines, "\n")
			if strings.Contains(f, "\x1b[38;") || !strings.Contains(f, "2 replies") {
				t.Fatal("unexpected monochrome frame")
			}
			if rows >= 21 && !strings.Contains(f, "1.50 ms") {
				t.Fatal("missing minimum latency")
			}
			if len(lines) > rows {
				t.Fatalf("%d by %d has %d lines", cols, rows, len(lines))
			}
			for _, l := range lines {
				if utf8.RuneCountInString(l) >= cols {
					t.Errorf("%d by %d wraps %q", cols, rows, l)
				}
			}
		}
	}
}

func TestCompletedPlotsStayAboveResults(t *testing.T) {
	d := dashboard{name: "Austin"}
	start := time.Unix(0, 0)
	for i := range d.screen.lines {
		tr := &d.screen.lines[i]
		tr.add(time.Second, 10+float64(i))
		tr.add(2*time.Second, 5+float64(i))
	}
	d.screen.started = start
	d.screen.active = upload
	duringUpload := d.panels(d.screen, start.Add(time.Second), 76, 24, false)
	d.screen.active = latency
	duringPing := d.panels(d.screen, start.Add(2*time.Second), 76, 24, false)
	report := d.report(result{Download: 5, Upload: 6, Ping: 7}, 80, 24)
	for _, phase := range []struct {
		label string
		lines []string
	}{
		{"UPLOAD", duringUpload}, {"PING", duringPing},
	} {
		// Everything preceding the active phase is frozen, including its axes.
		end := 0
		for end < len(phase.lines) && !strings.Contains(phase.lines[end], phase.label) {
			end++
		}
		if end < 2 || end == len(phase.lines) {
			t.Fatalf("missing plot before %s", phase.label)
		}
		for i, line := range phase.lines[:end] {
			if duringPing[i] != line {
				t.Fatalf("%s: completed plot changed on row %d", phase.label, i)
			}
		}
		if !strings.HasPrefix(report, "  "+strings.Join(phase.lines[:end], "\n  ")+"\n") {
			t.Fatalf("%s: final plot differs from live plot", phase.label)
		}
	}
	if !strings.HasSuffix(report, "7.00 ms  \n") {
		t.Fatal("missing final minimum ping")
	}
}

func TestRightHandScore(t *testing.T) {
	d := dashboard{name: "Austin"}
	s := screen{active: latency, started: time.Now()}
	for i := range s.lines {
		tr := &s.lines[i]
		tr.add(time.Second, 1.25)
		tr.add(2*time.Second, 1842.63)
	}
	for _, tc := range []struct {
		width, rows, plotWidth, plotHeight int
		large                              bool
	}{
		{76, 24, 40, 4, true}, {32, 8, 14, 1, false},
	} {
		for _, complete := range []bool{false, true} {
			lines := d.panels(s, s.started, tc.width, tc.rows, complete)
			for p := download; p <= latency; p++ {
				start := 0
				for start < len(lines) && !strings.Contains(lines[start], strings.ToUpper(labels[p])) {
					start++
				}
				if start+tc.plotHeight >= len(lines) {
					t.Fatalf("missing %s graph", labels[p])
				}
				v := "1842.63"
				if p == latency && complete {
					v = "1.25"
				}
				span := p.timeRange(2*time.Second, p == latency && !complete)
				curve := graph(s.lines[p], span, tc.plotWidth, tc.plotHeight)
				want := []string{v + " " + units[p]}
				if tc.large {
					glyph := digits(v)
					glyph[2] += " " + units[p]
					want = glyph[:]
				}
				for i, text := range want {
					if got := lines[start+i+1]; got != curve[i]+"  "+text {
						t.Fatalf("%s row %d: got %q, want score %q beside plot", labels[p], i, got, text)
					}
				}
			}
		}
	}
}

func TestTimeRange(t *testing.T) {
	const sec = time.Second
	for _, tc := range []struct {
		phase         phaseID
		elapsed, live time.Duration
	}{
		{download, 0, 5 * sec}, {upload, 4 * sec, 5 * sec}, {latency, 0, 3 * sec},
		{latency, 3 * sec, 3 * sec}, {latency, 3*sec + 1, 4 * sec},
		{download, 5 * sec, 5 * sec}, {upload, 5*sec + 1, 6 * sec}, {download, 6 * sec, 6 * sec},
		{upload, 8600 * time.Millisecond, 9 * sec}, {download, 10 * sec, 10 * sec}, {download, 10*sec + 1, 11 * sec},
	} {
		if got := tc.phase.timeRange(tc.elapsed, true); got != tc.live {
			t.Errorf("%s at %v: live range %v, want %v", labels[tc.phase], tc.elapsed, got, tc.live)
		}
		if got := tc.phase.timeRange(tc.elapsed, false); got != tc.elapsed {
			t.Errorf("completed range %v, want %v", got, tc.elapsed)
		}
	}
}

func TestGraphReprojectsObservation(t *testing.T) {
	var tr trace
	tr.add(4*time.Second, 100)
	// A single observation lights exactly one top-row dot. The other
	// character cells remain empty except for the baseline tick at column 0.
	for _, tc := range []struct {
		width  int
		span   time.Duration
		column int
		dot    rune
	}{
		{3, 5 * time.Second, 2, 1}, {3, 6 * time.Second, 1, 8},
		{3, 4 * time.Second, 2, 8}, {5, 5 * time.Second, 3, 8}, {1, 4 * time.Second, 0, 8},
	} {
		row := []rune(graph(tr, tc.span, tc.width, 1)[0])
		if len(row) != tc.width {
			t.Fatalf("graph width changed to %d", len(row))
		}
		for col, cell := range row {
			want := rune(' ')
			if col%4 == 0 {
				want = '⢀'
			}
			if col == tc.column {
				want = 0x2800 + tc.dot
			}
			if cell != want {
				t.Errorf("width %d, span %s, column %d: got %q, want %q", tc.width, tc.span, col, cell, want)
			}
		}
	}
}

func TestGraphCollocatedExtrema(t *testing.T) {
	var tr trace
	tr.add(2*time.Second, 5)
	tr.add(2*time.Second+time.Millisecond, 10)
	tr.add(2*time.Second+2*time.Millisecond, 0)
	tr.add(2*time.Second+3*time.Millisecond, 5)
	row := []rune(graph(tr, 5*time.Second, 4, 1)[0])
	// All four observations land on dot column 3. The spike and trough
	// must light all four right-hand dots, even though the last value is 5.
	if row[1] != 0x2800+8+16+32+128 {
		t.Fatalf("lost extrema in %q", string(row))
	}
}

func TestPanelTimeLabels(t *testing.T) {
	s := screen{active: latency, started: time.Now()}
	s.lines[download].add(6600*time.Millisecond, 10)
	s.lines[upload].add(5300*time.Millisecond, 20)
	s.lines[latency].add(20*time.Millisecond, 1)
	d := dashboard{}
	check := func(now time.Time, complete bool, want string) {
		t.Helper()
		lines := d.panels(s, now, 76, 24, complete)
		var ends []string
		for _, line := range lines {
			if strings.HasPrefix(line, "0s ") {
				ends = append(ends, strings.Fields(line)[1])
			}
		}
		if got := strings.Join(ends, ","); got != want {
			t.Errorf("got right edges %s, want %s", got, want)
		}
	}
	check(s.started.Add(time.Second), false, "6.6s,5.3s,3.0s")
	check(s.started.Add(4*time.Second+1), false, "6.6s,5.3s,5.0s")
	check(time.Time{}, true, "6.6s,5.3s,20ms")
	// A tick may arrive before a newer observation in its screen snapshot.
	s.lines[latency].add(3*time.Second+1, 2)
	check(s.started.Add(3*time.Second), false, "6.6s,5.3s,4.0s")
	var empty trace
	if got := graph(empty, 0, 1, 1)[0]; got != "⢀" {
		t.Fatalf("empty graph contains samples: %q", got)
	}
	empty.add(0, 1)
	if got := graph(empty, 0, 1, 1)[0]; got != "⠁" {
		t.Fatalf("unexpected time-zero point %q", got)
	}
}

func TestRedrawChangedRows(t *testing.T) {
	prev := []string{"download", "upload", "ping"}
	if s := changedRows(prev, prev); s != "" {
		t.Fatalf("redundant redraw %q", s)
	}
	if s := changedRows(prev, []string{"download", "upload", "new"}); s != "\x1b[3;1Hnew\x1b[K" {
		t.Fatalf("updated frozen rows: %q", s)
	}
	if s := changedRows(nil, prev); !strings.Contains(s, "download") || !strings.Contains(s, "ping") {
		t.Fatalf("resize failed to repaint: %q", s)
	}
}
