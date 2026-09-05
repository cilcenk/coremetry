package api

// log_field_search.go — v0.10.433 (CoSRE router boşlukları D5): alan
// süzgeçli log araması. Prod /ai "Router boşlukları": `url.full field'ında
// "/x/y" geçen loglar` sorusu log kökü taşıdığı için kılavuz kapısından
// geçiyor ama hata kökü olmadığından log_errors'a oturmuyor, none'a düşüp
// serbest döngüye savruluyordu. Şimdi deterministik: mesajdan ALAN + DEĞER
// + eşleşme türü (geçen/eşit) çıkar, logstore.Search'e KQL dizesi olarak
// ver (Filter'da alan yüklemi yok — tek kanal Search; her iki backend de
// `field:value` anlar, v0.10.279), kanıtı anlat, aynı sorguyu /logs linkine
// taşı.
//
// Dürüstlük: ES `allow_leading_wildcard=false` — "geçen" (içeren) araması
// ES'te ifade edilemez; tam ifade eşleşmesi olarak koşar ve kanıt bunu
// SÖYLER (sessiz genişleme/daralma yok — v0.8.398 sınıfı). CH'de tırnaksız
// `*değer*` LIKE'a derlenir (compile_ch: tırnak jokeri öldürür).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

const guidedLogFieldSample = 20

var (
	// logFieldQuotedRe — tırnaklı değer: "…", '…', “…”, ‘…’, `…`.
	logFieldQuotedRe = regexp.MustCompile("[\"“”'‘’`]([^\"“”'‘’`]{1,256})[\"“”'‘’`]")
	// logFieldWordRe — `<alan> alanında|field'ında|attribute|attr|anahtarında`.
	logFieldWordRe = regexp.MustCompile(`(?i)(?:^|[\s(,;])([a-z_][a-z0-9_.-]*[a-z0-9_])\s+(?:alan|field|attribute|attr|anahtar)`)
	// logFieldKQLRe — açık `alan:"değer"` / `alan:değer` yazımı.
	logFieldKQLRe = regexp.MustCompile(`(?:^|\s)([a-zA-Z_][\w.-]*):(?:"([^"]{1,256})"|([^\s"]{1,256}))`)
	// logFieldSuffixApostropheRe — iki harf arasındaki apostrof (Türkçe ek).
	logFieldSuffixApostropheRe = regexp.MustCompile(`(\pL)['‘’](\pL)`)
	// logFieldNameRe — sınıflandırıcıdan gelen alan adı (uydurma/enjeksiyon kapısı).
	logFieldNameRe = regexp.MustCompile(`^[A-Za-z_][\w.-]{0,63}$`)
	// logFieldBareValueRe — CH'de tırnaksız joker olarak güvenli değer:
	// boşluk, tırnak, joker, iki nokta yok.
	logFieldBareValueRe = regexp.MustCompile(`^[A-Za-z0-9_./=&%+@-]+$`)
)

// logFieldKnownBare — nokta/alt çizgi taşımayan ama alan olduğu bilinen
// adlar; "hata alanında" gibi Türkçe sözcükler alan sayılmasın.
var logFieldKnownBare = map[string]bool{
	"message": true, "body": true, "msg": true, "host": true, "pod": true, "url": true, "method": true,
	"path": true, "route": true, "status": true, "level": true, "severity": true, "service": true,
	"trace": true, "span": true, "container": true, "namespace": true, "cluster": true, "env": true,
	"logger": true, "thread": true, "exception": true, "user": true, "client": true, "request": true,
}

func logFieldNameOK(f string) bool {
	if !logFieldNameRe.MatchString(f) {
		return false
	}
	return strings.ContainsAny(f, "._-") || logFieldKnownBare[strings.ToLower(f)]
}

// logFieldContainsCue — "geçen/içeren/contains" → içeren; "eşit/olan/tam/
// equals/exact" → tam eşleşme; ikisi de yoksa içeren (operatörün doğal
// şekli "geçen loglar").
func logFieldContainsCue(toks []string) bool {
	contains, exact := false, false
	for _, t := range toks {
		switch {
		case strings.HasPrefix(t, "geç") || strings.HasPrefix(t, "gec") || strings.HasPrefix(t, "içer") || strings.HasPrefix(t, "icer") ||
			strings.HasPrefix(t, "contain") || t == "like" || strings.HasPrefix(t, "benze"):
			contains = true
		case strings.HasPrefix(t, "eşit") || strings.HasPrefix(t, "esit") || t == "olan" || strings.HasPrefix(t, "equal") ||
			strings.HasPrefix(t, "exact") || t == "tam" || t == "aynen":
			exact = true
		}
	}
	if exact && !contains {
		return false
	}
	return true
}

