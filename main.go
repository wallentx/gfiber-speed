package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

type result struct {
	Server   string  `json:"server"`
	Location string  `json:"location"`
	URL      string  `json:"url"`
	Download float64 `json:"download_mbps"`
	Upload   float64 `json:"upload_mbps"`
	Ping     float64 `json:"ping_ms"`
}

func run(ctx context.Context) error {
	server := flag.String("server", "", "select a listed region by ID, name, or hostname")
	listOnly := flag.Bool("list-servers", false, "print the live server directory and exit")
	asJSON := flag.Bool("json", false, "write JSON to stdout")
	plain := flag.Bool("plain", false, "use plain text without the live dashboard")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q (see --help)", flag.Arg(0))
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	tr.MaxIdleConnsPerHost = 20
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg, err := loadConfig(ctx, client, origin)
	if err != nil {
		return err
	}
	list, err := targets(cfg)
	if err != nil {
		return err
	}
	if *listOnly {
		if *asJSON {
			regions := make([]region, len(list))
			for i, t := range list {
				regions[i] = t.region
			}
			return json.NewEncoder(os.Stdout).Encode(regions)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tLOCATION\tSERVER")
		for _, t := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\n", t.ID, t.Name, strings.Join(t.URLs, ", "))
		}
		return w.Flush()
	}
	t, err := selectTarget(ctx, client, cfg, list, *server)
	if err != nil {
		return err
	}
	if len(cfg.Regions) > 0 {
		cfg, err = loadConfig(ctx, client, t.base)
		if err != nil {
			return err
		}
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	payload := uploadBody(cfg.UploadSize)
	view := newDashboard(t.Name, *plain || *asJSON)
	if view != nil {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(ctx, dashboardInterrupts...)
		defer cancel()
		view.start()
		defer view.stop()
	} else {
		fmt.Fprintf(os.Stderr, "Testing %s (%s)\n", t.Name, t.base)
	}
	d, err := transfer(ctx, client, t.base, cfg, nil, view.phase(download, cfg.NumConcurrentDownloads))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	u, err := transfer(ctx, client, t.base, cfg, payload, view.phase(upload, cfg.NumConcurrentUploads))
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	pingTime := time.Duration(cfg.PingRunTime) * time.Millisecond
	p := ping(ctx, client, t.base, pingTime, time.Duration(cfg.PingTimeout)*time.Millisecond, false, view.phase(latency, 0))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p == 0 {
		return fmt.Errorf("no successful ping from %s", t.base)
	}
	r := result{t.ID, t.Name, t.base, d, u, float64(p) / float64(time.Millisecond)}
	if view != nil {
		view.stop()
		return view.summary(r)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	_, err = fmt.Fprintf(os.Stdout, "%s (%s)\nDownload  %.2f Mbps\nUpload    %.2f Mbps\nPing      %.2f ms\n", t.Name, t.ID, d, u, r.Ping)
	return err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
