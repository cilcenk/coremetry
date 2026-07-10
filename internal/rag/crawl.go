package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// crawl.go — v0.8.442 wiki/URL kaynağı. Operatör bir adres verir
// ("link versem oradan bilgileri arayıp çekse"); crawler aynı host +
// path-prefix içinde kalarak sayfaları sınırlı BFS ile toplar, ana
// metni çıkarır. API türü bilmez — Confluence/MediaWiki/GitLab/statik
// site fark etmez. Sınırlar sert: sayfa/derinlik/hız/boyut tavanları
// aşılamaz; robots yok (iç ağ), nezaket hız limitiyle sağlanır.

const (
	crawlMaxPages    = 200
	crawlMaxDepth    = 3
	crawlPageMax     = 2 << 20 // 2MB/sayfa
	crawlRateEvery   = 500 * time.Millisecond
	crawlFetchTO     = 15 * time.Second
	crawlMinTextLen  = 80 // bundan kısa sayfalar (yönlendirme/menü) atlanır
)

// CrawledPage — bir sayfanın çıkarılmış hali.
type CrawledPage struct {
	URL   string
	Title string
	Text  string
	Hash  string // sha256(Text) hex — diff senkronun anahtarı
}

// CrawlSource — Settings'ten gelen tek kaynak tanımı.
type CrawlSource struct {
	URL        string `json:"url"`
	AuthHeader string `json:"authHeader,omitempty"` // "Header: value" — asla geri echo edilmez
}

// Crawl — kaynak URL'den başlayıp aynı host+prefix altında BFS.
// ctx iptali anında durur; hata tek sayfayı atlatır, taramayı öldürmez.
func Crawl(ctx context.Context, httpc *http.Client, src CrawlSource) ([]CrawledPage, error) {
	root, err := url.Parse(strings.TrimSpace(src.URL))
	if err != nil || root.Host == "" {
		return nil, fmt.Errorf("geçersiz kaynak URL: %q", src.URL)
	}
	prefix := root.Path
	if prefix == "" {
		prefix = "/"
	}

	type item struct {
		u     *url.URL
		depth int
	}
	queue := []item{{root, 0}}
	seen := map[string]bool{canonURL(root): true}
	var out []CrawledPage
	tick := time.NewTicker(crawlRateEvery)
	defer tick.Stop()

	for len(queue) > 0 && len(out) < crawlMaxPages {
		it := queue[0]
		queue = queue[1:]
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-tick.C: // nezaket hızı
		}
		body, err := fetchPage(ctx, httpc, it.u, src.AuthHeader)
		if err != nil {
			continue // tek sayfa hatası taramayı durdurmaz
		}
		title, text, links := extractHTML(body, it.u)
		if len([]rune(text)) >= crawlMinTextLen {
			sum := sha256.Sum256([]byte(text))
			out = append(out, CrawledPage{
				URL: canonURL(it.u), Title: firstNonEmptyStr(title, canonURL(it.u)),
				Text: text, Hash: hex.EncodeToString(sum[:]),
			})
		}
		if it.depth >= crawlMaxDepth {
			continue
		}
		for _, l := range links {
			if l.Host != root.Host || !strings.HasPrefix(l.Path, prefix) {
				continue
			}
			key := canonURL(l)
			if seen[key] {
				continue
			}
			seen[key] = true
			queue = append(queue, item{l, it.depth + 1})
		}
	}
	return out, nil
}

func fetchPage(ctx context.Context, httpc *http.Client, u *url.URL, authHeader string) (string, error) {
	fctx, cancel := context.WithTimeout(ctx, crawlFetchTO)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "coremetry-rag-crawler/1.0")
	if h := strings.TrimSpace(authHeader); h != "" {
		if k, v, ok := strings.Cut(h, ":"); ok {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "text/plain") {
		return "", fmt.Errorf("içerik türü atlandı: %s", ct)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, crawlPageMax))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// extractHTML — ana metin + başlık + linkler. script/style/nav/header/
// footer/aside altındaki HER ŞEY atılır (menü gürültüsü chunk'ları
// zehirlemesin); metin blok elemanlarında satır kırmalı toplanır.
func extractHTML(src string, base *url.URL) (title, text string, links []*url.URL) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", "", nil
	}
	skip := map[string]bool{"script": true, "style": true, "nav": true,
		"header": true, "footer": true, "aside": true, "noscript": true}
	blocky := map[string]bool{"p": true, "div": true, "li": true, "br": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "tr": true, "pre": true,
		"section": true, "article": true, "td": true}
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skip[n.Data] {
				return
			}
			if n.Data == "title" && title == "" && n.FirstChild != nil {
				title = strings.TrimSpace(n.FirstChild.Data)
			}
			if n.Data == "a" {
				for _, a := range n.Attr {
					if a.Key == "href" {
						if l, err := base.Parse(a.Val); err == nil && (l.Scheme == "http" || l.Scheme == "https") {
							l.Fragment = ""
							links = append(links, l)
						}
					}
				}
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blocky[n.Data] {
			sb.WriteString("\n\n")
		}
	}
	walk(doc)
	// çoklu boş satırları sadeleştir
	lines := strings.Split(sb.String(), "\n\n")
	var clean []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	return title, strings.Join(clean, "\n\n"), links
}

func canonURL(u *url.URL) string {
	c := *u
	c.Fragment = ""
	return strings.TrimRight(c.String(), "/")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
