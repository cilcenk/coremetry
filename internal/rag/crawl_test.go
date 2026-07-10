package rag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crawl_test.go — v0.8.442. httptest sunucusuyla uçtan uca: prefix
// hapsi, derinlik, menü gürültüsünün atılması, kısa sayfa eleme,
// auth header iletimi, hash determinizmi.
func TestCrawl(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("X-Auth")
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	longText := strings.Repeat("wiki içeriği burada anlatılır. ", 10)
	mux.HandleFunc("/wiki/", page(`<html><head><title>Ana</title></head><body>
		<nav><a href="/wiki/menu">menü çöpü</a> menü menü</nav>
		<p>`+longText+`</p>
		<a href="/wiki/derin">derin</a>
		<a href="/baska/yer">prefix dışı</a>
		<a href="https://evil.example/x">host dışı</a></body></html>`))
	mux.HandleFunc("/wiki/derin", page(`<html><body><p>`+longText+`derin sayfa</p></body></html>`))
	mux.HandleFunc("/wiki/menu", page(`<html><body><p>kısa</p></body></html>`)) // < min → elenir
	mux.HandleFunc("/baska/yer", page(`<html><body><p>`+longText+`</p></body></html>`))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pages, err := Crawl(context.Background(), srv.Client(),
		CrawlSource{URL: srv.URL + "/wiki/", AuthHeader: "X-Auth: gizli"})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	urls := map[string]CrawledPage{}
	for _, p := range pages {
		urls[p.URL] = p
	}
	if len(pages) != 2 {
		t.Fatalf("2 sayfa beklenirdi (ana+derin), %d geldi: %v", len(pages), urlsOf(pages))
	}
	for u := range urls {
		if strings.Contains(u, "/baska/") || strings.Contains(u, "evil") {
			t.Fatalf("prefix/host hapsi delindi: %s", u)
		}
	}
	if gotAuth != "gizli" {
		t.Fatalf("auth header iletilmedi: %q", gotAuth)
	}
	root := urls[strings.TrimRight(srv.URL+"/wiki/", "/")]
	if strings.Contains(root.Text, "menü çöpü") {
		t.Fatal("nav içeriği metne sızdı")
	}
	if root.Title != "Ana" {
		t.Fatalf("title: %q", root.Title)
	}
	if root.Hash == "" || len(root.Hash) != 64 {
		t.Fatalf("hash: %q", root.Hash)
	}
	// determinizm: aynı içerik → aynı hash
	pages2, _ := Crawl(context.Background(), srv.Client(), CrawlSource{URL: srv.URL + "/wiki/"})
	for _, p2 := range pages2 {
		if p2.URL == root.URL && p2.Hash != root.Hash {
			t.Fatal("hash deterministik değil")
		}
	}
}

func urlsOf(ps []CrawledPage) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.URL
	}
	return out
}
