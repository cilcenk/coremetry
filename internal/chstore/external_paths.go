package chstore

// v0.9.1255 — dış bağımlılık düğümünün YOL kırılımı.
//
// Operator-reported: topolojide dış düğüm `esbprod.example.internal` yazıyor
// ama operatörün bildiği şey URL'in DEVAMI — aynı base URL'in altında
// nvi/kps/yerlesimyerisorgulama gibi AYRI uçlar var ve hepsi tek bir
// pill'e kapanıyor. Düğümün kimliği host olarak KALIYOR (topology
// aggregator'ın `ext:<host>` düğüm adı; onu url bazlı yapmak grafiği
// binlerce düğüme patlatırdı) — eksik olan, o düğümün İÇİNİ açan
// kırılım.
//
// Neden ham url.full DEĞİL: url.full TEK bir span'ın değeridir ve
// yüksek kardinalitelidir (query string, id'ler). "Hangi uç sıcak"
// sorusunun cevabı normalize edilmiş yol GRUPLARI — /users/12345 ve
// /users/67890 aynı uçtur. Aynı gerekçe topology top_labels'ın
// v0.5.406'daki templatePath'ini doğurmuştu; token da bilerek AYNI
// (`{id}`), iki komşu yüzeyde iki farklı yer tutucu tutarsızlık olurdu.
//
// MV YOK, bilerek: yol kırılımı 5 dk'lık kenar MV'sinin grain'inin
// ALTINDA bir boyut (url) ve o boyutu MV'ye eklemek topology_edges_5m'i
// url kardinalitesiyle çarpardı. Bunun yerine ÇEKMECE-AÇILIŞINDA,
// sınırlı bir ham okuma — /endpoints çekmecesi + dbstmt_detail ile aynı
// sınıf. Sınırlar (clickhouse-schema adım 4):
//
//	· service_name IN ?  — spans ORDER BY (service_name, time) ÖN EKİ.
//	  Liste bedava geliyor: çekmece zaten MV'den çağıran servisleri
//	  okudu. Bu, okumayı "tüm servisler" yerine o hostu GERÇEKTEN
//	  çağıran ≤100 servise indiriyor.
//	· time >= ? AND time < ?  — günlük partition budaması.
//	· pencere externalPathsMaxWindow ile kırpılır (7g'lik bir seçim
//	  ham spans'te 7 günlük tarama demek). Kırpma GİZLENMEZ: çekmece
//	  gerçekten kullanılan pencereyi yazar (PathsWindowS).
//	· PREWHERE kind/db_system/msg_system — üçü de LowCardinality ve
//	  granülleri UCUZA eliyor; pahalı olan attr_keys/attr_values
//	  (ZSTD Array(String)) yalnız hayatta kalan satırlar için okunuyor.
//	  Bu okumanın ana maliyet kalemi o iki dizi.
//	· LIMIT + SETTINGS max_execution_time = 8. Tavan bilerek DAR:
//	  çekmece bir ek kırılım için 15 saniye asılı kalmamalı, ve
//	  aşıldığında cevap hata DEĞİL — üst yarı (çağıranlar + trend)
//	  çizilir, yol bloğu "okunamadı" der (external.go).
//	· p99 quantileTDigest — ham quantile() milyon satırda yasak.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ExternalPathRow — bir dış hostun tek bir NORMALIZE yol grubu.
// Path zaten templatelenmiş ("/orders/{id}/items"), ham değil.
type ExternalPathRow struct {
	Path      string  `json:"path"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	P99Ms     float64 `json:"p99Ms"`
}

// externalPathsMaxWindow — ham spans okumasının azami geriye bakışı.
// Çekmecenin zaman aralığı bundan genişse pencere SONA yaslanarak
// kırpılır ve kırpma payload'da beyan edilir.
const externalPathsMaxWindow = 6 * time.Hour

// externalPathsDefaultLimit — "en çok çağrılan yollar" tavanı.
const externalPathsDefaultLimit = 10

// externalPathPasses — her kural KAÇ kez uygulanır. RE2 (hem CH hem Go)
// eşleşmeleri ÜST ÜSTE BİNDİRMEZ ve kurallarımız segmentin iki yanındaki
// '/'i de tüketiyor: `/1/2/` taranırken `/1/` eşleşince imleç `2/`ye
// atlar ve ikinci segment o turda ıskalanır. İkinci geçiş kalanları
// toplar (n ardışık segment için 2 geçiş yeter: birinci geçiş bir
// atlayarak, ikincisi aradakileri). Lookahead RE2'de yok — bu yüzden
// çözüm geçiş sayısı, daha akıllı bir regex değil.
const externalPathPasses = 2

// externalPathRule — TEK kaynaklı kimlik-çökertme kuralı. Aynı desen
// hem CH tarafında (replaceRegexpAll ile GRUPLAMADAN ÖNCE, ki tavan
// ham yol çeşitlerine değil gruplara uygulansın) hem Go tarafında
// (dönen satırların son normalizasyonu) koşar. Desenler geri-referans
// İÇERMEZ: her iki yanındaki '/' de değişmezle geri yazılır, böylece
// CH'nin `\1` kaçış kuralına hiç girmeyiz.
type externalPathRule struct {
	Pattern string
	Replace string
	Why     string
}

// externalPathRules — sıra ÖNEMLİ. UUID önce: 8-4-4-4-12 biçimi tireli
// olduğu için saf-hex kuralına takılmaz, ama tireleri tek tek çökertecek
// bir kural eklenirse sıranın anlamı geri gelir; sıra bu yüzden pinli.
//
// Bilerek YOK: uzun opak base64url segmenti (path_template.go'daki
// b64IdRe). O kural ≥20 karakterlik her alfanumerik segmenti çökertiyor
// ve bu yüzeyde YANLIŞ cevap verirdi: operatörün aradığı segmentin
// kendisi uzun ve betimleyici ("YerlesimYeriSorgulama2", 22 karakter).
// Kimliği çökerten bir kural, kimliğin KENDİSİNİ silmemeli.
var externalPathRules = []externalPathRule{
	{
		Pattern: `/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/`,
		Replace: `/{id}/`,
		Why:     "kanonik UUID",
	},
	{
		Pattern: `/[0-9]+/`,
		Replace: `/{id}/`,
		Why:     "tamamı rakam segment (/orders/12345)",
	},
	{
		Pattern: `/[0-9a-fA-F]{8,}/`,
		Replace: `/{id}/`,
		Why:     "≥8 karakter saf hex — trace/span id, md5/sha öneki, oturum anahtarı",
	},
}

// externalPathRulesRe — externalPathRules'un Go derlemesi. Desenler CH
// ile BİREBİR aynı dizeler; RE2 sözdizimi iki tarafta da geçerli.
var externalPathRulesRe = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(externalPathRules))
	for i, r := range externalPathRules {
		out[i] = regexp.MustCompile(r.Pattern)
	}
	return out
}()

// externalPathSlashCollapse — kuralların ÖNÜNDE bir kez koşar. İki işi
// birden yapar: sondaki '/' (guard'la birlikte '//' olur) ve gövdedeki
// çift eğik çizgiler tek biçime iner, böylece "/a/b" ile "/a/b/" ve
// "/a//b" AYNI gruba düşer.
const externalPathSlashCollapse = `/{2,}`

var externalPathSlashCollapseRe = regexp.MustCompile(externalPathSlashCollapse)

// normalizeExternalPath — ham bir url.full / http.url / url.path
// değerini yol GRUBUNA çevirir.
//
// Boru hattı (CH tarafındaki ifade ile aynı sıra):
//  1. şema+host at (mutlak URL ise), query + fragment at
//  2. sona guard '/' ekle — son segment de kuralların '/segment/'
//     şekline uysun diye; kurallar aksi hâlde yalnız ORTA segmentleri
//     yakalardı
//  3. '/'+ → '/'  (sondaki eğik çizgi ve çift eğik çizgiler)
//  4. kimlik kuralları × externalPathPasses
//  5. guard '/'i sök (kök yol '/' olarak kalır)
//
// Boş girdi kökü ("/") döndürür: yolu olmayan bir URL kök çağrısıdır.
// Çağıran taraf zaten boş ham değerleri eliyor, bu yalnız sözleşme.
func normalizeExternalPath(raw string) string {
	p := externalURLToPath(raw)
	g := p + "/"
	g = externalPathSlashCollapseRe.ReplaceAllString(g, "/")
	for pass := 0; pass < externalPathPasses; pass++ {
		for i, re := range externalPathRulesRe {
			g = re.ReplaceAllString(g, externalPathRules[i].Replace)
		}
	}
	if len(g) > 1 {
		g = g[:len(g)-1]
	}
	return g
}

// externalURLToPath — mutlak URL'den yol kısmını, göreli değerden
// query/fragment'i ayıklar. Şemasız "host/yol" biçimi BİLEREK ele
// alınmıyor: onu hosttan ayırmak tahmin olurdu ve bu okuma zaten tek
// bir hosta kilitli.
func externalURLToPath(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			s = rest[j:]
		} else {
			s = "/"
		}
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "/"
	}
	if s[0] != '/' {
		s = "/" + s
	}
	return s
}

// externalPathNormalizeSQL — normalizeExternalPath'in 2-5. adımlarının
// CH ifadesi. `in` zaten yol hâline getirilmiş bir kolon/alias olmalı
// (1. adım SQL'de path()/cutQueryStringAndFragment ile yapılıyor).
//
// Neden SQL'de: gruplama tavanı (LIMIT 10) NORMALIZE edilmiş yollara
// uygulanmalı. Ham yolları gruplayıp Go'da birleştirmek, 200 ham
// varyanta bölünmüş bir grubun ilk 10'a hiç girememesi demekti —
// tavan cevabı sessizce değiştirirdi.
func externalPathNormalizeSQL(in string) string {
	cur := fmt.Sprintf("replaceRegexpAll(concat(%s, '/'), '%s', '/')", in, externalPathSlashCollapse)
	for pass := 0; pass < externalPathPasses; pass++ {
		for _, r := range externalPathRules {
			cur = fmt.Sprintf("replaceRegexpAll(%s, '%s', '%s')", cur, r.Pattern, r.Replace)
		}
	}
	return cur
}

// externalPathURLSQL — istemci span'inin URL'ini taşıyan attr zinciri.
// Sıra semconv'un yeni→eski hattı: url.full (1.21+) → http.url (eski) →
// url.path → http.target, en sonda http_route kolonu (rotalı istemci
// span'i; yol yoksa hiç değilse şablon).
const externalPathURLSQL = `coalesce(` +
	`nullIf(attr_values[indexOf(attr_keys, 'url.full')], ''), ` +
	`nullIf(attr_values[indexOf(attr_keys, 'http.url')], ''), ` +
	`nullIf(attr_values[indexOf(attr_keys, 'url.path')], ''), ` +
	`nullIf(attr_values[indexOf(attr_keys, 'http.target')], ''), ` +
	`nullIf(http_route, ''), '')`

// externalPeerHostSQL — dış düğümün host'unu türeten zincir. Topology
// aggregator'ın (topology.go infra pass) `infra_host` coalesce'iyle
// BİREBİR aynı ifade: düğümü üreten kriter neyse, düğümün içini açan
// okuma da aynı kriteri kullanmak ZORUNDA. İkinci bir kriter uydurmak,
// grafikte görünen çağrı sayısıyla çekmecede görünenin ıraksaması
// demekti.
const externalPeerHostSQL = `coalesce(` +
	`nullIf(peer_service, ''), ` +
	`nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''), ` +
	`nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''), '')`

// externalPathsSQL — sorgu metni, SAF kurucu (test edilebilirlik:
// wiring'i dize sabitine değil, gerçekten koşan metne assert ediyoruz).
//
// db_system/msg_system boşluk şartı da aggregator'dan geliyor: oradaki
// multiIf db/queue dallarını ext'ten ÖNCE alıyor, yani db_system dolu
// bir span dış düğüme HİÇ katkı vermiyor. kind='client' aynı şekilde
// iki ext dalının da ortak şartı.
func externalPathsSQL() string {
	return `
		SELECT if(length(g) > 1, substring(g, 1, length(g) - 1), '/') AS path,
		       count()                            AS calls,
		       countIf(status_code = 'error')     AS errors,
		       quantileTDigest(0.99)(duration) / 1e6 AS p99_ms
		FROM (
		  SELECT ` + externalPathNormalizeSQL("bp") + ` AS g, duration, status_code
		  FROM (
		    SELECT if(position(u, '://') > 0, path(u), cutQueryStringAndFragment(u)) AS bp,
		           duration, status_code
		    FROM (
		      SELECT ` + externalPathURLSQL + ` AS u, duration, status_code
		      FROM spans
		      PREWHERE kind = 'client' AND db_system = '' AND msg_system = ''
		      WHERE time >= ? AND time < ? AND service_name IN ?
		        AND ` + externalPeerHostSQL + ` = ? AND ` + externalPathURLSQL + ` != ''
		    )
		  )
		)
		GROUP BY path
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 8`
}

// clampExternalPathsWindow — pencereyi SONA yaslayarak kırpar. Saf +
// test edilebilir: çekmecenin gösterdiği etiket bu fonksiyonun
// döndürdüğü pencereden türüyor, "6 saat" diye sabit yazılmıyor.
func clampExternalPathsWindow(from, to time.Time) (time.Time, time.Time) {
	if to.Sub(from) > externalPathsMaxWindow {
		return to.Add(-externalPathsMaxWindow), to
	}
	return from, to
}

// GetExternalHostPaths — bir dış hostun en çok çağrılan NORMALIZE yol
// grupları. `services` MV'den gelen çağıran listesi; BOŞSA okuma hiç
// koşmaz (spans birincil anahtarının ön ekini kaybetmiş bir tarama
// olurdu) ve boş sonuç döner — bu meşru bir hâl, hata değil.
func (s *Store) GetExternalHostPaths(ctx context.Context, host string, services []string, from, to time.Time, limit int) ([]ExternalPathRow, error) {
	if host == "" || len(services) == 0 {
		return []ExternalPathRow{}, nil
	}
	if limit <= 0 {
		limit = externalPathsDefaultLimit
	}
	from, to = clampExternalPathsWindow(from, to)

	rows, err := s.conn.Query(ctx, externalPathsSQL(), from, to, services, host, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Go tarafı son normalizasyon: CH ifadesi ile AYNI kural listesinden
	// türediği için normalde no-op (kurallar idempotent — '{id}' hiçbir
	// desene uymaz). Yine de koşuyor, çünkü sözleşmeyi API'nin döndürdüğü
	// SATIRIN kendisi taşıyor; CH sürümleri arası bir sapma etiketleri
	// bozsa bile çekmece tutarlı grup adları görür. Birleşen satırlar
	// toplanır (≤10 satır, ihmal edilebilir maliyet).
	out := []ExternalPathRow{}
	idx := map[string]int{}
	for rows.Next() {
		var r ExternalPathRow
		if err := rows.Scan(&r.Path, &r.Calls, &r.Errors, &r.P99Ms); err != nil {
			return nil, err
		}
		r.Path = normalizeExternalPath(r.Path)
		if i, ok := idx[r.Path]; ok {
			out[i].Calls += r.Calls
			out[i].Errors += r.Errors
			if r.P99Ms > out[i].P99Ms {
				out[i].P99Ms = r.P99Ms
			}
			continue
		}
		idx[r.Path] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Calls > 0 {
			out[i].ErrorRate = float64(out[i].Errors) * 100 / float64(out[i].Calls)
		}
	}
	return out, nil
}
