package main

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowScoring(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   float64
	}{
		{"SIMPLE", 14}, // Latest interval: 16 Mbps. Last two: 12 Mbps.
		{"EXPONENTIAL", (16 + (16*2.0/3 + 8*2.0/9)) / 2},
	} {
		s := scorer{cfg: config{AverageType: tc.method, MinTransferIntervals: 1, MaxTransferIntervals: 2}, history: []sample{{}}}
		s.update(sample{time.Second, 1000000})
		got, done := s.update(sample{2 * time.Second, 3000000})
		if done || math.Abs(got-tc.want) > 1e-8 {
			t.Errorf("%s: got %f, done %t; want %f", tc.method, got, done, tc.want)
		}
	}
}

func TestStableTransferStopsAfterMinimum(t *testing.T) {
	s := scorer{cfg: config{AverageType: "SIMPLE", MinTransferIntervals: 25, MaxTransferIntervals: 50, MaxTransferVariance: .015}, history: []sample{{}}}
	for i := 1; i <= 51; i++ {
		speed, done := s.update(sample{time.Duration(i) * 100 * time.Millisecond, int64(i) * 1000000})
		if math.Abs(speed-80) > 1e-8 || done != (i == 51) {
			t.Fatalf("sample %d: %f Mbps, done %v", i, speed, done)
		}
	}
}

func TestTransferRequest(t *testing.T) {
	payload := uploadBody(12345)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload":
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != "POST" || r.Header.Get("Content-Type") != "application/octet-stream" || r.URL.Query().Get("time") == "" || string(body) != string(payload) || r.ContentLength != int64(len(payload)) {
				t.Error("unexpected upload")
			}
		case "/download":
			w.Write(payload)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		path string
		body []byte
		ok   bool
	}{
		{"/download?size=12345", nil, true}, {"/upload?time=", payload, true}, {"/error", nil, false},
	} {
		var count atomic.Int64
		err := transferRequest(context.Background(), srv.Client(), srv.URL+tc.path, tc.body, &count, make([]byte, 512))
		if (err == nil) != tc.ok || tc.ok && count.Load() != int64(len(payload)) {
			t.Fatalf("%s: %v, %d bytes", tc.path, err, count.Load())
		}
	}
}
