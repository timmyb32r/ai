package feed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Article struct {
	URL       string
	Title     string
	Summary   string
	Content   string
	Author    string
	Image     string
	Published time.Time
}
type Status struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	LastAttempt string `json:"last_attempt"`
	LastSuccess string `json:"last_success"`
	Error       string `json:"error"`
	Warning     string `json:"warning"`
	Count       int    `json:"count"`
}
type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY, last_attempt TEXT NOT NULL DEFAULT '',last_success TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',warning TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS seen(source TEXT NOT NULL,url TEXT NOT NULL,PRIMARY KEY(source,url));
 CREATE TABLE IF NOT EXISTS articles(seq INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL,url TEXT NOT NULL,title TEXT NOT NULL,summary TEXT NOT NULL,content TEXT NOT NULL,author TEXT NOT NULL,image TEXT NOT NULL,published TEXT NOT NULL,UNIQUE(source,url));`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Seen(ctx context.Context, id, u string) (bool, error) {
	var n int
	e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM seen WHERE source=? AND url=?", id, u).Scan(&n)
	return n > 0, e
}
func (s *Store) Initialized(ctx context.Context, id string) (bool, error) {
	var v string
	e := s.db.QueryRowContext(ctx, "SELECT last_success FROM sources WHERE id=?", id).Scan(&v)
	if e == sql.ErrNoRows {
		return false, nil
	}
	return v != "", e
}
func (s *Store) Save(ctx context.Context, id string, articles []Article, limit int, warning string, baseline []string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Insert oldest first so discovery order is stable when publication dates are absent.
	for i := len(articles) - 1; i >= 0; i-- {
		a := articles[i]
		r, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO seen(source,url) VALUES(?,?)", id, a.URL)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			continue
		}
		if a.Published.IsZero() {
			a.Published = time.Now().UTC()
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO articles(source,url,title,summary,content,author,image,published) VALUES(?,?,?,?,?,?,?,?)`, id, a.URL, a.Title, a.Summary, a.Content, a.Author, a.Image, a.Published.UTC().Format("2006-01-02T15:04:05.000000000Z"))
		if e != nil {
			return e
		}
	}
	for _, u := range baseline {
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO seen(source,url) VALUES(?,?)", id, u); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `DELETE FROM articles WHERE source=? AND seq NOT IN (SELECT seq FROM articles WHERE source=? ORDER BY published DESC,seq DESC LIMIT ?)`, id, id, limit)
	if e != nil {
		return e
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, e = tx.ExecContext(ctx, `INSERT INTO sources(id,last_attempt,last_success,error,warning) VALUES(?,?,?,'',?) ON CONFLICT(id) DO UPDATE SET last_attempt=excluded.last_attempt,last_success=excluded.last_success,error='',warning=excluded.warning`, id, now, now, warning)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Failure(ctx context.Context, id string, err error) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO sources(id,last_attempt,error) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET last_attempt=excluded.last_attempt,error=excluded.error`, id, time.Now().UTC().Format(time.RFC3339), err.Error())
	return e
}
func (s *Store) Articles(ctx context.Context, id string) ([]Article, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT url,title,summary,content,author,image,published FROM articles WHERE source=? ORDER BY published DESC,seq DESC`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Article{}
	for rows.Next() {
		var a Article
		var date string
		if e = rows.Scan(&a.URL, &a.Title, &a.Summary, &a.Content, &a.Author, &a.Image, &date); e != nil {
			return nil, e
		}
		a.Published, _ = time.Parse(time.RFC3339Nano, date)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Statuses(ctx context.Context, sources []Source) ([]Status, error) {
	out := make([]Status, 0, len(sources))
	for _, src := range sources {
		v := Status{ID: src.ID, Name: src.Name, URL: src.URL}
		e := s.db.QueryRowContext(ctx, `SELECT last_attempt,last_success,error,warning FROM sources WHERE id=?`, src.ID).Scan(&v.LastAttempt, &v.LastSuccess, &v.Error, &v.Warning)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM articles WHERE source=?", src.ID).Scan(&v.Count); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func guid(id, u string) string { return fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(id+"\n"+u))) }
