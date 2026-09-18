package feed

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "feed.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestRetentionRestartAndSeen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "feed.db")
	s, e := OpenStore(p)
	if e != nil {
		t.Fatal(e)
	}
	a := []Article{}
	for i := 0; i < 105; i++ {
		a = append(a, Article{URL: fmt.Sprintf("https://example.com/%d", i), Title: fmt.Sprint(i), Published: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)})
	}
	if e = s.Save(ctx, "test", a, 100, "", []string{"https://example.com/old"}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenStore(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Save(ctx, "test", a, 100, "", nil); e != nil {
		t.Fatal(e)
	}
	got, e := s.Articles(ctx, "test")
	if e != nil || len(got) != 100 {
		t.Fatalf("count %d: %v", len(got), e)
	}
	if got[0].Title != "104" {
		t.Fatal(got[0])
	}
	for _, u := range []string{a[0].URL, "https://example.com/old"} {
		seen, e := s.Seen(ctx, "test", u)
		if e != nil || !seen {
			t.Fatalf("forgot %s: %v", u, e)
		}
	}
	if e = s.Failure(ctx, "test", fmt.Errorf("upstream down")); e != nil {
		t.Fatal(e)
	}
	got, _ = s.Articles(ctx, "test")
	if len(got) != 100 {
		t.Fatal("failure erased feed")
	}
}
func TestRSSAndHTTP(t *testing.T) {
	s := testStore(t)
	src := Source{ID: "test", Name: "A & B", URL: "https://example.com"}
	svc := Service{Config: Config{Sources: []Source{src}, Interval: 30 * time.Minute, PublicBaseURL: "http://example.net:8080"}, Store: s}
	h := svc.Handler()
	req := httptest.NewRequest("GET", "/test.xml", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	a := Article{URL: "https://example.com/a?x=1&b=2", Title: "A < B", Content: "<p>Body &amp; image</p>", Summary: "Summary", Author: "Test Author", Published: time.Now()}
	if e := s.Save(context.Background(), src.ID, []Article{a}, 100, "", nil); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f, e := gofeed.NewParser().ParseString(w.Body.String())
	if e != nil {
		t.Fatal(e)
	}
	if len(f.Items) != 1 || f.Items[0].Title != a.Title || !strings.Contains(f.Items[0].Content, "Original article") || f.Items[0].Author.Name != a.Author {
		t.Fatalf("bad RSS: %+v", f)
	}
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/opml.xml", nil))
	var op struct {
		URL string `xml:"body>outline>unused"`
	}
	if e := xml.Unmarshal(w.Body.Bytes(), &op); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(w.Body.String(), "http://example.net:8080/test.xml") {
		t.Fatal(w.Body.String())
	}
}
func TestRefreshBaselineAndFailureIsolation(t *testing.T) {
	ctx := context.Background()
	var base string
	broken := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			http.Error(w, "down", 503)
			return
		}
		if r.URL.Path == "/feed" {
			fmt.Fprintf(w, `<rss version="2.0"><channel><title>x</title><link>%s</link><description>x</description>`, base)
			for i := 5; i >= 1; i-- {
				fmt.Fprintf(w, `<item><title>Article %d</title><link>%s/post/%d</link><description>Useful summary</description></item>`, i, base, i)
			}
			fmt.Fprint(w, "</channel></rss>")
			return
		}
		http.Error(w, "article unavailable", 503)
	}))
	defer upstream.Close()
	base = upstream.URL
	src := Source{ID: "test", Name: "Test", URL: base, FeedURL: base + "/feed"}
	c := Config{Sources: []Source{src}, InitialItems: 2, MaxItems: 100, Timeout: time.Second, Workers: 2}
	s := testStore(t)
	svc := Service{Config: c, Store: s, Fetcher: NewFetcher(c)}
	if e := svc.Refresh(ctx, src); e != nil {
		t.Fatal(e)
	}
	a, _ := s.Articles(ctx, src.ID)
	if len(a) != 2 || a[0].Summary != "Useful summary" {
		t.Fatal(a)
	}
	if e := svc.Refresh(ctx, src); e != nil {
		t.Fatal(e)
	}
	a, _ = s.Articles(ctx, src.ID)
	if len(a) != 2 {
		t.Fatal("second poll backfilled old articles", len(a))
	}
	broken = true
	if e := svc.Run(ctx, true); e == nil {
		t.Fatal("expected failure")
	}
	a, _ = s.Articles(ctx, src.ID)
	if len(a) != 2 {
		t.Fatal("lost articles")
	}
}
func TestExtractionAndSanitizing(t *testing.T) {
	body := `<html><head><meta property="og:title" content="Real title"><meta property="article:published_time" content="2026-09-18T10:00:00Z"><meta name="author" content="Alice"></head><body><article><p>` + strings.Repeat("Useful article text. ", 20) + `</p><img src="/picture.png"><script>alert(1)</script><a href="javascript:alert(1)">bad</a><a href="/related">related</a></article></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	f := NewFetcher(Config{Timeout: time.Second})
	a, e := f.Enrich(context.Background(), Source{ContentSelector: "article"}, Article{URL: server.URL + "/post", Title: "old"})
	if e != nil {
		t.Fatal(e)
	}
	if a.Title != "Real title" || a.Author != "Alice" || a.Published.IsZero() {
		t.Fatal(a)
	}
	if strings.Contains(a.Content, "<script") || strings.Contains(a.Content, "javascript:") || !strings.Contains(a.Content, server.URL+"/picture.png") {
		t.Fatal(a.Content)
	}
	listing := `<a href="/blog/a?utm_source=foo">First</a><a href="/blog/a">Duplicate</a><a href="/blog/category/c">Category</a><a class="next" href="?page=2">Next</a>`
	items, next, e := parseListing(listing, server.URL+"/blog", Source{LinkSelector: "a", URLPattern: `/blog/[^/?]+$`, NextSelector: "a.next"})
	if e != nil || len(items) != 1 || next != server.URL+"/blog?page=2" {
		t.Fatalf("%+v %s %v", items, next, e)
	}
}
func TestConfigRejectsInvalid(t *testing.T) {
	for _, fragment := range []string{"workers: 0", "typo: true", "interval: 0s", "max_items: 1"} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		os.WriteFile(p, []byte(fragment+"\nsources:\n  - id: demo\n    url: https://example.com\n"), 0600)
		if _, e := LoadConfig(p); e == nil {
			t.Fatal("accepted", fragment)
		}
	}
}

func TestSourceFailureDoesNotBlockHealthy(t *testing.T) {
	var base string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good" {
			fmt.Fprintf(w, `<rss version="2.0"><channel><title>Good</title><item><title>New article</title><link>%s/article</link><description>Available summary</description></item></channel></rss>`, base)
			return
		}
		http.Error(w, "unavailable", 503)
	}))
	defer upstream.Close()
	base = upstream.URL
	good := Source{ID: "good", URL: base + "/good", FeedURL: base + "/good"}
	bad := Source{ID: "bad", URL: base + "/bad"}
	c := Config{Sources: []Source{bad, good}, InitialItems: 1, MaxItems: 100, Timeout: time.Second, Workers: 2}
	s := testStore(t)
	svc := Service{Config: c, Store: s, Fetcher: NewFetcher(c)}
	if err := svc.Run(context.Background(), true); err == nil {
		t.Fatal("expected bad source failure")
	}
	states, err := s.Statuses(context.Background(), c.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if states[0].Error == "" || states[1].Error != "" || states[1].Count != 1 {
		t.Fatalf("source isolation failed: %+v", states)
	}
}

func TestRSSSupplementAndPagination(t *testing.T) {
	var base string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			fmt.Fprintf(w, `<rss version="2.0"><channel><title>x</title><item><title>One</title><link>%s/blog/one/</link></item></channel></rss>`, base)
		case "/blog/":
			fmt.Fprint(w, `<a href="/blog/one">One again</a><a href="/blog/two">Two</a><a class="next" href="/page2">Next</a>`)
		case "/page2":
			fmt.Fprint(w, `<a href="/blog/three">Three</a><a class="next" href="/blog/">Cycle</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base = upstream.URL
	s := Source{URL: base + "/blog/", FeedURL: base + "/rss", LinkSelector: "a", URLPattern: `/blog/[^/]+/?$`, NextSelector: "a.next", MaxPages: 3}
	a, warning, err := NewFetcher(Config{Timeout: time.Second, InitialItems: 20}).Discover(context.Background(), s)
	if err != nil || warning != "" || len(a) != 3 {
		t.Fatalf("got %d %s %v", len(a), warning, err)
	}
}

func TestShippedConfiguration(t *testing.T) {
	c, err := LoadConfig("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sources) != 24 {
		t.Fatalf("sources: %d", len(c.Sources))
	}
}
