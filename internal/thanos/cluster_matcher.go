package thanos

import "strings"

// cluster_matcher.go — CLUSTER MATCHER ENJEKSİYONU (v0.10.128, K8s entity
// katmanı adım 2; docs/plans/entity-layer-design-2026-08-28.md §1.1).
//
// ── NEDEN ────────────────────────────────────────────────────────────────
//
// Keşif raporu engel #1: kod hiçbir cluster etiketi okumuyordu — satırlara
// cluster adı Go'da konfigden damgalanıyordu (`row.Cluster = c.Name`).
// "Cluster başına ayrı Thanos URL'i" modelinde doğru; TEK Thanos Querier'ın
// önünde N cluster varken her sorgu BÜTÜN cluster'ların serilerini
// döndürür ve pod/node/deployment tabloları cluster'ları karıştırır.
//
// ── NASIL ────────────────────────────────────────────────────────────────
//
// Şablon başına elle matcher eklemek 45+ şablonda 45 fırsat demekti (ve
// yeni şablon eklenirken unutulurdu). Bunun yerine İFADE düzeyinde
// enjeksiyon, doQuery'de: her vektör seçicisine `<label>="<value>"`
// eklenir — süslü parantezli seçicide `{`'dan hemen sonra, çıplak metrik
// adından sonra `{…}` olarak. Fonksiyon/agregat adları, by/without/on/
// ignoring/group_* etiket listeleri, anahtar kelimeler, dize sabitleri,
// süreler ve sayılar metrik SANILMAZ. Her şablon tablo-testli
// (cluster_matcher_test.go: enjeksiyon sonrası çıplak metrik 0, her
// seçicide matcher).
//
// Bağımlılık kararı: Prometheus'un promql/parser paketi bunu "doğru"
// yapardı ama ağır bir modül grafiği taşır; yeni bağımlılık gerekçe +
// onay ister. Bu tokenizer PromQL'in seçici gramerini kapsar, ötesini
// (subquery, @ modifier) kullanmıyoruz — kullanılırsa golden test söyler.
//
// Etiket adı BOŞ = enjeksiyon YOK = eski davranış (cluster başına URL).

// promqlKeywords — asla metrik adı olmayan tokenlar.
var promqlKeywords = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true, "offset": true, "bool": true,
	"and": true, "or": true, "unless": true, "at": true,
	// agregat operatörleri `sum by (…) (…)` biçiminde `(`'dan önce `by`
	// ile ayrılabilir; adları da metrik sayılmasın.
	"sum": true, "min": true, "max": true, "avg": true, "group": true,
	"stddev": true, "stdvar": true, "count": true, "count_values": true,
	"bottomk": true, "topk": true, "quantile": true, "limitk": true, "limit_ratio": true,
	// sayısal sabitler
	"inf": true, "nan": true,
}

// labelListKeywords — ardından gelen parantezli liste ETİKET listesidir.
var labelListKeywords = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true,
}

func isIdentStart(b byte) bool {
	return b == '_' || b == ':' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentByte(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// skipString — s[i] bir tırnak; kapanışın SONRAKİ indeksini döndürür.
func skipString(s string, i int) int {
	q := s[i]
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		}
		i++
	}
	return len(s)
}

// nextNonSpace — boşlukları atlayıp ilk karakterin indeksini verir.
func nextNonSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// escapeMatcherValue — etiket değeri PromQL dize sabiti olarak.
func escapeMatcherValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}

// withClusterMatcher — expr'deki her vektör seçicisine label="value" ekler.
// label boşsa expr aynen döner. Saf; golden + her-şablon testli.
func withClusterMatcher(expr, label, value string) string {
	if label == "" {
		return expr
	}
	m := label + `="` + escapeMatcherValue(value) + `"`
	var out strings.Builder
	out.Grow(len(expr) + 32)
	i := 0
	labelListDepth := 0 // by/on/… parantezinin içindeyiz
	for i < len(expr) {
		c := expr[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := skipString(expr, i)
			out.WriteString(expr[i:j])
			i = j
		case c == '{':
			// Süslü parantez seçicisi: kapanışa kadar kopyala, matcher'ı
			// başa koy. `{}` → `{m}`, `{a="b"}` → `{m,a="b"}`.
			j := strings.IndexByte(expr[i:], '}')
			if j < 0 {
				out.WriteString(expr[i:])
				return out.String()
			}
			// dize içindeki '}' için tırnakları sayarak gerçek kapanışı bul
			k := i + 1
			for k < len(expr) && expr[k] != '}' {
				if expr[k] == '"' || expr[k] == '\'' || expr[k] == '`' {
					k = skipString(expr, k)
					continue
				}
				k++
			}
			inner := strings.TrimSpace(expr[i+1 : k])
			out.WriteByte('{')
			out.WriteString(m)
			if inner != "" {
				out.WriteByte(',')
				out.WriteString(inner)
			}
			out.WriteByte('}')
			i = k + 1
		case c == '[':
			// süre penceresi [5m] / [1h:5m]
			j := strings.IndexByte(expr[i:], ']')
			if j < 0 {
				out.WriteString(expr[i:])
				return out.String()
			}
			out.WriteString(expr[i : i+j+1])
			i += j + 1
		case c == '(' && labelListDepth > 0:
			labelListDepth++
			out.WriteByte(c)
			i++
		case c == ')' && labelListDepth > 0:
			labelListDepth--
			out.WriteByte(c)
			i++
		case isIdentStart(c):
			j := i
			for j < len(expr) && isIdentByte(expr[j]) {
				j++
			}
			ident := expr[i:j]
			out.WriteString(ident)
			lower := strings.ToLower(ident)
			n := nextNonSpace(expr, j)
			switch {
			case labelListDepth > 0:
				// etiket listesi içindeki ad: metrik değil
			case labelListKeywords[lower]:
				if n < len(expr) && expr[n] == '(' {
					labelListDepth = 1
					out.WriteString(expr[j:n])
					out.WriteByte('(')
					i = n + 1
					continue
				}
			case promqlKeywords[lower]:
				// anahtar kelime / agregat adı
			case n < len(expr) && expr[n] == '(':
				// fonksiyon çağrısı
			case n < len(expr) && expr[n] == '{':
				// süslü parantez seçicisi — '{' dalı ekler
			default:
				// çıplak metrik adı
				out.WriteString("{" + m + "}")
			}
			i = j
		case c >= '0' && c <= '9':
			// sayı / süre sabiti: 5m, 1e3, 0.5
			j := i
			for j < len(expr) && (isIdentByte(expr[j]) || expr[j] == '.') {
				j++
			}
			out.WriteString(expr[i:j])
			i = j
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// bareSelectorCount — ifadede matcher'sız (süslü parantezsiz) metrik adı
// sayısı. Testin "enjeksiyon tam mı" kapısı; withClusterMatcher ile aynı
// tokenizer kurallarını kullanır.
func bareSelectorCount(expr string) int {
	marker := "\x00BARE\x00"
	rewritten := withClusterMatcher(expr, marker, "")
	return strings.Count(rewritten, "{"+marker+`=""}`)
}
