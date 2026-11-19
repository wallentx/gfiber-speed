package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const origin = "http://speed.googlefiber.net"

type region struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	URLs []string `json:"url"`
}

type config struct {
	Region                                                   string
	Regions                                                  []region
	LocationID, LocationName                                 string
	AverageType                                              string
	DownloadSize, UploadSize                                 int
	NumConcurrentDownloads, NumConcurrentUploads             int
	TransferPortStart, TransferPortEnd                       int
	MinTransferIntervals, MaxTransferIntervals, IntervalSize int
	InitialTransferVariance, MaxTransferVariance             float64
	PingRunTime, PingTimeout                                 int
}

func loadConfig(ctx context.Context, client *http.Client, base string) (config, error) {
	var last error
	for i := 0; i < 3 && ctx.Err() == nil; i++ {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var cfg config
		last = func() error {
			req, err := http.NewRequestWithContext(reqCtx, "GET", strings.TrimRight(base, "/")+"/config?progress=true", nil)
			if err != nil {
				return err
			}
			r, err := client.Do(req)
			if err != nil {
				return err
			}
			defer r.Body.Close()
			if r.StatusCode != 200 {
				return fmt.Errorf("HTTP %s", r.Status)
			}
			return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cfg)
		}()
		cancel()
		if last == nil {
			return cfg, nil
		}
	}
	if ctx.Err() != nil {
		return config{}, ctx.Err()
	}
	return config{}, fmt.Errorf("config from %s: %w", base, last)
}

type target struct {
	region
	base string
}

func targets(cfg config) ([]target, error) {
	var list []target
	for _, r := range cfg.Regions {
		if len(r.URLs) == 0 {
			return nil, fmt.Errorf("region %q has no server URL", r.ID)
		}
		for _, raw := range r.URLs {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "http" || !strings.HasSuffix(u.Hostname(), ".speed.googlefiber.net") ||
				u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("invalid server URL %q", raw)
			}
		}
		raw := r.URLs[rand.IntN(len(r.URLs))]
		list = append(list, target{r, strings.TrimRight(raw, "/")})
	}
	if len(list) == 0 && cfg.LocationID != "" {
		list = append(list, target{region{cfg.LocationID, cfg.LocationName, []string{origin}}, origin})
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("discovery returned no regions")
	}
	return list, nil
}

func (t target) match(s string) (target, bool) {
	for _, raw := range t.URLs {
		u, _ := url.Parse(raw)
		if strings.EqualFold(s, u.Hostname()) ||
			strings.EqualFold(s, strings.TrimSuffix(u.Hostname(), ".speed.googlefiber.net")) ||
			strings.EqualFold(strings.TrimRight(s, "/"), strings.TrimRight(raw, "/")) {
			t.base = strings.TrimRight(raw, "/")
			return t, true
		}
	}
	return t, strings.EqualFold(s, t.ID) || strings.EqualFold(s, t.Name)
}

func selectTarget(ctx context.Context, client *http.Client, cfg config, list []target, name string) (target, error) {
	if name != "" {
		for _, t := range list {
			if matched, ok := t.match(name); ok {
				return matched, nil
			}
		}
		return target{}, fmt.Errorf("unknown server %q (see --list-servers)", name)
	}
	if len(cfg.Regions) == 0 {
		return list[0], nil
	}
	probe := func(t target, first bool) time.Duration {
		return ping(ctx, client, t.base, 2*time.Second, time.Second, first, nil)
	}
	for _, t := range list {
		if t.ID == cfg.Region && probe(t, true) > 0 {
			return t, nil
		}
	}
	type reply struct {
		target
		rtt time.Duration
	}
	ch := make(chan reply, len(list))
	for _, t := range list {
		go func() { ch <- reply{t, probe(t, false)} }()
	}
	var best, local reply
	for range list {
		r := <-ch
		if r.rtt == 0 {
			continue
		}
		if best.rtt == 0 || r.rtt < best.rtt {
			best = r
		}
		if r.ID == cfg.Region {
			local = r
		}
	}
	if local.rtt > 0 && local.rtt < 100*time.Millisecond {
		return local.target, nil
	}
	if best.rtt == 0 {
		return target{}, fmt.Errorf("no advertised server answered a ping")
	}
	return best.target, nil
}

func (c config) validate() error {
	if c.TransferPortStart < 1 || c.TransferPortEnd < c.TransferPortStart || c.TransferPortEnd > 65535 ||
		c.NumConcurrentDownloads < 1 || c.NumConcurrentDownloads > 100 || c.NumConcurrentUploads < 1 || c.NumConcurrentUploads > 100 ||
		c.DownloadSize < 1 || c.DownloadSize > 128<<20 || c.UploadSize < 1 || c.UploadSize > 128<<20 ||
		c.IntervalSize < 1 || c.IntervalSize > 1000 || c.MinTransferIntervals < 1 || c.MaxTransferIntervals < c.MinTransferIntervals ||
		c.PingRunTime < 100 || c.PingRunTime > 60000 || c.PingTimeout < 1 || c.PingTimeout > 60000 ||
		(c.AverageType != "SIMPLE" && c.AverageType != "EXPONENTIAL") {
		return fmt.Errorf("regional config contains missing or unsupported test parameters")
	}
	return nil
}

func transferURL(base string, port int, path string) string {
	u, _ := url.Parse(base)
	u.Host = net.JoinHostPort(u.Hostname(), fmt.Sprint(port))
	return u.String() + path
}
