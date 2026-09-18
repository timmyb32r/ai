package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/araddon/dateparse"
	readability "github.com/go-shiori/go-readability"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

var safeHTML = bluemonday.UGCPolicy()

func absolute(base, ref string) string {
	u, e := url.Parse(strings.TrimSpace(ref))
	if e != nil || ref == "" {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	u = b.ResolveReference(u)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}
func canonical(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	u.Fragment = ""
	if u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || k == "hsLang" || k == "fbclid" || k == "gclid" || k == "traceId" || k == "policyId" || k == "frompage" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func normalizeHTML(content, base string) string {
	d, e := goquery.NewDocumentFromReader(strings.NewReader(content))
	if e != nil {
		return ""
	}
	d.Find("a[href],img[src]").Each(func(_ int, s *goquery.Selection) {
		key := "href"
		if goquery.NodeName(s) == "img" {
			key = "src"
		}
		v, _ := s.Attr(key)
		s.SetAttr(key, absolute(base, v))
	})
	d.Find("img").Each(func(_ int, s *goquery.Selection) {
		if v, ok := s.Attr("data-src"); ok {
			s.SetAttr("src", absolute(base, v))
		}
	})
	v, _ := d.Find("body").Html()
	return safeHTML.Sanitize(v)
}
func text(s string) string { return strings.Join(strings.Fields(s), " ") }
func meta(d *goquery.Document, selector string) string {
	v, _ := d.Find(selector).First().Attr("content")
	return strings.TrimSpace(v)
}
func parseDate(raw string) time.Time { t, _ := dateparse.ParseAny(strings.TrimSpace(raw)); return t }
func parseFeed(body, base string) ([]Article, error) {
	parsed, e := gofeed.NewParser().ParseString(body)
	if e != nil {
		return nil, e
	}
	out := []Article{}
	for _, v := range parsed.Items {
		rawLink := absolute(base, v.Link)
		bu, _ := url.Parse(base)
		lu, _ := url.Parse(rawLink)
		if lu != nil && bu != nil && lu.Host == bu.Host && bu.Scheme == "https" {
			lu.Scheme = "https"
			rawLink = lu.String()
		}
		u := canonical(rawLink)
		if u == "" {
			continue
		}
		a := Article{URL: u, Title: text(v.Title), Summary: normalizeHTML(v.Description, u), Content: normalizeHTML(v.Content, u)}
		if v.PublishedParsed != nil {
			a.Published = *v.PublishedParsed
		} else if v.UpdatedParsed != nil {
			a.Published = *v.UpdatedParsed
		}
		if v.Author != nil {
			a.Author = v.Author.Name
		}
		if v.Image != nil {
			a.Image = absolute(u, v.Image.URL)
		}
		if a.Title == "" {
			a.Title = u
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("feed contains no article links")
	}
	sortArticles(out)
	return out, nil
}
func sortArticles(a []Article) {
	sort.SliceStable(a, func(i, j int) bool {
		if a[i].Published.IsZero() || a[j].Published.IsZero() {
			return !a[i].Published.IsZero() && a[j].Published.IsZero()
		}
		return a[i].Published.After(a[j].Published)
	})
}
func parseListing(body, base string, s Source) ([]Article, string, error) {
	d, e := goquery.NewDocumentFromReader(strings.NewReader(body))
	if e != nil {
		return nil, "", e
	}
	selector := s.LinkSelector
	if selector == "" {
		selector = "article a[href], main h2 a[href], main h3 a[href]"
	}
	var pattern *regexp.Regexp
	if s.URLPattern != "" {
		pattern = regexp.MustCompile(s.URLPattern)
	}
	seen := map[string]bool{}
	out := []Article{}
	d.Find(selector).Each(func(_ int, el *goquery.Selection) {
		href, _ := el.Attr("href")
		rawURL := absolute(base, href)
		u := canonical(rawURL)
		if u == "" || u == canonical(s.URL) || seen[u] || (pattern != nil && !(pattern.MatchString(u) || pattern.MatchString(u+"/"))) {
			return
		}
		cardSelector := s.CardSelector
		if cardSelector == "" {
			cardSelector = "article, .w-dyn-item, li"
		}
		card := el.Closest(cardSelector)
		title := text(el.Find("h1,h2,h3,h4").First().Text())
		if title == "" {
			title = text(card.Find("h1,h2,h3,h4,.article-title").First().Text())
		}
		if title == "" {
			title = text(el.Text())
		}
		if title == "" || strings.HasPrefix(strings.ToLower(title), "read more") || title == "Читать статью" {
			for parent := el.Parent(); parent.Length() > 0; parent = parent.Parent() {
				tag := goquery.NodeName(parent)
				if tag == "main" || tag == "body" || tag == "nav" || tag == "header" {
					break
				}
				headings := parent.Find("h2,h3,h4,h5,h6")
				if headings.Length() == 1 {
					title = text(headings.Text())
					break
				}
				if headings.Length() > 1 {
					break
				}
			}
		}
		if title == "" {
			title, _ = el.Attr("data-title")
		}
		if title == "" {
			title = text(el.Closest("[class*=ArticleCard_card], article").Find("h2,h3").First().Text())
		}
		if title == "" {
			title, _ = el.Attr("aria-label")
		}
		if title == "" {
			title, _ = el.Find("img").First().Attr("alt")
		}
		if title == "" {
			return
		}
		seen[u] = true
		a := Article{URL: u, Title: title}

		date, _ := card.Find("time").First().Attr("datetime")
		a.Published = parseDate(date)
		a.Summary = html.EscapeString(text(card.Find("p").First().Text()))
		img, _ := card.Find("img").First().Attr("src")
		a.Image = absolute(base, img)
		out = append(out, a)
	})
	next := ""
	if s.NextSelector != "" {
		ref, _ := d.Find(s.NextSelector).First().Attr("href")
		next = absolute(base, ref)
	}
	return out, next, nil
}
func (f *Fetcher) Discover(ctx context.Context, s Source) ([]Article, string, error) {
	if s.Adapter == "cloudera" {
		return f.cloudera(ctx, s)
	}
	var warnings []string
	var rssArticles []Article
	if s.FeedURL != "" {
		body, base, e := f.Get(ctx, s.FeedURL, false, "")
		if e == nil {
			var a []Article
			a, e = parseFeed(body, base)
			if e == nil {
				if len(a) >= max(1, f.InitialItems) {
					return a, "", nil
				}
				rssArticles = a
			}
		}
		if e != nil {
			warnings = append(warnings, fmt.Sprintf("configured RSS: %v", e))
		}
	}
	listing := s.ListingURL
	if listing == "" {
		listing = s.URL
	}
	body, base, e := f.listing(ctx, listing, s, false)
	if e != nil && s.BrowserFallback && !s.Browser {
		body, base, e = f.listing(ctx, listing, s, true)
	}
	if e != nil {
		if len(rssArticles) > 0 {
			return rssArticles, "HTML supplement: " + e.Error(), nil
		}
		return nil, strings.Join(warnings, "; "), e
	}
	// Explicit selectors avoid unrelated auto-discovered feeds (e.g. release feeds).
	if s.FeedURL == "" && s.LinkSelector == "" {
		d, _ := goquery.NewDocumentFromReader(strings.NewReader(body))
		var links []string
		d.Find(`link[rel="alternate"]`).Each(func(_ int, el *goquery.Selection) {
			typ, _ := el.Attr("type")
			if strings.Contains(typ, "rss") || strings.Contains(typ, "atom") {
				h, _ := el.Attr("href")
				links = append(links, absolute(base, h))
			}
		})
		for _, u := range links {
			b, loc, err := f.Get(ctx, u, false, "")
			if err == nil {
				a, err := parseFeed(b, loc)
				if err == nil {
					return a, "", nil
				}
			}
		}
	}
	out, next, e := parseListing(body, base, s)
	if e == nil && len(out) == 0 && s.BrowserFallback && !s.Browser {
		body, base, e = f.listing(ctx, listing, s, true)
		if e == nil {
			out, next, e = parseListing(body, base, s)
		}
	}
	if e != nil {
		if len(rssArticles) > 0 {
			return rssArticles, "HTML supplement: " + e.Error(), nil
		}
		return nil, strings.Join(warnings, "; "), e
	}
	queue := append([]string{}, s.ExtraListingURLs...)
	if next != "" {
		queue = append([]string{next}, queue...)
	}
	visited := map[string]bool{listing: true}
	pages := s.MaxPages
	if pages == 0 {
		pages = 3
	}
	for n := 1; n < pages && len(queue) > 0; n++ {
		next = queue[0]
		queue = queue[1:]
		if visited[next] {
			continue
		}
		visited[next] = true
		b, loc, err := f.listing(ctx, next, s, false)
		if err != nil {
			warnings = append(warnings, "pagination: "+err.Error())
			break
		}
		more, link, err := parseListing(b, loc, s)
		if parsed, feedErr := parseFeed(b, loc); feedErr == nil {
			more = parsed
			link = ""
			err = nil
		}
		if err != nil {
			return nil, "", err
		}
		out = append(out, more...)
		if link != "" {
			queue = append(queue, link)
		}
	}
	out = append(rssArticles, out...)
	unique := []Article{}
	seen := map[string]bool{}
	for _, a := range out {
		if !seen[a.URL] {
			seen[a.URL] = true
			unique = append(unique, a)
		}
	}
	sortArticles(unique)
	if len(unique) == 0 {
		return nil, strings.Join(warnings, "; "), fmt.Errorf("no articles matched; check selectors or site access")
	}
	return unique, strings.Join(warnings, "; "), nil
}
func (f *Fetcher) Enrich(ctx context.Context, s Source, a Article) (Article, error) {
	body, base, e := f.Get(ctx, a.URL, false, "")
	if e == nil {
		a, e = extractArticle(body, base, s, a)
	}
	if e != nil && s.BrowserFallback {
		body, base, renderErr := f.Get(ctx, a.URL, true, "")
		if renderErr == nil {
			return extractArticle(body, base, s, a)
		}
		return a, fmt.Errorf("%v; browser: %w", e, renderErr)
	}
	return a, e
}
func extractArticle(body, base string, s Source, a Article) (Article, error) {
	d, e := goquery.NewDocumentFromReader(strings.NewReader(body))
	if e != nil {
		return a, e
	}
	u, _ := url.Parse(base)
	title := meta(d, `meta[property="og:title"]`)
	if title == "" {
		title = text(d.Find("h1").First().Text())
	}
	if title != "" {
		a.Title = title
	}
	if v := meta(d, `meta[name="description"],meta[property="og:description"]`); v != "" {
		a.Summary = html.EscapeString(v)
	}
	if v := meta(d, `meta[property="og:image"]`); v != "" {
		a.Image = absolute(base, v)
	}
	if v := meta(d, `meta[name="author"]`); v != "" {
		a.Author = v
	}
	date := meta(d, `meta[property="article:published_time"],meta[name="date"],meta[name="pubdate"]`)
	if date == "" {
		date, _ = d.Find("time[datetime]").First().Attr("datetime")
	}
	if t := parseDate(date); !t.IsZero() {
		a.Published = t
	}
	readArticleMetadata(d, &a)
	content := ""
	if s.ContentSelector != "" {
		content, _ = d.Find(s.ContentSelector).First().Html()
	} else {
		article, err := readability.FromReader(strings.NewReader(body), u)
		if err == nil {
			content = article.Content
			if article.Byline != "" && a.Author == "" {
				a.Author = article.Byline
			}
			if article.PublishedTime != nil && a.Published.IsZero() {
				a.Published = *article.PublishedTime
			}
		}
	}
	content = normalizeHTML(content, base)
	if len(text(bluemonday.StrictPolicy().Sanitize(content))) < 100 {
		return a, fmt.Errorf("article body too short; using available feed content or summary")
	}
	if len(content) > len(a.Content) {
		a.Content = content
	}
	return a, nil
}

func (f *Fetcher) cloudera(ctx context.Context, s Source) ([]Article, string, error) {
	body, _, e := f.Get(ctx, s.ListingURL, false, "")
	if e != nil {
		return nil, "", e
	}
	var data struct {
		Articles []struct {
			Title                  string
			AuthorsFormatted       string
			PublishedDateFormatted string
			ImageURL               string
			ArticleLink            struct{ LinkAttrs struct{ Href string } }
		}
	}
	if e = json.Unmarshal([]byte(body), &data); e != nil {
		return nil, "", e
	}
	out := []Article{}
	for _, v := range data.Articles {
		u := absolute(s.URL, v.ArticleLink.LinkAttrs.Href)
		if u == "" {
			continue
		}
		out = append(out, Article{URL: canonical(u), Title: text(v.Title), Author: html.UnescapeString(bluemonday.StrictPolicy().Sanitize(v.AuthorsFormatted)), Published: parseDate(v.PublishedDateFormatted), Image: absolute(s.URL, v.ImageURL)})
	}
	sortArticles(out)
	if len(out) == 0 {
		return nil, "", fmt.Errorf("Cloudera API returned no articles")
	}
	return out, "", nil
}

// Only article schema objects contribute metadata; WebSite and Breadcrumb dates do not.
func readArticleMetadata(d *goquery.Document, a *Article) {
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, v := range x {
				walk(v)
			}
		case map[string]any:
			typ, _ := x["@type"].(string)
			if typ == "Article" || typ == "BlogPosting" || typ == "NewsArticle" || typ == "TechArticle" {
				if a.Published.IsZero() {
					if date, ok := x["datePublished"].(string); ok {
						a.Published = parseDate(date)
					}
				}
				if a.Author == "" {
					authors := x["author"]
					if obj, ok := authors.(map[string]any); ok {
						a.Author, _ = obj["name"].(string)
					}
					if list, ok := authors.([]any); ok {
						var names []string
						for _, v := range list {
							if obj, ok := v.(map[string]any); ok {
								if name, ok := obj["name"].(string); ok {
									names = append(names, name)
								}
							}
						}
						a.Author = strings.Join(names, ", ")
					}
				}
			}
			if graph, ok := x["@graph"]; ok {
				walk(graph)
			}
		}
	}
	d.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		var v any
		if json.Unmarshal([]byte(s.Text()), &v) == nil {
			walk(v)
		}
	})
}
