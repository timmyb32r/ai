package feed

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"
)

type rss struct {
	XMLName   xml.Name `xml:"rss"`
	Version   string   `xml:"version,attr"`
	ContentNS string   `xml:"xmlns:content,attr"`
	DCNS      string   `xml:"xmlns:dc,attr"`
	Channel   channel  `xml:"channel"`
}
type channel struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	TTL         int    `xml:"ttl"`
	Items       []item `xml:"item"`
}
type item struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	Date        string  `xml:"pubDate"`
	Description string  `xml:"description"`
	Content     string  `xml:"content:encoded"`
	Author      string  `xml:"dc:creator,omitempty"`
}
type rssGUID struct {
	Permanent string `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

func RSS(src Source, articles []Article, interval time.Duration) ([]byte, error) {
	r := rss{Version: "2.0", ContentNS: "http://purl.org/rss/1.0/modules/content/", DCNS: "http://purl.org/dc/elements/1.1/", Channel: channel{Title: src.Name, Link: src.URL, Description: src.Name, TTL: max(1, int(interval.Minutes()))}}
	for _, a := range articles {
		body := a.Content
		if body == "" {
			body = a.Summary
		}
		if a.Image != "" && !strings.Contains(body, a.Image) {
			body = `<p><img src="` + html.EscapeString(a.Image) + `" alt=""></p>` + body
		}
		body += `<p><a href="` + html.EscapeString(a.URL) + `">Original article</a></p>`
		r.Channel.Items = append(r.Channel.Items, item{Title: a.Title, Link: a.URL, GUID: rssGUID{"false", guid(src.ID, a.URL)}, Date: a.Published.Format(time.RFC1123Z), Description: a.Summary, Content: body, Author: a.Author})
	}
	b, e := xml.MarshalIndent(r, "", "  ")
	return append([]byte(xml.Header), b...), e
}

var statusTemplate = template.Must(template.New("status").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Personal Feed</title><style>body{font:16px system-ui;max-width:1200px;margin:40px auto;padding:0 20px}table{border-collapse:collapse;width:100%}td,th{padding:12px;text-align:left;border-bottom:1px solid #ddd;vertical-align:top}.error{color:#a21}small{display:block;overflow-wrap:anywhere}a{color:#246}</style><h1>Personal Feed</h1><p>Проверка каждые {{.Interval}}. <a href="/opml.xml">Импорт всех лент (OPML)</a> · <a href="/status.json">JSON</a></p><table><tr><th>Источник / RSS</th><th>Статей</th><th>Последняя успешная проверка (UTC)</th><th>Состояние</th></tr>{{range .Statuses}}<tr><td><a href="/{{.ID}}.xml">{{.Name}}</a><small><a href="{{.URL}}">Оригинал</a></small></td><td>{{.Count}}</td><td>{{if .LastSuccess}}{{.LastSuccess}}{{else}}Ещё не загружен{{end}}</td><td>{{if .Error}}<span class="error">{{.Error}}</span>{{else if .LastSuccess}}OK{{else}}Ожидает проверки{{end}}{{if .Warning}}<small>{{.Warning}}</small>{{end}}</td></tr>{{end}}</table></html>`))

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Store.db.PingContext(r.Context()); e != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /status.json", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Statuses(r.Context(), s.Config.Sources)
		if e != nil {
			http.Error(w, "database error", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Statuses(r.Context(), s.Config.Sources)
		if e != nil {
			http.Error(w, "database error", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		statusTemplate.Execute(w, struct {
			Interval string
			Statuses []Status
		}{s.Config.Interval.String(), v})
	})
	mux.HandleFunc("GET /opml.xml", func(w http.ResponseWriter, r *http.Request) {
		base := s.Config.PublicBaseURL
		if base == "" {
			base = "http://" + r.Host
		}
		type outline struct {
			Text    string `xml:"text,attr"`
			Title   string `xml:"title,attr"`
			Type    string `xml:"type,attr"`
			XMLURL  string `xml:"xmlUrl,attr"`
			HTMLURL string `xml:"htmlUrl,attr"`
		}
		v := struct {
			XMLName  xml.Name  `xml:"opml"`
			Version  string    `xml:"version,attr"`
			Title    string    `xml:"head>title"`
			Outlines []outline `xml:"body>outline"`
		}{Version: "2.0", Title: "Personal Feed"}
		for _, src := range s.Config.Sources {
			v.Outlines = append(v.Outlines, outline{src.Name, src.Name, "rss", base + "/" + src.ID + ".xml", src.URL})
		}
		b, e := xml.MarshalIndent(v, "", "  ")
		if e != nil {
			http.Error(w, "XML error", 500)
			return
		}
		w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
		w.Write([]byte(xml.Header))
		w.Write(b)
	})
	for _, src := range s.Config.Sources {
		mux.HandleFunc("GET /"+src.ID+".xml", func(w http.ResponseWriter, r *http.Request) {
			a, e := s.Store.Articles(r.Context(), src.ID)
			if e != nil {
				http.Error(w, "database error", 500)
				return
			}
			if len(a) == 0 {
				ready, e := s.Store.Initialized(r.Context(), src.ID)
				if e != nil {
					http.Error(w, "database error", 500)
					return
				}
				if !ready {
					w.Header().Set("Retry-After", "60")
					http.Error(w, "source has not been fetched successfully; see /status.json", 503)
					return
				}
			}
			b, e := RSS(src, a, s.Config.Interval)
			if e != nil {
				http.Error(w, "XML error", 500)
				return
			}
			etag := fmt.Sprintf(`"%x"`, sha256.Sum256(b))
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(304)
				return
			}
			w.Write(b)
		})
	}
	return mux
}
