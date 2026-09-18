package feed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Service struct {
	Config  Config
	Store   *Store
	Fetcher *Fetcher
}

func (s *Service) Refresh(ctx context.Context, src Source) error {
	candidates, warning, e := s.Fetcher.Discover(ctx, src)
	if e != nil {
		return e
	}
	initialized, e := s.Store.Initialized(ctx, src.ID)
	if e != nil {
		return e
	}
	if !initialized && len(candidates) < s.Config.InitialItems {
		warning = strings.Trim(warning+fmt.Sprintf("; only %d discoverable articles (requested %d)", len(candidates), s.Config.InitialItems), "; ")
	}
	limit := s.Config.MaxItems
	if !initialized {
		limit = s.Config.InitialItems
	}
	articles := []Article{}
	failures := 0
	var example string
	for _, a := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seen, err := s.Store.Seen(ctx, src.ID, a.URL)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		a, err = s.Fetcher.Enrich(ctx, src, a)
		if err != nil {
			failures++
			example = err.Error()
			slog.Warn("article fallback", "source", src.ID, "url", a.URL, "error", err)
		}
		articles = append(articles, a)
		if len(articles) >= limit {
			break
		}
	}
	if failures > 0 {
		warning = strings.Trim(warning+fmt.Sprintf("; %d article(s) used fallback; example: %s", failures, example), "; ")
	}
	var baseline []string
	if !initialized {
		for _, a := range candidates {
			baseline = append(baseline, a.URL)
		}
	}
	if e = s.Store.Save(ctx, src.ID, articles, s.Config.MaxItems, warning, baseline); e != nil {
		return e
	}
	slog.Info("source refreshed", "source", src.ID, "discovered", len(candidates), "added", len(articles), "fallbacks", failures)
	return nil
}
func (s *Service) refreshRecorded(ctx context.Context, src Source) error {
	e := s.Refresh(ctx, src)
	if e != nil && !errors.Is(e, context.Canceled) {
		slog.Error("source refresh failed", "source", src.ID, "error", e)
		if err := s.Store.Failure(ctx, src.ID, e); err != nil {
			slog.Error("record source failure", "error", err)
		}
	}
	return e
}
func (s *Service) Run(ctx context.Context, once bool) error {
	slots := make(chan struct{}, s.Config.Workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for _, src := range s.Config.Sources {
		wg.Add(1)
		go func(src Source) {
			defer wg.Done()
			for {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				started := time.Now()
				e := s.refreshRecorded(ctx, src)
				<-slots
				if once {
					if e != nil {
						mu.Lock()
						failures = append(failures, fmt.Errorf("%s: %w", src.ID, e))
						mu.Unlock()
					}
					return
				}
				timer := time.NewTimer(max(time.Second, time.Until(started.Add(s.Config.Interval))))
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(src)
	}
	wg.Wait()
	return errors.Join(failures...)
}
