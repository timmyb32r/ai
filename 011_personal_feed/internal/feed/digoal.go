package feed

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	goldtext "github.com/yuin/goldmark/text"
)

const (
	digoalRawBase   = "https://raw.githubusercontent.com/digoal/blog/master/"
	digoalBlobBase  = "https://github.com/digoal/blog/blob/master/"
	digoalReadmeURL = digoalRawBase + "README.md"
	digoalLimit     = 100
)

var digoalPathPattern = regexp.MustCompile(`^([0-9]{6})/([0-9]{8})_([0-9]{2})\.md$`)

func digoalPathDate(path string) (time.Time, int, bool) {
	m := digoalPathPattern.FindStringSubmatch(path)
	if m == nil || m[1] != m[2][:6] {
		return time.Time{}, 0, false
	}
	date, err := time.Parse("20060102", m[2])
	if err != nil || date.Year() < 1 {
		return time.Time{}, 0, false
	}
	ordinal, _ := strconv.Atoi(m[3])
	return date, ordinal, true
}

func (f *Fetcher) digoal(ctx context.Context, s Source) ([]Article, string, error) {
	listing := s.ListingURL
	if listing == "" {
		listing = digoalReadmeURL
	}
	if listing != digoalReadmeURL {
		return nil, "", fmt.Errorf("digoal: listing_url must be %s", digoalReadmeURL)
	}
	body, err := f.digoalGet(ctx, listing)
	if err != nil {
		return nil, "", err
	}
	articles, err := parseDigoalREADME([]byte(body))
	return articles, "", err
}

func parseDigoalREADME(source []byte) ([]Article, error) {
	md := goldmark.New()
	doc := md.Parser().Parse(goldtext.NewReader(source))
	type candidate struct {
		path    string
		date    time.Time
		ordinal int
		link    *ast.Link
	}
	unique := map[string]candidate{}
	err := ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		link, ok := node.(*ast.Link)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		path := strings.TrimPrefix(string(link.Destination), digoalBlobBase)
		path = strings.TrimPrefix(path, "./")
		date, ordinal, valid := digoalPathDate(path)
		if valid {
			unique[path] = candidate{path, date, ordinal, link}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	ordered := make([]candidate, 0, len(unique))
	for _, c := range unique {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].date.Equal(ordered[j].date) {
			return ordered[i].date.After(ordered[j].date)
		}
		return ordered[i].ordinal > ordered[j].ordinal
	})
	articles := make([]Article, 0, min(digoalLimit, len(ordered)))
	for _, c := range ordered {
		// Render the selected link label rather than slicing Markdown: escaped
		// brackets, inline code, emphasis and entities all keep their visible text.
		var rendered bytes.Buffer
		if err := md.Renderer().Render(&rendered, source, c.link); err != nil {
			return nil, err
		}
		label, err := goquery.NewDocumentFromReader(&rendered)
		if err != nil {
			return nil, err
		}
		title := text(label.Text())
		if title == "" {
			continue
		}
		articles = append(articles, Article{URL: digoalBlobBase + c.path, Title: title, Author: "Digoal", Published: c.date})
		if len(articles) == digoalLimit {
			break
		}
	}
	if len(articles) == 0 {
		return nil, fmt.Errorf("digoal: README contains no dated article links")
	}
	return articles, nil
}

func (f *Fetcher) enrichDigoal(ctx context.Context, _ Source, a Article) (Article, error) {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(a.URL, digoalBlobBase) {
		return a, fmt.Errorf("digoal: invalid article URL")
	}
	path := strings.TrimPrefix(a.URL, digoalBlobBase)
	if _, _, valid := digoalPathDate(path); !valid {
		return a, fmt.Errorf("digoal: invalid dated article path")
	}
	rawURL := digoalRawBase + path
	body, err := f.digoalGet(ctx, rawURL)
	if err != nil {
		return a, err
	}
	// Raw HTML is parsed only as data and sanitized below. This preserves the
	// author's inline image tags while removing scripts, handlers and unsafe URLs.
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(goldhtml.WithUnsafe()))
	var rendered bytes.Buffer
	if err := md.Convert([]byte(body), &rendered); err != nil {
		return a, err
	}
	if rendered.Len() > maxBody {
		return a, fmt.Errorf("digoal: rendered article exceeds %d bytes", maxBody)
	}
	doc, err := goquery.NewDocumentFromReader(&rendered)
	if err != nil {
		return a, err
	}
	doc.Find("img").Each(func(_ int, image *goquery.Selection) {
		ref, _ := image.Attr("src")
		if lazy, ok := image.Attr("data-src"); ok {
			ref = lazy
		}
		image.SetAttr("src", absolute(rawURL, ref))
	})
	doc.Find("a[href]").Each(func(_ int, link *goquery.Selection) {
		ref, _ := link.Attr("href")
		resolved := absolute(a.URL, ref)
		// Keep Markdown section anchors; the shared article URL canonicalizer
		// intentionally drops fragments for deduplication, not navigation.
		if relative, err := url.Parse(ref); err == nil && relative.Fragment != "" && resolved != "" {
			target, _ := url.Parse(resolved)
			target.Fragment = relative.Fragment
			resolved = target.String()
		}
		link.SetAttr("href", resolved)
	})
	html, err := doc.Find("body").Html()
	if err != nil {
		return a, err
	}
	a.Content = safeHTML.Sanitize(html)
	if len(text(bluemonday.StrictPolicy().Sanitize(a.Content))) < 100 {
		return a, fmt.Errorf("digoal: article body too short")
	}
	return a, nil
}

// The adapter never follows a Markdown URL to choose a metadata endpoint.
// Also reject cross-endpoint redirects before issuing the redirected request.
func (f *Fetcher) digoalGet(ctx context.Context, raw string) (string, error) {
	client := *f.Client
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.String() != raw || len(via) >= 10 {
			return fmt.Errorf("digoal: unexpected raw-content redirect")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	copy := *f
	copy.Client = &client
	body, _, err := copy.Get(ctx, raw, false, "")
	return body, err
}
