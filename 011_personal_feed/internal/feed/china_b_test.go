package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingkaiUsesChronologicalIndexAndPublicationTimestamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("latest") != "true" {
			t.Error("requested recommendations instead of chronological index")
			http.Error(w, "latest required", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"blogs":{"content":[
{"slug":"old","status":"PUBLISHED","title":"Older","publishedAt":"2026-09-17T08:00:00.000+00:00"},
{"slug":"new","status":"PUBLISHED","title":"新文章","summary":"A summary","publishedAt":"2026-09-18T09:03:11.000+00:00","author":{"username":"作者"}},
{"slug":"draft","status":"DRAFT","title":"Draft","publishedAt":"2026-09-19T00:00:00Z"}
]}}}}</script>`)
	}))
	defer srv.Close()
	f := NewFetcher(Config{InitialItems: 20, Timeout: time.Second})
	a, warning, err := f.pingkai(context.Background(), Source{URL: srv.URL + "/tidbcommunity/blog"})
	if err != nil || warning != "" || len(a) != 2 {
		t.Fatalf("articles=%+v warning=%q err=%v", a, warning, err)
	}
	if a[0].URL != srv.URL+"/tidbcommunity/blog/new" || a[0].Title != "新文章" || a[0].Author != "作者" || a[0].Published.Format(time.RFC3339) != "2026-09-18T09:03:11Z" {
		t.Fatalf("wrong newest article: %+v", a[0])
	}
}

func TestPingkaiRejectsUndatedOrInvalidArticleData(t *testing.T) {
	for _, field := range []string{"publishedAt", "slug"} {
		t.Run(field, func(t *testing.T) {
			post := map[string]string{"slug": "valid", "title": "A title", "status": "PUBLISHED", "publishedAt": "2026-09-18T00:00:00Z"}
			post[field] = ""
			data, _ := json.Marshal(post)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `<script id="__NEXT_DATA__">{"props":{"pageProps":{"blogs":{"content":[%s]}}}}</script>`, data)
			}))
			defer srv.Close()
			f := NewFetcher(Config{Timeout: time.Second})
			_, _, err := f.pingkai(context.Background(), Source{URL: srv.URL + "/tidbcommunity/blog"})
			if err == nil {
				t.Fatal("accepted invalid article")
			}
		})
	}
}

// Shared by the Mirrorship fixtures below.
func mirrorBody(title string, millis int64) string {
	return fmt.Sprintf(`<h1 class="title">%s</h1><div class="ck-content content"><p class="blog-date">本文发表于： &amp;{ new Date(%d).toLocaleDateString() }</p><p>%s</p><img src="/picture.png"></div>`, title, millis, strings.Repeat("A complete article. ", 20))
}

func TestMirrorshipCombinesAllCategoriesAndSortsActualDates(t *testing.T) {
	var active, peak, requests atomic.Int32
	baseDate := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	card := func(slug string) string {
		return fmt.Sprintf(`<a class="blog col-flex" href="/zh-CN/blog/d/%s"><p class="title"> Article  %s </p><h2 class="desc">Not the title</h2></a>`, slug, slug)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zh-CN/blog":
			for i := 0; i < 20; i++ {
				fmt.Fprint(w, card(fmt.Sprint(i)))
			}
			// Curated lists can put a new article beyond the first twenty.
			fmt.Fprint(w, card("newest"))
		case "/zh-CN/blog/Comparison":
			fmt.Fprint(w, card("0")) // Deduplicate across categories.
		case "/zh-CN/blog/technical":
			fmt.Fprint(w, card("technical"))
		case "/zh-CN/blog/product":
			fmt.Fprint(w, card("product"))
		default:
			requests.Add(1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			slug := strings.TrimPrefix(r.URL.Path, "/zh-CN/blog/d/")
			date := baseDate
			if slug == "newest" {
				date = date.AddDate(1, 0, 0)
			}
			fmt.Fprint(w, mirrorBody("Article "+slug, date.UnixMilli()))
		}
	}))
	defer srv.Close()
	f := NewFetcher(Config{Timeout: time.Second, InitialItems: 20})
	a, warning, err := f.mirrorship(context.Background(), Source{URL: srv.URL + "/zh-CN/blog"})
	if err != nil || warning != "" || len(a) != 23 || requests.Load() != 23 {
		t.Fatalf("count=%d requests=%d warning=%q err=%v", len(a), requests.Load(), warning, err)
	}
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("expected bounded parallelism, got %d", peak.Load())
	}
	if a[0].Title != "Article newest" || a[0].Published.Year() != 2026 || !strings.HasSuffix(a[0].URL, "/newest") {
		t.Fatalf("missed newest article behind curated order: %+v", a[0])
	}
	if !strings.Contains(a[0].Content, srv.URL+"/picture.png") || strings.Contains(a[0].Content, "new Date") {
		t.Fatalf("invalid full content: %s", a[0].Content)
	}
}

func TestMirrorshipArticleLimitIsExplicitBeforeDetailRequests(t *testing.T) {
	var details atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zh-CN/blog" {
			details.Add(1)
			return
		}
		for i := 0; i <= mirrorshipArticleLimit; i++ {
			fmt.Fprintf(w, `<a class="blog" href="/zh-CN/blog/d/%d"><p class="title">Article</p></a>`, i)
		}
	}))
	defer srv.Close()
	f := NewFetcher(Config{Timeout: time.Second})
	_, _, err := f.mirrorship(context.Background(), Source{URL: srv.URL + "/zh-CN/blog"})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") || details.Load() != 0 {
		t.Fatalf("silent truncation or unnecessary details: %v, requests=%d", err, details.Load())
	}
}

func TestMirrorshipRetriesForeignBodiesSequentiallyBeforeAcceptingDates(t *testing.T) {
	for _, remainsWrong := range []bool{false, true} {
		t.Run(fmt.Sprint("remains wrong=", remainsWrong), func(t *testing.T) {
			var requests [4]atomic.Int32
			var active, finishedFirst atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/zh-CN/blog/d/") {
					for i := range 4 {
						fmt.Fprintf(w, `<a class="blog" href="/zh-CN/blog/d/%d"><p class="title">Article %d</p></a>`, i, i)
					}
					return
				}
				var i int
				fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/zh-CN/blog/d/"), "%d", &i)
				n := active.Add(1)
				defer active.Add(-1)
				attempt := requests[i].Add(1)
				if attempt == 1 {
					time.Sleep(5 * time.Millisecond)
					finishedFirst.Add(1)
				} else if n != 1 || finishedFirst.Load() != 4 {
					t.Errorf("retry overlapped initial or other detail requests: active=%d initialFinished=%d", n, finishedFirst.Load())
				}
				if attempt == 1 || remainsWrong {
					// A syntactically valid, but unrelated, upstream response must
					// never assign its title, content or future date to this URL.
					fmt.Fprint(w, mirrorBody("Other article", time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()))
					return
				}
				date := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
				if i == 1 {
					date = date.AddDate(1, 0, 0)
				}
				fmt.Fprint(w, mirrorBody(fmt.Sprint("Article ", i), date.UnixMilli()))
			}))
			defer srv.Close()
			f := NewFetcher(Config{Timeout: time.Second})
			a, _, err := f.mirrorship(context.Background(), Source{URL: srv.URL + "/zh-CN/blog"})
			if remainsWrong {
				if err == nil || !strings.Contains(err.Error(), "title mismatch after sequential retry") || len(a) != 0 {
					t.Fatalf("accepted foreign article after retry: articles=%+v error=%v", a, err)
				}
				return
			}
			if err != nil || len(a) != 4 || a[0].URL != srv.URL+"/zh-CN/blog/d/1" || a[0].Title != "Article 1" || a[0].Published.Year() != 2026 {
				t.Fatalf("wrong article or chronology after retry: articles=%+v error=%v", a, err)
			}
			for i := range requests {
				if requests[i].Load() != 2 {
					t.Fatalf("article %d requested %d times, expected one bounded retry", i, requests[i].Load())
				}
			}
		})
	}
}

func TestMirrorshipMissingDatesAndCancellationFailWithoutInventingDates(t *testing.T) {
	t.Run("missing date", func(t *testing.T) {
		_, err := parseMirrorshipArticle(`<h1 class="title">A title</h1><div class="ck-content content"><p>A body without a publication date</p></div>`, "https://www.mirrorship.cn/zh-CN/blog/d/a")
		if err == nil || !strings.Contains(err.Error(), "timestamp") {
			t.Fatalf("invented publication date: %v", err)
		}
	})
	t.Run("cancel active detail requests", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/zh-CN/blog/d/") {
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, `<a class="blog" href="/zh-CN/blog/d/blocked"><p class="title">Blocked article</p></a>`)
		}))
		defer srv.Close()
		f := NewFetcher(Config{Timeout: 5 * time.Second})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, _, err := f.mirrorship(ctx, Source{URL: srv.URL + "/zh-CN/blog"})
		if err == nil || time.Since(started) > time.Second {
			t.Fatalf("cancellation did not stop adapter: %v, %v", err, time.Since(started))
		}
	})
}
