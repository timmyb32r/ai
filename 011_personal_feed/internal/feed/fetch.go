package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const maxBody = 16 << 20

type Fetcher struct {
	BrowserNoSandbox bool
	InitialItems     int
	Client           *http.Client
	Timeout          time.Duration
	BrowserPath      string
	browserSlots     chan struct{}
}

func NewFetcher(c Config) *Fetcher {
	return &Fetcher{BrowserNoSandbox: c.BrowserNoSandbox, InitialItems: c.InitialItems, Client: &http.Client{Timeout: c.Timeout}, Timeout: c.Timeout, BrowserPath: c.BrowserPath, browserSlots: make(chan struct{}, 1)}
}
func (f *Fetcher) Get(ctx context.Context, raw string, browser bool, wait string) (string, string, error) {
	if browser {
		return f.render(ctx, raw, wait, "", 0, 0)
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return "", raw, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PersonalFeed/1.0)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, text/html;q=0.9, */*;q=0.5")
	r, e := f.Client.Do(req)
	if e != nil {
		return "", raw, e
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return "", raw, fmt.Errorf("GET %s: HTTP %d", raw, r.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if e != nil {
		return "", raw, e
	}
	if len(b) > maxBody {
		return "", raw, fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	return string(b), r.Request.URL.String(), nil
}
func (f *Fetcher) render(ctx context.Context, raw, wait, loadMore string, clicks, scrollSteps int) (string, string, error) {
	select {
	case f.browserSlots <- struct{}{}:
		defer func() { <-f.browserSlots }()
	case <-ctx.Done():
		return "", raw, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if f.BrowserNoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	opts = append(opts, chromedp.Flag("disable-dev-shm-usage", true))
	if f.BrowserPath != "" {
		opts = append(opts, chromedp.ExecPath(f.BrowserPath))
	}
	alloc, stop := chromedp.NewExecAllocator(ctx, opts...)
	defer stop()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	var body, location string
	actions := []chromedp.Action{chromedp.Navigate(raw), chromedp.WaitReady("body", chromedp.ByQuery)}
	if wait != "" {
		actions = append(actions, chromedp.WaitReady(wait, chromedp.ByQuery))
	} else {
		actions = append(actions, chromedp.Sleep(2*time.Second))
	}
	if e := chromedp.Run(tab, actions...); e != nil {
		return "", raw, e
	}
	for i := 0; i < scrollSteps; i++ {
		if e := chromedp.Run(tab, chromedp.Evaluate(`window.scrollTo(0,document.body.scrollHeight)`, nil), chromedp.Sleep(2*time.Second)); e != nil {
			return "", raw, e
		}
	}
	var snapshots []string
	if loadMore != "" {
		if e := chromedp.Run(tab, chromedp.WaitReady(loadMore, chromedp.ByQuery), chromedp.Sleep(time.Second)); e != nil {
			return "", raw, e
		}
		selector, _ := json.Marshal(loadMore)
		for i := 0; i < clicks; i++ {
			var snapshot string
			if e := chromedp.Run(tab, chromedp.Evaluate(`(()=>{const d=document.body.cloneNode(true);d.querySelectorAll("script,style").forEach(e=>e.remove());return d.innerHTML})()`, &snapshot)); e != nil {
				return "", raw, e
			}
			snapshots = append(snapshots, snapshot)
			var clicked bool
			js := `(()=>{const e=document.querySelector(` + string(selector) + `);if(!e||e.disabled||e.getAttribute("aria-disabled")==="true")return false;e.click();return true})()`
			if e := chromedp.Run(tab, chromedp.Evaluate(js, &clicked)); e != nil {
				return "", raw, e
			}
			if !clicked {
				break
			}
			if e := chromedp.Run(tab, chromedp.Sleep(4*time.Second)); e != nil {
				return "", raw, e
			}
		}
	}
	actions = []chromedp.Action{chromedp.OuterHTML("html", &body, chromedp.ByQuery), chromedp.Location(&location)}
	if e := chromedp.Run(tab, actions...); e != nil {
		return "", raw, e
	}
	if len(snapshots) > 0 {
		body = "<html><body>" + strings.Join(snapshots, "\n") + body + "</body></html>"
	}
	if len(body) > maxBody {
		return "", raw, fmt.Errorf("rendered page too large")
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<title>just a moment") || strings.Contains(lower, "<title>access denied") || strings.Contains(lower, "cf-chl-widget") || strings.Contains(lower, "<title>attention required!") || strings.Contains(lower, "<title>один момент") {
		return "", raw, fmt.Errorf("browser received access challenge for %s", raw)
	}
	return body, location, nil
}

func (f *Fetcher) listing(ctx context.Context, raw string, s Source, force bool) (string, string, error) {
	if s.Browser || force {
		return f.render(ctx, raw, s.WaitSelector, s.LoadMoreSelector, s.LoadMoreClicks, s.ScrollSteps)
	}
	return f.Get(ctx, raw, false, "")
}
