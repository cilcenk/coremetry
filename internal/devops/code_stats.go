package devops

// code_stats.go — kod-çekme sonuçlarının SÜREÇ-İÇİ sayacı (v0.9.1241).
//
// SORUN: "Kodu da incele" isabet ediyor mu? Bu soruya kadar tek cevap
// tek tek çekmeceleri açıp Reason satırını okumaktı. FetchCode on bir
// ayrı çıkmaz sınıfı üretiyor ama hiçbirini hiçbir yere YAZMIYORDU:
// süresi dolmuş bir PAT tüm filoda kod bağlamını sessizce kapatabilir
// ve açıklamalar kodsuz üretilmeye devam ettiği için (fail-open, doğru
// karar) hiçbir ekranda tek bir iz kalmazdı.
//
// DURUŞ — üç bilinçli karar:
//
//  1. SÜREÇ-İÇİ, CH YAZIMI YOK. Bu operatör telemetrisi, faturalama
//     değil. Davranış motorunun (behaviorObs, v0.9.936) ve ingest
//     sayaçlarının duruşunun aynısı: atomik sayaç, /admin/stats okur.
//     İstek başına bir CH yazımı, tam da pahalı olduğu için ölçmek
//     istediğimiz yola ikinci bir pahalı yol eklerdi.
//  2. SAYAÇLAR SÜREÇ BAŞLANGICINDAN BERİDİR ve restart'ta sıfırlanır.
//     Bu bir eksiklik değil, sözleşme — ekranda da AYNEN böyle yazıyor.
//     Yalan alternatifi (kalıcı sayaç) yeni bir tablo ve yeni bir
//     yazma yolu demekti.
//  3. SINIF KÜMESİ, FetchCode'un ZATEN döndürdüğü çıkmazlardan
//     TÜRETİLDİ — paralel bir taksonomi değil. Her sınıf, kodda tek bir
//     `return` noktasına karşılık gelir; sınıf orada, gerekçe dizesinin
//     yanında atanır. Reason METNİNDEN sonradan sınıf çıkarmak (string
//     eşleme) ilk yeniden yazımda sessizce kırılırdı.