// extractLogFieldQuery — SAF: ham mesajdan (alan, değer, içeren?) çıkarır.
// Log kökü ŞART (trace araması D2'nin işi). Sıra: açık KQL yazımı, sonra
// "<alan> alanında <tırnaklı değer>". Alan adı ASCII ve nokta/alt çizgi/
// tire taşıyan ya da bilinen çıplak ad; Türkçe sözcükler alan olamaz.
func extractLogFieldQuery(raw string, toks []string) (field, value string, contains, ok bool) {
	if !hasLogSignal(toks) {
		return "", "", false, false
	}
	if m := logFieldKQLRe.FindStringSubmatch(raw); m != nil && logFieldNameOK(m[1]) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		if v = strings.TrimSpace(v); v != "" {
			contains = logFieldContainsCue(toks)
			if strings.HasPrefix(v, "*") && strings.HasSuffix(v, "*") && len(v) > 2 {
				v, contains = strings.Trim(v, "*"), true
			}
			return m[1], v, contains, true
		}
	}
	m := logFieldWordRe.FindStringSubmatch(raw)
	if m == nil || !logFieldNameOK(m[1]) {
		return "", "", false, false
	}
	// Türkçe ek apostrofu ("field'ında", "attribute'unda") tırnak değil:
	// harfler arasındaki apostrof silinir, yoksa değer "ında" çıkardı.
	q := logFieldQuotedRe.FindStringSubmatch(logFieldSuffixApostropheRe.ReplaceAllString(raw, "$1$2"))
	if q == nil || strings.TrimSpace(q[1]) == "" {
		return "", "", false, false
	}
	return m[1], strings.TrimSpace(q[1]), logFieldContainsCue(toks), true
}

