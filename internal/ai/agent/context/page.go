// Package agentctx — v0.10.539 (CoSRE v2 Faz 3.2): SAYFA BAĞLAMI protokolü,
// sunucu yarısı. İstemci her turda pageContext(pathname, search) çıktısını
// `context.page` olarak gönderir (frontend/src/lib/pageContext.ts); burada
// sınırlanır (Sanitize) ve modele deterministik Türkçe önsöz olarak yazılır
// (PreambleTR). Boş alan HİÇ yazılmaz — "(bilinmiyor)" modele doldurulacak
// boşluk sunar (chat_screen_context.go dersi).
//
// Eski ekran bağlamı (servis/operation/env/aralık) chat_screen_context.go'da
// kalır ve guided/drawer kademelerini beslemeye devam eder; bu önsöz yalnız
// YENİ boyutları (sayfa, cluster, namespace, workload, pod, span, problem,
// exception, arama, filtreler) ekler. Sabitlenmiş bağlam (pin, Faz 3.2b)
// ayrı ve ÖNCE yazılır: operatör başka sayfaya geçse de özne kaybolmaz.
package agentctx

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

type PageFilter struct {
	K  string   `json:"k"`
	Op string   `json:"op"`
	V  []string `json:"v"`
}

type PageRange struct {
	Preset string `json:"preset"`
	FromMs int64  `json:"fromMs,omitempty"`
	ToMs   int64  `json:"toMs,omitempty"`
}

// PageContext — frontend PageContext ile alan-alan aynı (lib/types.ts).
type PageContext struct {
	Page        string       `json:"page"`
	Path        string       `json:"path,omitempty"`
	Env         string       `json:"env,omitempty"`
	Cluster     string       `json:"cluster,omitempty"`
	Namespace   string       `json:"namespace,omitempty"`
	Service     string       `json:"service,omitempty"`
	Workload    string       `json:"workload,omitempty"`
	Pod         string       `json:"pod,omitempty"`
	Operation   string       `json:"operation,omitempty"`
	TraceID     string       `json:"traceId,omitempty"`
	SpanID      string       `json:"spanId,omitempty"`
	ProblemID   string       `json:"problemId,omitempty"`
	ExceptionID string       `json:"exceptionId,omitempty"`
	Search      string       `json:"search,omitempty"`
	TimeRange   *PageRange   `json:"timeRange,omitempty"`
	Filters     []PageFilter `json:"activeFilters,omitempty"`
}

const (
	maxValueRunes = 200
	maxFilters    = 20
	maxFilterVals = 10
)

// clean — kırpar, kontrol karakterlerini düşürür, rune tavanı uygular.
// URL'den gelen değer operatörün kendi ekranıdır ama sınır yine de var:
// önsöz modele veri olarak gider, satır sonu/kontrol karakteri prompt
// yapısını bozamamalı.
func clean(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if n >= maxValueRunes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Sanitize — nil güvenli; kopyayı döner (istek gövdesi değişmez).
func Sanitize(p *PageContext) *PageContext {
	if p == nil {
		return nil
	}
	c := *p
	c.Page, c.Path, c.Env, c.Cluster, c.Namespace = clean(c.Page), clean(c.Path), clean(c.Env), clean(c.Cluster), clean(c.Namespace)
	c.Service, c.Workload, c.Pod, c.Operation = clean(c.Service), clean(c.Workload), clean(c.Pod), clean(c.Operation)
	c.TraceID, c.SpanID, c.ProblemID, c.ExceptionID, c.Search = clean(c.TraceID), clean(c.SpanID), clean(c.ProblemID), clean(c.ExceptionID), clean(c.Search)
	if c.TimeRange != nil {
		r := *c.TimeRange
		r.Preset = clean(r.Preset)
		c.TimeRange = &r
	}
	if len(c.Filters) > 0 {
		out := make([]PageFilter, 0, len(c.Filters))
		for i, f := range c.Filters {
			if i >= maxFilters {
				break
			}
			nf := PageFilter{K: clean(f.K), Op: clean(f.Op)}
			for j, v := range f.V {
				if j >= maxFilterVals {
					break
				}
				nf.V = append(nf.V, clean(v))
			}
			if nf.K != "" {
				out = append(out, nf)
			}
		}
		c.Filters = out
	}
	if c.Page == "" {
		return nil
	}
	return &c
}

// Empty — sayfa adı dışında hiçbir boyut yok.
func (p *PageContext) Empty() bool {
	if p == nil {
		return true
	}
	return p.Cluster == "" && p.Namespace == "" && p.Service == "" && p.Workload == "" && p.Pod == "" &&
		p.Operation == "" && p.TraceID == "" && p.SpanID == "" && p.ProblemID == "" && p.ExceptionID == "" &&
		p.Search == "" && p.TimeRange == nil && len(p.Filters) == 0
}

func rangeTR(r *PageRange) string {
	if r == nil {
		return ""
	}
	if r.Preset == "custom" && r.FromMs > 0 && r.ToMs > 0 {
		return fmt.Sprintf("%s → %s UTC", time.UnixMilli(r.FromMs).UTC().Format("2006-01-02 15:04"), time.UnixMilli(r.ToMs).UTC().Format("2006-01-02 15:04"))
	}
	return r.Preset
}

func filtersTR(fs []PageFilter) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, strings.TrimSpace(f.K+" "+f.Op+" "+strings.Join(f.V, ",")))
	}
	return strings.Join(parts, "; ")
}

// lines — bir bağlamın satırları. full=false: servis/operation/env/aralık
// yazılmaz (eski ekran bağlamı önsözü zaten yazıyor); full=true (pin) hepsi.
func lines(b *strings.Builder, p *PageContext, full bool) {
	fmt.Fprintf(b, "- sayfa: %s", p.Page)
	if p.Path != "" {
		fmt.Fprintf(b, " (%s)", p.Path)
	}
	b.WriteString("\n")
	w := func(label, v string) {
		if v != "" {
			fmt.Fprintf(b, "- %s: %s\n", label, v)
		}
	}
	w("cluster", p.Cluster)
	w("namespace", p.Namespace)
	if full {
		w("servis", p.Service)
		w("operation", p.Operation)
		w("ortam", p.Env)
		w("zaman aralığı", rangeTR(p.TimeRange))
	}
	w("workload", p.Workload)
	w("pod", p.Pod)
	w("trace", p.TraceID)
	w("span", p.SpanID)
	w("problem", p.ProblemID)
	w("exception", p.ExceptionID)
	w("arama", p.Search)
	w("filtreler", filtersTR(p.Filters))
}

// PreambleTR — pin (varsa) ÖNCE, açık sayfa sonra. İkisi de boşsa "".
func PreambleTR(cur, pinned *PageContext) string {
	var b strings.Builder
	if pinned != nil && (pinned.Page != "" || !pinned.Empty()) {
		b.WriteString("SABİTLENMİŞ BAĞLAM — operatör bu özneyi sohbete sabitledi; sayfa değişse de sorular bununla ilgilidir:\n")
		lines(&b, pinned, true)
		b.WriteString("\n")
	}
	if cur != nil && cur.Page != "" && !cur.Empty() {
		b.WriteString("AÇIK SAYFA — operatör şu an burada (servis/aralık yukarıdaki ekran bağlamında):\n")
		lines(&b, cur, false)
		b.WriteString("Soru AKSİNİ SÖYLEMEDİKÇE bu boyutları (cluster/namespace/problem/filtreler) tool argümanlarında kullan.\n\n")
	}
	return b.String()
}
