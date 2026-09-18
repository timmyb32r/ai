package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"personal-feed/internal/feed"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "config.yaml", "YAML configuration")
	once := flag.Bool("once", false, "refresh once and exit")
	check := flag.Bool("check", false, "discover sources without writing database")
	only := flag.String("source", "", "only this source ID")
	sample := flag.Bool("sample", false, "with -check, extract one article per source")
	health := flag.Bool("healthcheck", false, "probe local HTTP server")
	flag.Parse()
	c, e := feed.LoadConfig(*path)
	if e != nil {
		return e
	}
	if *health {
		client := http.Client{Timeout: 3 * time.Second}
		_, port, _ := net.SplitHostPort(c.Listen)
		r, e := client.Get("http://127.0.0.1:" + port + "/healthz")
		if e != nil {
			return e
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("health: %s", r.Status)
		}
		return nil
	}
	if *only != "" {
		found := false
		for _, s := range c.Sources {
			if s.ID == *only {
				c.Sources = []feed.Source{s}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown source: %s", *only)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fetcher := feed.NewFetcher(c)
	if *check {
		failed := false
		for _, s := range c.Sources {
			a, w, err := fetcher.Discover(ctx, s)
			v := map[string]any{"id": s.ID, "count": len(a), "warning": w}
			if err != nil {
				v["error"] = err.Error()
				failed = true
			} else {
				v["first_url"] = a[0].URL
				v["first_title"] = a[0].Title
				if *sample {
					article, err := fetcher.Enrich(ctx, s, a[0])
					v["content_bytes"] = len(article.Content)
					v["published"] = article.Published
					v["title"] = article.Title
					if err != nil {
						v["article_warning"] = err.Error()
					}
				}
			}
			json.NewEncoder(os.Stdout).Encode(v)
		}
		if failed {
			return fmt.Errorf("some sources failed; see report")
		}
		return nil
	}
	store, e := feed.OpenStore(c.Database)
	if e != nil {
		return e
	}
	defer store.Close()
	svc := feed.Service{Config: c, Store: store, Fetcher: fetcher}
	if *once {
		return svc.Run(ctx, true)
	}
	server := &http.Server{Addr: c.Listen, Handler: svc.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { slog.Info("listening", "address", c.Listen); done <- server.ListenAndServe() }()
	workersDone := make(chan struct{})
	go func() { defer close(workersDone); svc.Run(ctx, false) }()
	select {
	case <-ctx.Done():
	case err := <-done:
		stop()
		if err != http.ErrServerClosed {
			<-workersDone
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e = server.Shutdown(shutdown)
	<-workersDone
	return e
}
