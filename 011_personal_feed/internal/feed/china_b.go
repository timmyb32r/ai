package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const mirrorshipArticleLimit = 120

var mirrorshipPublishedMillis = regexp.MustCompile(`new Date\(\s*([0-9]{13})\s*\)`)

// Mirrorship's category cards omit dates and are partly curated, so the global
// latest articles must be selected from dated details across all four categories.
func (f *Fetcher) mirrorship(ctx context.Context, s Source) ([]Article, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	base, err := url.Parse(s.URL)
	if err != nil || strings.TrimRight(base.Path, "/") != "/zh-CN/blog" {
		return nil, "", fmt.Errorf("Mirrorship requires its /zh-CN/blog index URL")
	}
	base.RawQuery, base.Fragment = "", ""
	type card struct{ url, title string }
	var cards []card
	seen := make(map[string]bool)
	for _, category := range []string{"", "/Comparison", "/technical", "/product"} {
		listing := *base
		listing.Path = "/zh-CN/blog" + category
		body, loc, err := f.Get(ctx, listing.String(), false, "")
		if err != nil {
			return nil, "", fmt.Errorf("Mirrorship category %s: %w", category, err)
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		count := 0
		missingTitle := false
		doc.Find("a.blog[href]").Each(func(_ int, el *goquery.Selection) {
			href, _ := el.Attr("href")
			u, err := url.Parse(absolute(loc, href))
			if err != nil || u.Host != base.Host || !strings.HasPrefix(u.Path, "/zh-CN/blog/d/") || strings.TrimPrefix(u.Path, "/zh-CN/blog/d/") == "" {
				return
			}
			u.RawQuery, u.Fragment = "", ""
			count++
			articleURL := canonical(u.String())
			if !seen[articleURL] {
				title := text(el.Find(".title").First().Text())
				if title == "" {
					missingTitle = true
					return
				}
				seen[articleURL] = true
				cards = append(cards, card{articleURL, title})
			}
		})
		if missingTitle {
			return nil, "", fmt.Errorf("Mirrorship category %s has an article without a title", category)
		}
		if count == 0 {
			return nil, "", fmt.Errorf("Mirrorship category %s has no article cards", category)
		}
		if len(cards) > mirrorshipArticleLimit {
			return nil, "", fmt.Errorf("Mirrorship has more than %d articles; refusing to truncate before comparing publication dates", mirrorshipArticleLimit)
		}
	}
	type result struct {
		card    card
		article Article
		err     error
	}
	fetchArticle := func(c card) (Article, error) {
		body, loc, err := f.Get(ctx, c.url, false, "")
		if err != nil {
			return Article{}, err
		}
		a, err := parseMirrorshipArticle(body, loc)
		a.URL = c.url
		return a, err
	}
	jobs := make(chan card, len(cards))
	results := make(chan result, len(cards))
	for _, c := range cards {
		jobs <- c
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for c := range jobs {
				a, err := fetchArticle(c)
				results <- result{c, a, err}
			}
		}()
	}
	go func() { workers.Wait(); close(results) }()
	var out []Article
	var retry []card
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("Mirrorship article %s: %w", result.card.url, result.err)
				cancel()
			}
			continue
		}
		if result.article.Title != result.card.title {
			retry = append(retry, result.card)
			continue
		}
		out = append(out, result.article)
	}
	if firstErr != nil {
		return nil, "", firstErr
	}
	// This origin sometimes serves another concurrent request's article body.
	// Check identity before accepting its date; retry mismatches after workers stop.
	for _, c := range retry {
		a, err := fetchArticle(c)
		if err != nil {
			return nil, "", fmt.Errorf("Mirrorship article %s retry: %w", c.url, err)
		}
		if a.Title != c.title {
			return nil, "", fmt.Errorf("Mirrorship article %s title mismatch after sequential retry: listing %q, article %q", c.url, c.title, a.Title)
		}
		out = append(out, a)
	}
	sortArticles(out)
	return out, "", nil
}

func parseMirrorshipArticle(body, base string) (Article, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return Article{}, err
	}
	a := Article{URL: canonical(base), Title: text(doc.Find("h1.title").First().Text())}
	date := mirrorshipPublishedMillis.FindStringSubmatch(doc.Find(".blog-date").First().Text())
	if len(date) != 2 {
		return a, fmt.Errorf("publication timestamp is missing")
	}
	millis, err := strconv.ParseInt(date[1], 10, 64)
	if err != nil || millis <= 0 {
		return a, fmt.Errorf("invalid publication timestamp")
	}
	a.Published = time.UnixMilli(millis).UTC()
	content := doc.Find(".ck-content.content").First()
	content.Find(".blog-date").Remove() // The unrendered Vue expression is not article text.
	a.Summary = html.EscapeString(text(content.Find("p").First().Text()))
	image, _ := content.Find("img[src]").First().Attr("src")
	a.Image = absolute(base, image)
	raw, _ := content.Html()
	a.Content = normalizeHTML(raw, base)
	if a.Title == "" || len(text(content.Text())) < 100 {
		return a, fmt.Errorf("article title or complete body is missing")
	}
	return a, nil
}

// Pingkai's ordinary index is ranked by recommendations. The site's own
// chronological view embeds full publication timestamps in its server-rendered data.
func (f *Fetcher) pingkai(ctx context.Context, s Source) ([]Article, string, error) {
	listing := s.ListingURL
	if listing == "" {
		listing = s.URL
	}
	u, err := url.Parse(listing)
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	q.Set("latest", "true")
	u.RawQuery = q.Encode()
	body, _, err := f.Get(ctx, u.String(), false, "")
	if err != nil {
		return nil, "", err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	var data struct {
		Props struct {
			PageProps struct {
				Blogs struct {
					Content []struct {
						Slug, Status, Title, Summary, PublishedAt string
						Author                                    struct{ Username string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(doc.Find("script#__NEXT_DATA__").Text()), &data); err != nil {
		return nil, "", fmt.Errorf("Pingkai article data: %w", err)
	}
	var out []Article
	seen := make(map[string]bool)
	for _, p := range data.Props.PageProps.Blogs.Content {
		if p.Status != "PUBLISHED" {
			continue
		}
		if p.Slug == "" || len(p.Slug) > 200 || strings.ContainsAny(p.Slug, "/?#") || text(p.Title) == "" {
			return nil, "", fmt.Errorf("Pingkai article has invalid slug or title")
		}
		if seen[p.Slug] {
			continue
		}
		seen[p.Slug] = true
		date := parseDate(p.PublishedAt)
		if date.IsZero() {
			return nil, "", fmt.Errorf("Pingkai article %s has no valid publication date", p.Slug)
		}
		articleURL := strings.TrimRight(s.URL, "/") + "/" + url.PathEscape(p.Slug)
		out = append(out, Article{URL: canonical(articleURL), Title: text(p.Title), Summary: normalizeHTML(p.Summary, articleURL), Author: p.Author.Username, Published: date})
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("Pingkai chronological index has no published articles")
	}
	sortArticles(out)
	return out, "", nil
}
