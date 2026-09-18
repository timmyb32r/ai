package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

var infoQSlug = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var modbID = regexp.MustCompile(`^[0-9]+$`)

// These are the same public requests made by the publishers' own pages. Their
// APIs require a page Referer; no login cookies or challenge tokens are used.
func (f *Fetcher) chinaARequest(ctx context.Context, method, endpoint, referer string, payload any) ([]byte, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PersonalFeed/1.0)")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if referer != "" {
		req.Header.Set("Referer", referer)
		if u, e := url.Parse(referer); e == nil {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return nil, fmt.Errorf("publisher API: HTTP %d", r.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("publisher response exceeds %d bytes", maxBody)
	}
	return b, nil
}

type infoQArticle struct {
	UUID       string `json:"uuid"`
	Title      string `json:"article_title"`
	Summary    string `json:"article_summary"`
	Image      string `json:"article_cover"`
	Published  int64  `json:"publish_time"`
	Content    string `json:"content"`
	ContentURL string `json:"content_url"`
	Author     []struct {
		Nickname string `json:"nickname"`
	} `json:"author"`
}

func decodeInfoQ(body []byte, target any) error {
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("InfoQ response: %w", err)
	}
	if envelope.Code == nil || *envelope.Code != 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("InfoQ returned no successful public data")
	}
	return json.Unmarshal(envelope.Data, target)
}

