package logstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// patterns.go — v0.10.296 (docs/audit/log-search.md Dilim 2, B3):
// /logs "Desenler" — pencere içindeki log mesajlarını NormalizeSignature
// imzasıyla gruplar.
//
// ÖRNEKLEME ZORUNLU (INCIDENTS "Drain templating … sample-based";
// templater/puller.go 1000/5 dk disiplini): pencere ne kadar büyük olursa
// olsun en çok PatternsSampleCap satır okunur — Search sayfaları (cursor)
// ile, en yeniden eskiye. Sayımlar bu örneğe GÖREdir; sonuç `Sampled` /
// `Total` / `Truncated` ile bunu ilan eder ("~%12" değil "2000 örnekte
// 240"). ES'te terms agg KULLANILAMAZ (imza indekste yok) — iki backend de
// aynı yoldan geçer, ayrı uygulama yok (bir backend'in uyguladığı, ötekinin
// yok saydığı metot bu repo'nun sessiz no-op sınıfıdır).
//
// İmza: logstore.NormalizeSignature — Problem zenginleştirmesiyle (influx
// enrich groupLogSignatures) AYNI fonksiyon; iki yüzey aynı "aynı log"
// tanımını gösterir. Drain (templater) ayrı uzaydır.

const (
	PatternsSampleCap = 2000
	patternsPageSize  = 500
	patternsMaxGroups = 500
	patternsTopSvc    = 3
)

// SignatureGroup — bir desen satırı.
type SignatureGroup struct {
	Hash         string   `json:"hash"`
	Template     string   `json:"template"`
	Count        int64    `json:"count"`
	Sample       string   `json:"sample"` // ilk görülen mesaj, VERBATİM
	Severity     uint8    `json:"severity"`
	SeverityText string   `json:"severityText,omitempty"`
	FirstSeen    int64    `json:"firstSeen"` // unix ns (örnek içinde)
	LastSeen     int64    `json:"lastSeen"`
	Services     []string `json:"services"`     // en çok görülen ≤3
	ServiceCount int      `json:"serviceCount"` // farklı servis sayısı
	// Query — v0.10.297: şablondan türetilen arama dizesi (PatternSearchQuery);
	// UI "Ara" ile bunu serbest metne koyar. Boş = aranabilir literal yok.
	Query string `json:"query"`
}

// PatternsResult — gruplar + örnekleme dürüstlüğü.
type PatternsResult struct {
	Groups    []SignatureGroup `json:"groups"`
	Sampled   int              `json:"sampled"`   // okunan satır
	Total     int              `json:"total"`     // backend'in pencere toplamı (ES ≤10k doygun)
	Cap       int              `json:"cap"`       // PatternsSampleCap
	Truncated bool             `json:"truncated"` // Sampled == Cap && Total > Cap
	Distinct  int              `json:"distinct"`  // limit'ten önceki grup sayısı
	// CoveredFromNs/CoveredToNs (v0.10.441, log arama denetimi C4) —
	// örneklemenin GERÇEKTEN kapsadığı alt pencere (en yeni uç; okunan
	// satırların min/max zaman damgası). Tavan dolduğunda istenen
	// pencereden dardır: yoğun serviste "son 1 saat" sanılan sayımlar
	// aslında son birkaç dakikayı anlatıyordu ve panel bunu söylemiyordu.
	// Kapıyı Sampled >= Cap ile kur, Truncated ile DEĞİL (Total güvenilmez
	// olabilir: ES track_total_hits kapalı; aşağıdaki clamp truncated=false
	// bırakır).
	CoveredFromNs int64 `json:"coveredFromNs,omitempty"`
	CoveredToNs   int64 `json:"coveredToNs,omitempty"`
}

