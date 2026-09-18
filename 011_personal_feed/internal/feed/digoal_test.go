package feed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func TestDigoalREADMEValidatesDeduplicatesAndOrdersArticles(t *testing.T) {
	readme := `# Featured old articles
[Featured title](202001/20200110_01.md)
[Newer day](202609/20260917_01.md)
[Earlier ordinal](202609/20260916_01.md)
[Wrong month](202608/20260918_01.md)
[Invalid calendar date](202602/20260230_01.md)
[External](https://other.example/202609/20260918_01.md)
[Traversal](../202609/20260918_01.md)
[Query](202609/20260918_01.md?raw=1)
[Not an article](README.md)
[Not Markdown](202609/20260918_01.html)
[Future invalid](202699/20269918_01.md)
[Zero year](000001/00000101_01.md)
[Unclosed](202609/20260918_01.md
[Old canonical title](202001/20200110_01.md)
[Escaped \[brackets\] &amp; **bold**](202609/20260916_10.md)
[Code ` + "`x[y]`" + `](./202609/20260916_02.md)
[Leap day](202402/20240229_01.md)
`
	articles, err := parseDigoalREADME([]byte(readme))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"202609/20260917_01.md", "202609/20260916_10.md", "202609/20260916_02.md", "202609/20260916_01.md", "202402/20240229_01.md", "202001/20200110_01.md"}
	if len(articles) != len(want) {
		t.Fatalf("got %d articles: %+v", len(articles), articles)
	}
	for i, path := range want {
		if articles[i].URL != digoalBlobBase+path || articles[i].Author != "Digoal" || articles[i].Published.IsZero() {
			t.Fatalf("article %d: %+v", i, articles[i])
		}
	}
	if articles[1].Title != "Escaped [brackets] & bold" || articles[2].Title != "Code x[y]" {
		t.Fatalf("Markdown label corrupted: %q / %q", articles[1].Title, articles[2].Title)
	}
	if articles[len(articles)-1].Title != "Old canonical title" {
		t.Fatal("duplicate featured link was not replaced by indexed title")
	}
}

