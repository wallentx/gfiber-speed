package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var epoch = time.Now()

func stamp() string {
	return strconv.FormatFloat(float64(time.Since(epoch))/float64(time.Millisecond), 'f', 3, 64)
}

func ping(ctx context.Context, client *http.Client, base string, runtime, timeout time.Duration, first bool, observe func(time.Duration, float64)) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, runtime)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	var best time.Duration
	var finished time.Time
	for {
		select {
		case <-ctx.Done():
			return best
		case tick := <-ticker.C:
			if tick.Before(finished) {
				continue
			}
			reqCtx, end := context.WithTimeout(ctx, timeout)
			req, err := http.NewRequestWithContext(reqCtx, "GET", base+"/ping?time="+stamp(), nil)
			if err != nil {
				end()
				return best
			}
			start := time.Now()
			resp, err := client.Do(req)
			if err == nil {
				_, err = io.Copy(io.Discard, resp.Body)
				finished = time.Now()
				resp.Body.Close()
				d := finished.Sub(start)
				if resp.StatusCode == 200 && err == nil {
					if best == 0 || d < best {
						best = d
					}
					if observe != nil {
						observe(finished.Sub(started), float64(d)/float64(time.Millisecond))
					}
				}
			} else {
				finished = time.Now()
			}
			end()
			if first && best > 0 {
				return best
			}
		}
	}
}

type sample struct {
	elapsed time.Duration
	bytes   int64
}
type scorer struct {
	cfg         config
	history     []sample
	short, long float64
	rampEnd     time.Duration
}

// The published runner reads *Millis properties. The caller supplies differently
// named options for these three timings, so the runner uses its defaults.
const (
	minRun   = 5 * time.Second
	maxRun   = 10 * time.Second
	postRamp = 2 * time.Second
)

func (s *scorer) update(now sample) (float64, bool) {
	prevShort := s.short
	s.history = append(s.history, now)
	last := len(s.history) - 1
	rate := func(window int) float64 {
		prev := s.history[max(0, last-window)]
		return float64(now.bytes-prev.bytes) * 8 / (now.elapsed - prev.elapsed).Seconds() / 1e6
	}
	if s.cfg.AverageType == "EXPONENTIAL" {
		speed := rate(1)
		s.short += (speed - s.short) * 2 / (1 + float64(s.cfg.MinTransferIntervals))
		s.long += (speed - s.long) * 2 / (1 + float64(s.cfg.MaxTransferIntervals))
	} else {
		s.short = rate(s.cfg.MinTransferIntervals)
		s.long = rate(s.cfg.MaxTransferIntervals)
	}
	score := (s.short + s.long) / 2
	if now.elapsed >= maxRun {
		return score, true
	}
	if s.short == 0 || s.long == 0 {
		return score, false
	}
	variance := s.cfg.MaxTransferVariance
	initial := s.cfg.InitialTransferVariance
	if initial > 0 && initial < variance && now.elapsed > minRun {
		variance = initial + (variance-initial)*float64(now.elapsed-minRun)/float64(maxRun-minRun)
	}
	// At the first sample the browser's previous short speed is undefined.
	if s.rampEnd == 0 && (last == 1 || s.short <= prevShort*(1+variance)) {
		s.rampEnd = now.elapsed
	}
	return score, s.rampEnd > 0 && now.elapsed > minRun && now.elapsed-s.rampEnd >= postRamp &&
		1-math.Min(s.short, s.long)/math.Max(s.short, s.long) <= variance
}

type meteredReader struct {
	r     io.Reader
	count *atomic.Int64
}

func (r meteredReader) Read(buf []byte) (int, error) {
	n, err := r.r.Read(buf)
	r.count.Add(int64(n))
	return n, err
}

// Chrome reuses a Blob made of random numbers rendered as decimal strings.
func uploadBody(size int) []byte {
	buf := make([]byte, 0, size+32)
	for len(buf) < size {
		buf = strconv.AppendFloat(buf, rand.Float64(), 'f', -1, 64)
	}
	return buf[:size]
}

func transfer(ctx context.Context, client *http.Client, base string, c config, upload []byte, observe func(time.Duration, float64)) (float64, error) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	streams := c.NumConcurrentDownloads
	if upload != nil {
		streams = c.NumConcurrentUploads
	}
	var total atomic.Int64
	fail := make(chan error, streams)
	start := time.Now()
	for i := 0; i < streams; i++ {
		port := c.TransferPortStart + i%(c.TransferPortEnd-c.TransferPortStart+1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			endpoint := transferURL(base, port, "/download?size="+strconv.Itoa(c.DownloadSize))
			if upload != nil {
				endpoint = transferURL(base, port, "/upload?time=")
			}
			buf := make([]byte, 32<<10)
			for ctx.Err() == nil {
				err := transferRequest(ctx, client, endpoint, upload, &total, buf)
				if err != nil {
					fail <- fmt.Errorf("port %d: %w", port, err)
					return
				}
			}
		}()
	}
	s := scorer{cfg: c, history: []sample{{}}}
	ticker := time.NewTicker(time.Duration(c.IntervalSize) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case err := <-fail:
			return 0, err
		case <-ticker.C:
			elapsed := time.Since(start)
			speed, done := s.update(sample{elapsed, total.Load()})
			if observe != nil {
				observe(elapsed, speed)
			}
			if done {
				if speed <= 0 {
					return 0, fmt.Errorf("no sustained data transfer")
				}
				return speed, nil
			}
		}
	}
}

func transferRequest(ctx context.Context, client *http.Client, endpoint string, upload []byte, count *atomic.Int64, buf []byte) error {
	method := "GET"
	var body io.Reader
	if upload != nil {
		method = "POST"
		endpoint += stamp()
		body = meteredReader{bytes.NewReader(upload), count}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if upload != nil {
		req.ContentLength = int64(len(upload))
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if upload != nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	for {
		n, err := resp.Body.Read(buf)
		count.Add(int64(n))
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