import (
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// CodeOutcome — bir kod-çekme denemesinin sonucu.
//
// İki başarı hâli + on bir çıkmaz. Sınıflar FetchCode'un (ve ondan
// önceki buildCodeContext adımlarının) gerçek çıkışlarıyla BİREBİR;
// her birinin operatör için farklı bir eylemi vardır — karıştırmak
// "kod gelmiyor" cevabını tek bir çaresiz kutuya indirirdi.
type CodeOutcome string

const (
	// CodeOK — en az bir pencere geldi ve HİÇBİR kayıp yok.
	CodeOK CodeOutcome = "ok"
	// CodePartial — pencere geldi ama eksik: bütçe kesti, bir frame
	// ağaçta bulunamadı, süre/deneme tavanı doldu. Başarıyla aynı
	// kovaya koymak isabet oranını olduğundan iyi gösterirdi.
	CodePartial CodeOutcome = "partial"

	// ── Çıkmazlar ───────────────────────────────────────────────
	// Sıra, operatörün müdahale mesafesine göre: en yakın (ayar) →
	// en uzak (uzak sistem).

	// CodeUnconfigured — DevOps bağlantısı yok/ayarlanmamış.
	// Eylem: Ayarlar → Kod entegrasyonu.
	CodeUnconfigured CodeOutcome = "unconfigured"
	// CodeRepoUnresolved — servis için depo ADI çözülemedi (katalogda
	// pin yok, konvansiyon tutmadı). Eylem: servis kataloğuna Repository.
	CodeRepoUnresolved CodeOutcome = "repo-unresolved"
	// CodeProjectDeadEnd — proje adı ne ayarda ne pinde ne de servis
	// önekinden türetilebiliyor (v0.9.1240 çıkmazı).
	CodeProjectDeadEnd CodeOutcome = "project-dead-end"
	// CodeNoStack — kayıtta stacktrace yok ya da dosya+satır taşıyan
	// uygulama frame'i yok. Eylem YOK: bu, entegrasyonun değil verinin
	// hâli. Ayrı kova olmasının tek sebebi bu — arıza sanılmasın.
	CodeNoStack CodeOutcome = "no-stack"
	// CodeCatalogError — servis kataloğu OKUNAMADI ve yanlış depoya
	// düşmemek için adım bilerek iptal edildi (fail-CLOSED, v0.9.1236).
	CodeCatalogError CodeOutcome = "catalog-error"
	// CodeEmptyTree — depo ağacı BOŞ döndü: proje/depo adı büyük
	// olasılıkla yanlış (ya da PAT'ın Code(Read) yetkisi yok).
	CodeEmptyTree CodeOutcome = "empty-tree"
	// CodeTreeMiss — ağaç geldi ama frame'lerin dosyası içinde yok.
	// Klasik sebep: yanlış depo ya da branş sürüm farkı.
	CodeTreeMiss CodeOutcome = "tree-miss"
	// CodeWindowFailed — dosya bulundu, okundu, ama pencere kurulamadı
	// (satır aralığı boş). Nadir; ayrı tutuluyor çünkü tree-miss'ten
	// TAMAMEN farklı bir hikâye.
	CodeWindowFailed CodeOutcome = "window-failed"
	// CodeDeadline — BİZİM süre tavanımız doldu (v0.9.1237). Suç
	// ayrımı korunuyor: çağıran vazgeçtiyse bu sınıf DEĞİL.
	CodeDeadline CodeOutcome = "deadline"
	// CodeCancelled — çağıran gitti (tarayıcı kapandı, üst ctx düştü).
	// DevOps'un yavaşlığı değil; deadline ile birleştirmek olmayan bir
	// yavaşlığın peşine düşürürdü.
	CodeCancelled CodeOutcome = "cancelled"
	// CodeBackendError — DevOps sunucusu hata döndü (401/404/5xx, ağ).
	// PAT süresi dolduğunda burası dolar: sessiz filo-çapında bozulmayı
	// gösteren TEK kova.
	CodeBackendError CodeOutcome = "backend-error"
	// CodeOther — sınıflandırılmamış çıkış. Sıfırdan farklı olması
	// KODDA bir eksik demek: FetchCode'a sınıf atamayan yeni bir çıkış
	// eklenmiş. Sessizce "ok" saymaktansa görünür bir kova.
	CodeOther CodeOutcome = "other"
)

// codeMissClasses — çıkmaz kovalarının SIRASI. Dizi (slice değil):
// len() derleme-zamanı sabiti olsun ki sayaç bloğu sabit boyutlu
// kalsın — map + mutex yerine kilitsiz atomik dizi.
var codeMissClasses = [...]CodeOutcome{
	CodeUnconfigured,
	CodeRepoUnresolved,
	CodeProjectDeadEnd,
	CodeNoStack,
	CodeCatalogError,
	CodeEmptyTree,
	CodeTreeMiss,
	CodeWindowFailed,
	CodeDeadline,
	CodeCancelled,
	CodeBackendError,
	CodeOther,
}

// codeMissIndex — sınıf → dizi indisi. init'te BİR kez kuruluyor ve
// bir daha yazılmıyor; eşzamanlı okuma güvenli (yazma yok).
var codeMissIndex = func() map[CodeOutcome]int {
	m := make(map[CodeOutcome]int, len(codeMissClasses))
	for i, c := range codeMissClasses {
		m[c] = i
	}
	return m
}()

// codeObs — sayaç bloğu. Sıfır değeri kullanıma hazır; Service'e
// gömülü (paket-genel değil) çünkü ayar da istemci de Service'e ait —
// sayaçların ölçtüğü şey o nesnenin davranışı.
type codeObs struct {
	attempts atomic.Int64
	ok       atomic.Int64
	partial  atomic.Int64
	miss     [len(codeMissClasses)]atomic.Int64

	lastUnix    atomic.Int64
	lastOutcome atomic.Value // string
	// lastErr / lastErrUnix — son BAŞARISIZ denemenin gerekçesi ve anı.
	// YAPIŞKAN: sonraki bir başarı bunu SİLMEZ. Sebep, BehaviorPanel'in
	// v0.9.1077'de öğrendiğiyle aynı — flap eden bir arızayı tek şanslı
	// isabet ekrandan silerse operatör onu hiç görmez. Tazeliği
	// zaman damgası anlatır, silmek değil.
	lastErr     atomic.Value // string
	lastErrUnix atomic.Int64
}

// CodeMissCount — bir çıkmaz kovası. Slice olarak dönüyor (map değil):
// ekrandaki sıra deterministik olmalı, JSON map'i değil.
type CodeMissCount struct {
	Class string `json:"class"`
	Count int64  `json:"count"`
}

// CodeStats — /admin/stats'ın gördüğü anlık görüntü. Tüm sayaçlar
// SÜREÇ BAŞLANGICINDAN beri; restart sıfırlar.
type CodeStats struct {
	// Attempts — kod bağlamı İSTENEN her deneme (isabet + çıkmaz).
	Attempts int64 `json:"attempts"`
	// OK / Partial — tam ve kısmi isabet. İkisinin toplamı "kod geldi"
	// demektir; isabet oranı bu toplam / Attempts.
	OK      int64 `json:"ok"`
	Partial int64 `json:"partial"`
	// Misses — çıkmaz kovaları, ÇOKTAN AZA sıralı; sıfır olanlar hiç
	// yok. On bir boş kova göstermek asıl sinyali gömerdi.
	Misses []CodeMissCount `json:"misses,omitempty"`
	// LastUnix / LastOutcome — SON denemenin anı ve sınıfı.
	LastUnix    int64  `json:"lastUnix"`
	LastOutcome string `json:"lastOutcome,omitempty"`
	// LastError / LastErrorUnix — son BAŞARISIZ denemenin gerekçesi
	// (yapışkan, bkz. codeObs.lastErr).
	LastError     string `json:"lastError,omitempty"`
	LastErrorUnix int64  `json:"lastErrorUnix,omitempty"`
}

// HitRate — isabet oranı [0,1]. Deneme yoksa 0 (ve çağıran zaten
// Attempts==0'ı ayrı gösterir: "hiç denenmedi" ile "%0 isabet" aynı
// şey değil).
func (c CodeStats) HitRate() float64 {
	if c.Attempts <= 0 {
		return 0
	}
	return float64(c.OK+c.Partial) / float64(c.Attempts)
}

// record — tek denemenin sonucunu işler.
func (o *codeObs) record(class CodeOutcome, reason string) {
	now := time.Now().Unix()
	o.attempts.Add(1)
	switch class {
	case CodeOK:
		o.ok.Add(1)
	case CodePartial:
		o.partial.Add(1)
	default:
		i, known := codeMissIndex[class]
		if !known {
			// Bilinmeyen sınıf sessizce kaybolmaz: "other" kovası,
			// koddaki eksiği ekrana taşır.
			i, class = codeMissIndex[CodeOther], CodeOther
		}
		o.miss[i].Add(1)
		o.lastErr.Store(strings.TrimSpace(reason))
		o.lastErrUnix.Store(now)
	}
	o.lastOutcome.Store(string(class))
	o.lastUnix.Store(now)
}

// snapshot — sayaçların anlık görüntüsü.
func (o *codeObs) snapshot() CodeStats {
	s := CodeStats{
		Attempts:      o.attempts.Load(),
		OK:            o.ok.Load(),
		Partial:       o.partial.Load(),
		LastUnix:      o.lastUnix.Load(),
		LastErrorUnix: o.lastErrUnix.Load(),
	}
	if v, ok := o.lastOutcome.Load().(string); ok {
		s.LastOutcome = v
	}
	if v, ok := o.lastErr.Load().(string); ok {
		s.LastError = v
	}
	for i, class := range codeMissClasses {
		if n := o.miss[i].Load(); n > 0 {
			s.Misses = append(s.Misses, CodeMissCount{Class: string(class), Count: n})
		}
	}
	// Çoktan aza; eşitlikte sınıf adı — sıra deterministik olmalı ki
	// panel her yenilemede zıplamasın.
	sort.SliceStable(s.Misses, func(a, b int) bool {
		if s.Misses[a].Count != s.Misses[b].Count {
			return s.Misses[a].Count > s.Misses[b].Count
		}
		return s.Misses[a].Class < s.Misses[b].Class
	})
	return s
}

// RecordCodeOutcome — kod-çekme sonucunu sayar. FetchCode'dan ÖNCEKİ
// adımlar (katalog okuması, depo çözümü — internal/api) da buradan
// geçer: sayaç "operatör kod istedi" anını ölçmeli, yalnız işin
// FetchCode'a kadar gelebilen kısmını değil.
//
// nil-Service güvenli: kod entegrasyonu hiç kurulmamış bir sürecte
// çağıran ekstra bir muhafız yazmak zorunda kalmasın.
func (s *Service) RecordCodeOutcome(class CodeOutcome, reason string) {
	if s == nil {
		return
	}
	s.obs.record(class, reason)
}

// CodeObservability — sayaçların anlık görüntüsü; API sunucusu
// /admin/stats için çağırır (anomaly.BehaviorObservability muadili).
func (s *Service) CodeObservability() CodeStats {
	if s == nil {
		return CodeStats{}
	}
	return s.obs.snapshot()
}