func TestDigoalREADMEKeepsLatestHundredFromUnsortedInput(t *testing.T) {
	var readme strings.Builder
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 140; i++ {
		date := start.AddDate(0, 0, i)
		fmt.Fprintf(&readme, "[Article %d](%s/%s_01.md)\n", i, date.Format("200601"), date.Format("20060102"))
	}
	articles, err := parseDigoalREADME([]byte(readme.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 100 || articles[0].Title != "Article 139" || articles[99].Title != "Article 40" {
		t.Fatalf("latest hundred not selected: %d, %q, %q", len(articles), articles[0].Title, articles[len(articles)-1].Title)
	}
	if _, err := parseDigoalREADME([]byte("[Other](README.md)")); err == nil {
		t.Fatal("empty discovery must fail rather than replace the feed")
	}
}

type digoalTransport func(*http.Request) (*http.Response, error)

func (f digoalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func digoalResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestDigoalFetchAndMarkdownEnrichment(t *testing.T) {
	articleURL := digoalBlobBase + "202609/20260916_01.md"
	var requested []string
	markdown := "# A different heading must not replace the README title\n\n" + strings.Repeat("PostgreSQL query planning and data architecture. ", 5) + `

![diagram](../pic/architecture.png)
[Related](../202608/20260810_01.md#planning)
[Section](#query-planning)
<img src="figures/local.png" onerror="alert(1)">
<img data-src="../pic/lazy.png" src="placeholder.png">
<a href="javascript:alert(1)" onclick="alert(2)">Unsafe link</a>
<script>alert('unsafe script')</script>

| Column | Meaning |
| --- | --- |
| id | Identifier |
`
	f := NewFetcher(Config{Timeout: time.Second, InitialItems: 20})
	f.Client.Transport = digoalTransport(func(r *http.Request) (*http.Response, error) {
		requested = append(requested, r.URL.String())
		switch r.URL.String() {
		case digoalReadmeURL:
			return digoalResponse(r, 200, "[Original README title](202609/20260916_01.md)"), nil
		case digoalRawBase + "202609/20260916_01.md":
			return digoalResponse(r, 200, markdown), nil
		default:
			t.Fatalf("unexpected network destination %s", r.URL)
			return nil, fmt.Errorf("unexpected URL")
		}
	})
	articles, warning, err := f.digoal(context.Background(), Source{ListingURL: digoalReadmeURL})
	if err != nil || warning != "" || len(articles) != 1 {
		t.Fatalf("discover: %+v %q %v", articles, warning, err)
	}
	a, err := f.enrichDigoal(context.Background(), Source{}, articles[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.URL != articleURL || a.Title != "Original README title" || !a.Published.Equal(articles[0].Published) || a.Author != "Digoal" || len(requested) != 2 {
		t.Fatalf("metadata changed: %+v; requests %v", a, requested)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(a.Content))
	if err != nil {
		t.Fatal(err)
	}
	var images, links []string
	doc.Find("img").Each(func(_ int, s *goquery.Selection) { v, _ := s.Attr("src"); images = append(images, v) })
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) { v, _ := s.Attr("href"); links = append(links, v) })
	wantImages := []string{digoalRawBase + "pic/architecture.png", digoalRawBase + "202609/figures/local.png", digoalRawBase + "pic/lazy.png"}
	if fmt.Sprint(images) != fmt.Sprint(wantImages) {
		t.Fatalf("wrong image bases: %v", images)
	}
	if len(links) < 2 || links[0] != digoalBlobBase+"202608/20260810_01.md#planning" || links[1] != articleURL+"#query-planning" {
		t.Fatalf("wrong link bases: %v", links)
	}
	for _, unsafe := range []string{"<script", "onerror", "onclick", "javascript:"} {
		if strings.Contains(a.Content, unsafe) {
			t.Fatalf("unsafe content %q survived: %s", unsafe, a.Content)
		}
	}
	if doc.Find("table").Length() != 1 {
		t.Fatal("GFM table was lost")
	}
}

func TestDigoalRejectsUntrustedMetadataEndpointsAndRedirects(t *testing.T) {
	f := NewFetcher(Config{Timeout: time.Second})
	requests := 0
	f.Client.Transport = digoalTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		response := digoalResponse(r, http.StatusFound, "")
		response.Header.Set("Location", "http://127.0.0.1/private")
		return response, nil
	})
	for _, listing := range []string{"http://raw.githubusercontent.com/digoal/blog/master/README.md", "https://raw.githubusercontent.com/other/private/master/README.md"} {
		if _, _, err := f.digoal(context.Background(), Source{ListingURL: listing}); err == nil {
			t.Fatalf("accepted listing %s", listing)
		}
	}
	for _, raw := range []string{
		"https://github.com/other/blog/blob/master/202609/20260916_01.md",
		"https://github.com.evil.test/digoal/blog/blob/master/202609/20260916_01.md",
		"https://github.com@127.0.0.1/digoal/blog/blob/master/202609/20260916_01.md",
		digoalBlobBase + "../202609/20260916_01.md",
		digoalBlobBase + "202609%2f20260916_01.md",
		digoalBlobBase + "202608/20260916_01.md",
		digoalBlobBase + "202602/20260230_01.md",
		digoalBlobBase + "202609/20260916_01.md?raw=1",
		digoalBlobBase + "202609/20260916_01.md#fragment",
	} {
		if _, err := f.enrichDigoal(context.Background(), Source{}, Article{URL: raw}); err == nil {
			t.Fatalf("accepted article %s", raw)
		}
	}
	if requests != 0 {
		t.Fatalf("rejected metadata addresses performed %d requests", requests)
	}
	if _, _, err := f.digoal(context.Background(), Source{}); err == nil || requests != 1 {
		t.Fatalf("unsafe redirect was followed: %d requests, %v", requests, err)
	}
}