func (f *Fetcher) infoqBigData(ctx context.Context, s Source) ([]Article, string, error) {
	// Topic 15 is the id returned by /topic/bigdata's public topicInfo payload.
	body, err := f.chinaARequest(ctx, http.MethodPost, "https://www.infoq.cn/public/v1/article/getList", s.URL, map[string]int{"id": 15, "type": 1, "ptype": 0, "size": 30})
	if err != nil {
		return nil, "", err
	}
	var records []infoQArticle
	if err = decodeInfoQ(body, &records); err != nil {
		return nil, "", err
	}
	out := make([]Article, 0, len(records))
	seen := map[string]bool{}
	for _, v := range records {
		if !infoQSlug.MatchString(v.UUID) || text(v.Title) == "" || v.Published <= 0 || seen[v.UUID] {
			continue
		}
		seen[v.UUID] = true
		a := Article{URL: "https://www.infoq.cn/article/" + v.UUID, Title: text(v.Title), Summary: html.EscapeString(v.Summary), Image: absolute(s.URL, v.Image), Published: time.UnixMilli(v.Published).UTC()}
		if len(v.Author) > 0 {
			a.Author = text(v.Author[0].Nickname)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("InfoQ returned no valid articles")
	}
	sortArticles(out)
	return out, "", nil
}

func (f *Fetcher) modbNews(ctx context.Context, s Source) ([]Article, string, error) {
	body, err := f.chinaARequest(ctx, http.MethodGet, "https://www.modb.pro/api/knowledges/find/v2?type=3&pageSize=30&pageNum=1", s.URL, nil)
	if err != nil {
		return nil, "", err
	}
	var response struct {
		List []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Summary string `json:"brief"`
			Author  string `json:"createdByName"`
			Image   string `json:"imageUrl"`
			Created string `json:"createdTime"`
			Access  string `json:"encryptLevel"`
		} `json:"list"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, "", fmt.Errorf("Modb response: %w", err)
	}
	out := make([]Article, 0, len(response.List))
	seen := map[string]bool{}
	for _, v := range response.List {
		if !modbID.MatchString(v.ID) || text(v.Title) == "" || seen[v.ID] || (v.Access != "" && v.Access != "PUBLIC") {
			continue
		}
		published, e := time.ParseInLocation("2006-01-02 15:04:05", v.Created, time.FixedZone("Asia/Shanghai", 8*3600))
		if e != nil {
			continue
		}
		seen[v.ID] = true
		out = append(out, Article{URL: "https://www.modb.pro/db/" + v.ID, Title: text(v.Title), Summary: html.EscapeString(v.Summary), Author: text(v.Author), Image: absolute(s.URL, v.Image), Published: published.UTC()})
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("Modb returned no valid public news")
	}
	sortArticles(out)
	return out, "", nil
}

func (f *Fetcher) enrichInfoQ(ctx context.Context, s Source, a Article) (Article, error) {
	u, err := url.Parse(a.URL)
	if err != nil || u.Host != "www.infoq.cn" || !strings.HasPrefix(u.Path, "/article/") {
		return a, fmt.Errorf("invalid InfoQ article URL")
	}
	id := strings.TrimPrefix(u.Path, "/article/")
	if !infoQSlug.MatchString(id) {
		return a, fmt.Errorf("invalid InfoQ article id")
	}
	body, err := f.chinaARequest(ctx, http.MethodPost, "https://www.infoq.cn/public/v1/article/getDetail", a.URL, map[string]string{"uuid": id})
	if err != nil {
		return a, err
	}
	var detail infoQArticle
	if err = decodeInfoQ(body, &detail); err != nil {
		return a, err
	}
	content := detail.Content
	if detail.ContentURL != "" {
		asset, e := url.Parse(detail.ContentURL)
		if e != nil || asset.Scheme != "https" || !(strings.HasSuffix(asset.Hostname(), ".geekbang.org") || strings.HasSuffix(asset.Hostname(), ".infoq.cn")) {
			return a, fmt.Errorf("unexpected InfoQ content host")
		}
		body, err = f.chinaARequest(ctx, http.MethodGet, detail.ContentURL, a.URL, nil)
		if err != nil {
			return a, err
		}
		content, err = infoQContent(body)
		if err != nil {
			return a, err
		}
	}
	content = normalizeHTML(content, a.URL)
	if len(text(bluemonday.StrictPolicy().Sanitize(content))) < 100 {
		return a, fmt.Errorf("InfoQ article body too short")
	}
	a.Content = content
	return a, nil
}

// InfoQ serves ProseMirror JSON rather than HTML for current articles. Retain
// text, block structure, links and images, then apply the same HTML sanitizer as
// other sources. Ignore presentation-only attributes from the publisher.
type infoQNode struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Attrs struct {
		Href  string `json:"href"`
		Src   string `json:"src"`
		Alt   string `json:"alt"`
		Level int    `json:"level"`
	} `json:"attrs"`
	Content []infoQNode `json:"content"`
}

func infoQContent(body []byte) (string, error) {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
		return string(body), nil
	}
	var htmlString string
	if json.Unmarshal(body, &htmlString) == nil {
		return htmlString, nil
	}
	var doc infoQNode
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("InfoQ content JSON: %w", err)
	}
	if doc.Type != "doc" {
		return "", fmt.Errorf("unsupported InfoQ content document")
	}
	var b strings.Builder
	nodes := 0
	var render func(infoQNode, int) error
	render = func(n infoQNode, depth int) error {
		nodes++
		if depth > 64 || nodes > 10000 || b.Len() > maxBody {
			return fmt.Errorf("InfoQ document exceeds structural limits")
		}
		if n.Type == "text" {
			b.WriteString(html.EscapeString(n.Text))
			return nil
		}
		if n.Type == "image" {
			b.WriteString(`<img src="` + html.EscapeString(n.Attrs.Src) + `" alt="` + html.EscapeString(n.Attrs.Alt) + `">`)
			return nil
		}
		if n.Type == "hard_break" || n.Type == "hardBreak" {
			b.WriteString("<br>")
			return nil
		}
		tag := ""
		switch n.Type {
		case "paragraph":
			tag = "p"
		case "heading":
			tag = "h" + strconv.Itoa(max(1, min(6, n.Attrs.Level)))
		case "blockquote":
			tag = "blockquote"
		case "bullet_list", "bulletList":
			tag = "ul"
		case "ordered_list", "orderedList":
			tag = "ol"
		case "list_item", "listItem":
			tag = "li"
		case "code_block", "codeBlock":
			tag = "pre"
		case "table":
			tag = "table"
		case "table_row", "tableRow":
			tag = "tr"
		case "table_cell", "tableCell":
			tag = "td"
		case "table_header", "tableHeader":
			tag = "th"
		case "link":
			b.WriteString(`<a href="` + html.EscapeString(n.Attrs.Href) + `">`)
		}
		if tag != "" {
			b.WriteString("<" + tag + ">")
		}
		for _, child := range n.Content {
			if err := render(child, depth+1); err != nil {
				return err
			}
		}
		if tag != "" {
			b.WriteString("</" + tag + ">")
		}
		if n.Type == "link" {
			b.WriteString("</a>")
		}
		return nil
	}
	if err := render(doc, 0); err != nil {
		return "", err
	}
	if b.Len() > maxBody {
		return "", fmt.Errorf("InfoQ article too large")
	}
	return b.String(), nil
}