// GroupBySignature — st.Search üstünden örnekleyerek gruplar. limit ≤ 0 →
// 50; tavan patternsMaxGroups. Filter'ın Limit/Offset/Cursor/WantCursor
// alanları burada YÖNETİLİR (çağıranınki ezilir).
func GroupBySignature(ctx context.Context, st Store, f Filter, limit int) (*PatternsResult, error) {
	if st == nil {
		return nil, fmt.Errorf("log store not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > patternsMaxGroups {
		limit = patternsMaxGroups
	}
	f.Offset, f.Cursor, f.Ascending = 0, "", false
	f.WantCursor = true
	type acc struct {
		g    SignatureGroup
		svcs map[string]int64
	}
	groups := map[string]*acc{}
	res := &PatternsResult{Cap: PatternsSampleCap}
	for res.Sampled < PatternsSampleCap {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := PatternsSampleCap - res.Sampled
		f.Limit = patternsPageSize
		if remaining < f.Limit {
			f.Limit = remaining
		}
		page, err := st.Search(ctx, f)
		if err != nil {
			return nil, err
		}
		if page == nil {
			break
		}
		if page.Total > res.Total {
			res.Total = page.Total
		}
		for _, rec := range page.Logs {
			if rec == nil {
				continue
			}
			res.Sampled++
			// v0.10.441 (C4) — kapsanan uç: Sampled'a eşlik eder (imzasız
			// satır da okunmuştur); ts=0 (bozuk doküman) 1970'e çekmesin.
			if ts := rec.Timestamp; ts > 0 {
				if res.CoveredFromNs == 0 || ts < res.CoveredFromNs {
					res.CoveredFromNs = ts
				}
				if ts > res.CoveredToNs {
					res.CoveredToNs = ts
				}
			}
			sig := NormalizeSignature(rec.Body)
			if sig == "" {
				continue
			}
			a, ok := groups[sig]
			if !ok {
				a = &acc{g: SignatureGroup{
					Hash: fmt.Sprintf("%016x", SignatureHash(sig)), Template: sig,
					Sample: rec.Body, Severity: rec.Severity, SeverityText: rec.SeverityText,
					FirstSeen: rec.Timestamp, LastSeen: rec.Timestamp,
				}, svcs: map[string]int64{}}
				groups[sig] = a
			}
			a.g.Count++
			if rec.Timestamp < a.g.FirstSeen {
				a.g.FirstSeen = rec.Timestamp
			}
			if rec.Timestamp > a.g.LastSeen {
				a.g.LastSeen = rec.Timestamp
			}
			if rec.Severity > a.g.Severity {
				a.g.Severity, a.g.SeverityText = rec.Severity, rec.SeverityText
			}
			if rec.ServiceName != "" {
				a.svcs[rec.ServiceName]++
			}
		}
		if len(page.Logs) == 0 || page.NextCursor == "" || len(page.Logs) < f.Limit {
			break
		}
		f.Cursor = page.NextCursor
	}
	res.Truncated = res.Sampled >= PatternsSampleCap && res.Total > res.Sampled
	if res.Total < res.Sampled {
		res.Total = res.Sampled
	}
	res.Distinct = len(groups)
	out := make([]SignatureGroup, 0, len(groups))
	for _, a := range groups {
		a.g.ServiceCount = len(a.svcs)
		a.g.Query = PatternSearchQuery(a.g.Template)
		type sc struct {
			n string
			c int64
		}
		svc := make([]sc, 0, len(a.svcs))
		for n, c := range a.svcs {
			svc = append(svc, sc{n, c})
		}
		sort.Slice(svc, func(i, j int) bool {
			if svc[i].c != svc[j].c {
				return svc[i].c > svc[j].c
			}
			return svc[i].n < svc[j].n
		})
		for i := 0; i < len(svc) && i < patternsTopSvc; i++ {
			a.g.Services = append(a.g.Services, svc[i].n)
		}
		if a.g.Services == nil {
			a.g.Services = []string{}
		}
		out = append(out, a.g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Template < out[j].Template
	})
	if len(out) > limit {
		out = out[:limit]
	}
	res.Groups = out
	return res, nil
}

// PatternSearchQuery — bir şablondan, iki backend'in de anladığı arama
// dizesi: yer tutucular atılır, ≥3 karakterlik literal parçalar tırnaklanıp
// AND'lenir (logql / query_string). Yaklaşıktır ve öyle etiketlenir —
// "connection refused to <x>:<x>" → "connection refused to". Boş → "".
func PatternSearchQuery(template string) string {
	// v0.10.310 — Drain şablonları (log_templates) yer tutucuyu "<*>" yazar;
	// tek türetici kalsın diye burada normalize edilir.
	template = strings.ReplaceAll(template, "<*>", "<x>")
	parts := strings.Split(template, "<x>")
	var terms []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, ":;,.-=()[]{}'\"")
		p = strings.TrimSpace(p)
		if len(p) < 3 {
			continue
		}
		terms = append(terms, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p)+`"`)
		if len(terms) >= 6 {
			break
		}
	}
	return strings.Join(terms, " AND ")
}

// patternsBudget — örnekleme turu tavanı (sayfa başına Search kendi
// bütçesini de taşır).
const patternsBudget = 10 * time.Second