// logFieldSearchQuery — (alan, değer, içeren, backend) → logstore.Search
// KQL dizesi + dürüstlük notu (boş = tam istenen şekilde koştu).
func logFieldSearchQuery(field, value string, contains bool, backend string) (q, note string) {
	phrase := field + `:"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	if !contains {
		return phrase, ""
	}
	if backend == "clickhouse" && logFieldBareValueRe.MatchString(value) {
		return field + ":*" + value + "*", ""
	}
	label := backend
	if label == "" {
		label = "log arka ucu"
	}
	return phrase, fmt.Sprintf("'geçen' (içeren) araması %s'de baştan joker desteklenmediğinden TAM İFADE eşleşmesi olarak koştu; alt-dize eşleşmeleri eksik olabilir", label)
}

// guidedLogFieldBundle — arama + kanıt; route.LogQuery koşulan sorguyla
// doldurulur (link aynı sorguyu taşır — ReqWindow deseninin ikizi).
func (s *Server) guidedLogFieldBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	backend := ""
	if s.logs != nil {
		backend = s.logs.Backend()
	}
	q, note := logFieldSearchQuery(route.LogField, route.LogValue, route.LogContains, backend)
	route.LogQuery = q
	args, _ := json.Marshal(map[string]any{"q": q, "service": route.Service, "env": route.Env, "limit": guidedLogFieldSample})
	n := emitGuidedStep(emit, "log_search", string(args))
	if s.logs == nil {
		err := fmt.Errorf("log backend not configured")
		emitGuidedStepResult(emit, n, "log_search", "", err)
		return "", "", err
	}
	page, err := s.logs.Search(ctx, logstore.Filter{Service: route.Service, Env: route.Env, From: from, To: to, Search: q, Limit: guidedLogFieldSample})
	if err != nil {
		emitGuidedStepResult(emit, n, "log_search", "", err)
		return "", "", err
	}
	emitGuidedStepResult(emit, n, "log_search", fmt.Sprintf("%d kayıt", page.Total), nil)
	src := fmt.Sprintf("log araması %q (son %s, %s)", q, fmtAgoTR(rangeS), backend)
	if route.Env != "" && page.EnvUnapplied {
		src += "; ortam filtresi uygulanamadı (log verisinde ortam alanı çözülemedi)"
	}
	return renderLogFieldEvidenceTR(page, *route, note, rangeS), src, nil
}

func logSeverityNameTR(n uint8) string {
	switch {
	case n >= 21:
		return "FATAL"
	case n >= 17:
		return "ERROR"
	case n >= 13:
		return "WARN"
	case n >= 9:
		return "INFO"
	case n >= 5:
		return "DEBUG"
	case n >= 1:
		return "TRACE"
	}
	return "UNSET"
}

// renderLogFieldEvidenceTR — SAF: sayı, dağılım, örnek satırlar; alanın
// değeri kayıtta varsa satıra eklenir (modelin "gerçekten eşleşti mi"
// sorusuna kanıt).
func renderLogFieldEvidenceTR(page *logstore.Page, route guidedRoute, note string, rangeS int64) string {
	var b strings.Builder
	mode := "geçen"
	if !route.LogContains {
		mode = "eşit olan"
	}
	scope := ""
	if route.Service != "" {
		scope = ", servis: " + route.Service
	}
	fmt.Fprintf(&b, "Log araması — %s alanında %q %s kayıtlar (son %s%s): toplam %d kayıt.\n", route.LogField, route.LogValue, mode, fmtAgoTR(rangeS), scope, page.Total)
	if note != "" {
		b.WriteString("⚠ " + note + "\n")
	}
	if route.Env != "" && page.EnvUnapplied {
		b.WriteString("⚠ Ortam filtresi uygulanamadı; sayılar tüm ortamları kapsıyor.\n")
	}
	if page.Total == 0 || len(page.Logs) == 0 {
		b.WriteString("Bu pencerede eşleşen log yok — bunu dürüstçe söyle; alan adı ya da değer yazımı farklı olabilir (ör. url.full yerine http.url).\n")
		return b.String()
	}
	sev := map[string]int{}
	svc := map[string]int{}
	for _, l := range page.Logs {
		sev[logSeverityNameTR(l.Severity)]++
		svc[l.ServiceName]++
	}
	var parts []string
	for _, name := range append(guidedSeverityOrder, "UNSET") {
		if v := sev[name]; v > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, v))
		}
	}
	fmt.Fprintf(&b, "Örnek %d kayıtta severity: %s.\n", len(page.Logs), strings.Join(parts, ", "))
	if route.Service == "" && len(svc) > 0 {
		type kv struct {
			k string
			v int
		}
		var ks []kv
		for k, v := range svc {
			ks = append(ks, kv{k, v})
		}
		sort.Slice(ks, func(i, j int) bool {
			if ks[i].v != ks[j].v {
				return ks[i].v > ks[j].v
			}
			return ks[i].k < ks[j].k
		})
		if len(ks) > 5 {
			ks = ks[:5]
		}
		var sp []string
		for _, e := range ks {
			sp = append(sp, fmt.Sprintf("%s %d", e.k, e.v))
		}
		b.WriteString("Servisler (örnekte): " + strings.Join(sp, ", ") + ".\n")
	}
	b.WriteString("Örnek satırlar (en yeni):\n")
	for i, l := range page.Logs {
		if i >= 8 {
			break
		}
		ts := time.Unix(0, l.Timestamp).UTC().Format("15:04:05")
		line := fmt.Sprintf("- %s %s %s: %s", ts, logSeverityNameTR(l.Severity), l.ServiceName, truncRunes(strings.TrimSpace(l.Body), 160))
		if v, ok := l.Attributes[route.LogField]; ok && v != "" {
			line += fmt.Sprintf(" [%s=%s]", route.LogField, truncRunes(v, 120))
		} else if v, ok := l.ResourceAttributes[route.LogField]; ok && v != "" {
			line += fmt.Sprintf(" [%s=%s]", route.LogField, truncRunes(v, 120))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("Yorum: sayıyı ve dağılımı söyle; satırların ortak örüntüsünü (aynı hata, aynı uç) belirt; kanıtta olmayan servis adı ya da sayı uydurma.\n")
	return b.String()
}
