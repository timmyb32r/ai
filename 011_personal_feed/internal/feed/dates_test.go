package feed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const meltanoDateSelector = ".blog-list-item-date, .blog-author-date .bold"

func TestMeltanoDateSelector(t *testing.T) {
	src := Source{URL: "https://meltano.com/blog", LinkSelector: "article.blog-list-item .blog-list-item-text > a[href]", CardSelector: "article.blog-list-item", DateSelector: meltanoDateSelector}
	// The site's cards use visible <p> dates; full articles put the date in
	// a nested span after an "on " prefix, without <time> or publication meta.
	listing := `<article class="blog-list-item"><div class="blog-list-item-text"><p class="blog-list-item-date">17 Sep 2026</p><a href="/blog/arrow"><h5>Arrow</h5></a></div></article>
<article class="blog-list-item"><div class="blog-list-item-text"><p class="blog-list-item-date">10 Sep 2026</p><a href="/blog/editor"><h5>Editor</h5></a></div></article>`
	articles, _, err := parseListing(listing, src.URL, src)
	if err != nil || len(articles) != 2 {
		t.Fatalf("listing: %v, %+v", err, articles)
	}
	want := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if !articles[0].Published.Equal(want) || !articles[1].Published.Equal(want.AddDate(0, 0, -7)) {
		t.Fatalf("card dates were not scoped to each article: %+v", articles)
	}
	body := `<html><body><p class="blog-author-date">on <span class="bold">September 17 2026</span></p><article><h1>Arrow</h1><p>` + strings.Repeat("Useful article text. ", 20) + `</p></article></body></html>`
	src.ContentSelector = "article"
	article, err := extractArticle(body, "https://meltano.com/blog/arrow", src, Article{})
	if err != nil || !article.Published.Equal(want) || len(article.Content) < 100 {
		t.Fatalf("full article: %v, %+v", err, article)
	}
}

func TestOptionalDateSelectorPreservesExistingDates(t *testing.T) {
	want := time.Date(2026, 9, 17, 12, 34, 0, 0, time.UTC)
	for _, visibleDate := range []string{"", "not a date"} {
		body := `<article><p class="date">` + visibleDate + `</p><p>` + strings.Repeat("Useful article text. ", 20) + `</p></article>`
		got, err := extractArticle(body, "https://example.com/post", Source{DateSelector: ".date", ContentSelector: "article"}, Article{Published: want})
		if err != nil || !got.Published.Equal(want) {
			t.Fatalf("lost listing date with visible %q: %v, %v", visibleDate, err, got.Published)
		}
	}
	listing := `<article><p class="date">17 Sep 2026</p><h2><a href="/post">A post</a></h2></article>`
	got, _, err := parseListing(listing, "https://example.com", Source{URL: "https://example.com"})
	if err != nil || len(got) != 1 || !got[0].Published.IsZero() {
		t.Fatalf("unconfigured visible date changed old behavior: %v, %+v", err, got)
	}
}

func TestDateSelectorLeavesStandardPublicationMetadataAuthoritative(t *testing.T) {
	body := `<html><head><meta property="article:published_time" content="2026-09-17T12:34:00Z"></head><body><article><p class="date">10 Sep 2026</p><p>` + strings.Repeat("Useful article text. ", 20) + `</p></article></body></html>`
	got, err := extractArticle(body, "https://example.com/post", Source{DateSelector: ".date", ContentSelector: "article"}, Article{})
	want := time.Date(2026, 9, 17, 12, 34, 0, 0, time.UTC)
	if err != nil || !got.Published.Equal(want) {
		t.Fatalf("visible fallback overrode standard date: %v, %v", err, got.Published)
	}
}

func TestDateSelectorValidation(t *testing.T) {
	for _, tc := range []struct {
		selector string
		valid    bool
	}{{meltanoDateSelector, true}, {"[", false}} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		body := "sources:\n  - id: meltano\n    url: https://meltano.com/blog\n    date_selector: '" + tc.selector + "'\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if tc.valid {
			if err != nil || cfg.Sources[0].DateSelector != tc.selector {
				t.Fatalf("valid date selector: %v, %+v", err, cfg)
			}
		} else if err == nil {
			t.Fatal("invalid date selector accepted")
		}
	}
}
