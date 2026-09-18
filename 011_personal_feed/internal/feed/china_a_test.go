package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type chinaATransport func(*http.Request) (*http.Response, error)

func (t chinaATransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }
func chinaAResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestInfoQPublicListingAndContent(t *testing.T) {
	requests := 0
	f := &Fetcher{Client: &http.Client{Transport: chinaATransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected authentication cookie")
		}
		if r.Header.Get("Referer") == "" {
			t.Fatal("missing normal page Referer")
		}
		switch r.URL.Path {
		case "/public/v1/article/getList":
			if r.Method != "POST" {
				t.Fatal(r.Method)
			}
			var p map[string]int
			if e := json.NewDecoder(r.Body).Decode(&p); e != nil {
				t.Fatal(e)
			}
			if p["id"] != 15 || p["type"] != 1 || p["size"] < 20 {
				t.Fatal(p)
			}
			return chinaAResponse(200, `{"code":0,"data":[{"uuid":"older","article_title":"旧文章","publish_time":1789021200000},{"uuid":"newer","article_title":"新文章","article_summary":"<unsafe>","publish_time":1789108800096},{"uuid":"../escape","article_title":"bad","publish_time":1},{"uuid":"newer","article_title":"duplicate","publish_time":1789108800096}]}`), nil
		case "/public/v1/article/getDetail":
			return chinaAResponse(200, `{"code":0,"data":{"content_url":"https://static-acl-001.geekbang.org/resource/article/test/content.json"}}`), nil
		case "/resource/article/test/content.json":
			return chinaAResponse(200, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"`+strings.Repeat("中文正文", 40)+`"}]},{"type":"paragraph","content":[{"type":"link","attrs":{"href":"javascript:alert(1)"},"content":[{"type":"text","text":"unsafe link"}]}]}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, fmt.Errorf("unexpected request")
		}
	})}}
	s := Source{URL: "https://www.infoq.cn/topic/bigdata"}
	a, w, e := f.infoqBigData(context.Background(), s)
	if e != nil || w != "" || len(a) != 2 || a[0].URL != "https://www.infoq.cn/article/newer" || a[0].Published.UnixMilli() != 1789108800096 || a[0].Summary != "&lt;unsafe&gt;" {
		t.Fatalf("%+v %s %v", a, w, e)
	}
	v, e := f.enrichInfoQ(context.Background(), s, a[0])
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(v.Content, "中文正文") || strings.Contains(v.Content, "javascript:") || requests != 3 {
		t.Fatalf("requests=%d content=%s", requests, v.Content)
	}
}

func TestModbPublicNewsKeepsLargeIDsAndLocalDate(t *testing.T) {
	f := &Fetcher{Client: &http.Client{Transport: chinaATransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/knowledges/find/v2" || r.URL.Query().Get("type") != "3" || r.URL.Query().Get("pageSize") != "30" || r.Header.Get("Referer") != "https://www.modb.pro/dbNews" {
			t.Fatal(r)
		}
		return chinaAResponse(200, `{"list":[{"id":"2100803773564280832","title":"新闻","brief":"摘要","createdTime":"2026-09-18 12:29:38","encryptLevel":"PUBLIC"},{"id":"999","title":"private","createdTime":"2026-09-18 13:00:00","encryptLevel":"PRIVATE"},{"id":"../bad","title":"invalid","createdTime":"2026-09-18 12:00:00"},{"id":"888","title":"undated","createdTime":"bad"}]}`), nil
	})}}
	a, _, e := f.modbNews(context.Background(), Source{URL: "https://www.modb.pro/dbNews"})
	if e != nil || len(a) != 1 || a[0].URL != "https://www.modb.pro/db/2100803773564280832" || a[0].Published.Format(time.RFC3339) != "2026-09-18T04:29:38Z" {
		t.Fatalf("%+v %v", a, e)
	}
}

func TestChinaAPublicFailuresAreErrors(t *testing.T) {
	for _, body := range []string{`{"code":-2000,"data":[]}`, `{"data":[]}`, `{"code":0,"data":null}`, `{"code":0,"data":[]}`, `<html>Login</html>`} {
		f := &Fetcher{Client: &http.Client{Transport: chinaATransport(func(*http.Request) (*http.Response, error) { return chinaAResponse(200, body), nil })}}
		if _, _, e := f.infoqBigData(context.Background(), Source{URL: "https://www.infoq.cn/topic/bigdata"}); e == nil {
			t.Fatalf("accepted failed response %s", body)
		}
	}
	f := &Fetcher{Client: &http.Client{Transport: chinaATransport(func(*http.Request) (*http.Response, error) { return chinaAResponse(403, `blocked`), nil })}}
	if _, _, e := f.modbNews(context.Background(), Source{URL: "https://www.modb.pro/dbNews"}); e == nil {
		t.Fatal("accepted HTTP403")
	}
	if _, e := f.enrichInfoQ(context.Background(), Source{}, Article{URL: "https://other.test/article/test"}); e == nil {
		t.Fatal("accepted foreign article URL")
	}
}

func TestInfoQDocumentEscapingStructureAndBounds(t *testing.T) {
	input := `{"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"<Title>"}]},{"type":"bullet_list","content":[{"type":"list_item","content":[{"type":"paragraph","content":[{"type":"link","attrs":{"href":"https://example.com/?x=1&y=2"},"content":[{"type":"text","text":"link"}]}]}]}]},{"type":"image","attrs":{"src":"https://example.com/a.png","alt":"a\"b"}}]}`
	got, e := infoQContent([]byte(input))
	if e != nil {
		t.Fatal(e)
	}
	for _, part := range []string{"<h2>&lt;Title&gt;</h2>", "<ul><li><p>", "x=1&amp;y=2", "alt=\"a&#34;b\""} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %s: %s", part, got)
		}
	}
	sanitized := normalizeHTML(got, "https://www.infoq.cn/article/test")
	for _, part := range []string{`href="https://example.com/?x=1&amp;y=2"`, `src="https://example.com/a.png"`} {
		if !strings.Contains(sanitized, part) {
			t.Fatalf("sanitizer lost %s: %s", part, sanitized)
		}
	}
	node := `{"type":"text","text":"deep"}`
	for i := 0; i < 70; i++ {
		node = `{"type":"paragraph","content":[` + node + `]}`
	}
	if _, e = infoQContent([]byte(`{"type":"doc","content":[` + node + `]}`)); e == nil {
		t.Fatal("unbounded nested document")
	}
	if _, e = infoQContent([]byte(`{"unexpected":"schema"}`)); e == nil {
		t.Fatal("accepted unknown schema")
	}
}
