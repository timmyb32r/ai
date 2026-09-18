package feed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExplicitListingTitle(t *testing.T) {
	body := `<article><a href="/first"><p class="title">First title</p><h2>Description</h2><p>Read more</p></a></article><article><a href="/second"><p class="title">Second title</p><h2>Other description</h2></a></article>`
	s := Source{URL: "https://example.com", LinkSelector: "article a", CardSelector: "article", TitleSelector: ".title"}
	a, _, err := parseListing(body, s.URL, s)
	if err != nil || len(a) != 2 || a[0].Title != "First title" || a[1].Title != "Second title" {
		t.Fatalf("explicit titles: %+v %v", a, err)
	}
	s.TitleSelector = ".missing"
	a, _, err = parseListing(body, s.URL, s)
	if err != nil || a[0].Title != "Description" {
		t.Fatalf("existing title fallback: %+v %v", a, err)
	}
}

func TestExplicitArticleTitleOverridesSiteHeading(t *testing.T) {
	body := `<meta property="og:title" content="Company name"><h1>Company name</h1><article><h4 class="page-content-title">The actual article</h4><p>` + strings.Repeat("Useful article text. ", 20) + `</p></article>`
	a, err := extractArticle(body, "https://example.com/post", Source{TitleSelector: ".page-content-title", ContentSelector: "article"}, Article{Title: "The actual article"})
	if err != nil || a.Title != "The actual article" {
		t.Fatalf("article title replaced by generic site title: %+v %v", a, err)
	}
}

func TestRSSSupplementFillsDatesWithoutLosingContent(t *testing.T) {
	var base string
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			fmt.Fprintf(w, `<rss version="2.0"><channel><title>News</title><item><title>Newest</title><link>%s/new</link><description>Original RSS summary</description></item></channel></rss>`, base)
		} else {
			fmt.Fprint(w, `<article><a href="/new">Newest</a><time datetime="2026-09-10"></time></article><article><a href="/old">Older</a><time datetime="2026-09-03"></time></article>`)
		}
	}))
	defer web.Close()
	base = web.URL
	a, warning, err := NewFetcher(Config{Timeout: time.Second, InitialItems: 20}).Discover(context.Background(), Source{URL: base, FeedURL: base + "/rss", LinkSelector: "article a", CardSelector: "article"})
	if err != nil || warning != "" || len(a) != 2 || a[0].URL != base+"/new" || a[0].Published.IsZero() || !strings.Contains(a[0].Summary, "Original RSS summary") {
		t.Fatalf("RSS/HTML merge lost dates or content: %+v %s %v", a, warning, err)
	}
}

func TestTitleSelectorValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("sources:\n  - id: test\n    url: https://example.com\n    title_selector: '['\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("invalid title selector accepted")
	}
}
