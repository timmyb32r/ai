package feed

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen           string        `yaml:"listen"`
	Database         string        `yaml:"database"`
	PublicBaseURL    string        `yaml:"public_base_url"`
	Interval         time.Duration `yaml:"interval"`
	Timeout          time.Duration `yaml:"timeout"`
	InitialItems     int           `yaml:"initial_items"`
	MaxItems         int           `yaml:"max_items"`
	Workers          int           `yaml:"workers"`
	BrowserPath      string        `yaml:"browser_path"`
	BrowserNoSandbox bool          `yaml:"browser_no_sandbox"`
	Sources          []Source      `yaml:"sources"`
}
type Source struct {
	ScrollSteps      int      `yaml:"scroll_steps"`
	Adapter          string   `yaml:"adapter"`
	CardSelector     string   `yaml:"card_selector"`
	LoadMoreSelector string   `yaml:"load_more_selector"`
	LoadMoreClicks   int      `yaml:"load_more_clicks"`
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name"`
	URL              string   `yaml:"url"`
	FeedURL          string   `yaml:"feed_url"`
	ListingURL       string   `yaml:"listing_url"`
	LinkSelector     string   `yaml:"link_selector"`
	URLPattern       string   `yaml:"url_pattern"`
	ContentSelector  string   `yaml:"content_selector"`
	NextSelector     string   `yaml:"next_selector"`
	ExtraListingURLs []string `yaml:"extra_listing_urls"`
	MaxPages         int      `yaml:"max_pages"`
	Browser          bool     `yaml:"browser"`
	BrowserFallback  bool     `yaml:"browser_fallback"`
	WaitSelector     string   `yaml:"wait_selector"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{Listen: ":8080", Database: "data/feed.db", Interval: 30 * time.Minute, Timeout: 45 * time.Second, InitialItems: 20, MaxItems: 100, Workers: 3}
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if c.Interval <= 0 || c.Timeout <= 0 || c.InitialItems < 1 || c.MaxItems < c.InitialItems || c.Workers < 1 || c.Workers > 16 {
		return c, fmt.Errorf("invalid interval, timeout, item limits or workers")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("listen: %w", err)
	}
	if c.Listen == "" || c.Database == "" || len(c.Sources) == 0 {
		return c, fmt.Errorf("listen, database and sources required")
	}
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	if c.PublicBaseURL != "" {
		if e = validURL(c.PublicBaseURL); e != nil {
			return c, e
		}
	}
	ids := map[string]bool{}
	idPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	for i := range c.Sources {
		s := &c.Sources[i]
		if !idPattern.MatchString(s.ID) || ids[s.ID] {
			return c, fmt.Errorf("invalid or duplicate id: %s", s.ID)
		}
		ids[s.ID] = true
		if s.Adapter != "" && s.Adapter != "cloudera" {
			return c, fmt.Errorf("%s: unknown adapter %s", s.ID, s.Adapter)
		}
		if s.Name == "" {
			s.Name = s.ID
		}
		if e = validURL(s.URL); e != nil {
			return c, fmt.Errorf("%s: %w", s.ID, e)
		}
		for _, u := range append([]string{s.FeedURL, s.ListingURL}, s.ExtraListingURLs...) {
			if u != "" {
				if e = validURL(u); e != nil {
					return c, e
				}
			}
		}
		for _, sel := range []string{s.LinkSelector, s.ContentSelector, s.NextSelector, s.WaitSelector, s.LoadMoreSelector, s.CardSelector} {
			if sel != "" {
				if _, e = cascadia.Compile(sel); e != nil {
					return c, fmt.Errorf("%s selector: %w", s.ID, e)
				}
			}
		}
		if s.URLPattern != "" {
			if _, e = regexp.Compile(s.URLPattern); e != nil {
				return c, e
			}
		}
		if s.ScrollSteps < 0 || s.ScrollSteps > 10 {
			return c, fmt.Errorf("%s: scroll_steps must be 0..10", s.ID)
		}
		if s.LoadMoreClicks < 0 || s.LoadMoreClicks > 10 || len(s.ExtraListingURLs) > 20 {
			return c, fmt.Errorf("%s: excessive pagination", s.ID)
		}
		if s.MaxPages == 0 {
			s.MaxPages = 3
		}
		if s.MaxPages < 1 || s.MaxPages > 20 {
			return c, fmt.Errorf("%s: max_pages must be 1..20", s.ID)
		}
	}
	return c, nil
}
func validURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid HTTP URL: %s", raw)
	}
	return nil
}
