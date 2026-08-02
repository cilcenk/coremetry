// v0.9.550 — evaluator sağlık okuması.
//
// Operatör: "bazen sanki takıldığını hissediyorum." Bu his ölçülebilir
// değildi çünkü Problems sayfasının BOŞ olması iki bambaşka durumu
// birebir aynı gösteriyordu:
//
//	(a) her şey yolunda, açık problem yok
//	(b) evaluator ölü/takılı, kimse problem üretmiyor
//
// Bu endpoint o ikisini ayırır. Kalp atışını worker pod'u Redis'e
// yazar (evaluator/heartbeat.go), API pod'u buradan okur — dağıtık
// kurulumda evaluator ile /api/problems FARKLI pod'larda çalıştığı
// için paylaşımlı bir yerden geçmek zorunlu.
//
// Tasarım kararı: TAZELİĞİ SUNUCU HESAPLAR. İstemciye ham zaman
// damgası verip "sen çıkar" demek, tarayıcı saati kayan bir makinede
// (VDI, uyku sonrası laptop) sessizce yanlış bir yaş üretirdi. Aynı
// dersi v0.9.543'te chNodeWork'te de uygulamıştık: geçen süre
// sunucunun generatedAt'inden türer.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/evaluator"
)

// EvaluatorHealth — /api/problems/evaluator yanıtı.
type EvaluatorHealth struct {
	// Status — dört hal, dördü de FARKLI şeyler söyler:
	//
	//	ok       — son tik taze ve temiz
	//	stale    — kalp atışı var ama bayat (takılma / lider yok /
	//	           worker dağıtılmamış)
	//	failing  — son tik hata ile bitti (deadline dahil)
	//	unknown  — ÖLÇEMİYORUZ (Redis yok veya hiç kalp atışı
	//	           yazılmamış). "ok" DEĞİLDİR ve öyle gösterilmemeli;
	//	           bilmemeyi iyi habere çevirmek bu işin tam da
	//	           düzeltmeye çalıştığı hata.
	Status string `json:"status"`
	// Reason — operatöre gösterilecek tek cümlelik açıklama.
	Reason string `json:"reason"`

	// AgeSec — son tikin bitişinden bu yana geçen süre (sunucu saati).
	// Status=unknown iken anlamsızdır, -1 döner.
	AgeSec int64 `json:"ageSec"`

	// Son tikin ayrıntıları; unknown iken sıfır değerli.
	DurationMS int64  `json:"durationMs"`
	Rules      int    `json:"rules"`
	Opened     int    `json:"opened"`
	Resolved   int    `json:"resolved"`
	Err        string `json:"err,omitempty"`
	Version    string `json:"version,omitempty"`
}

// evaluatorStaleAfter — kalp atışının bayat sayılacağı yaş.
//
// Yazanın bildirdiği aralığın 3 katı: bir tik kaçırmak (yavaş bir tur,
// lider devri) alarm sebebi değildir, üç tik üst üste kaçırmak
// gerçekten bir şeyin durduğu anlamına gelir. Aralık kalp atışının
// içinden gelir — burada sabit yazmak, evaluator'ın temposu
// değiştiğinde sessizce yanlış eşiğe düşmek olurdu.
//
// intervalMs geçersizse (eski kayıt, bozuk JSON) 1 dakikalık varsayılan
// kullanılır — bu, evaluator.New(store, time.Minute, ...) ile aynı.
func evaluatorStaleAfter(intervalMS int64) time.Duration {
	iv := time.Duration(intervalMS) * time.Millisecond
	if iv <= 0 {
		iv = time.Minute
	}
	return 3 * iv
}

// evaluatorHealthFrom — saf karar fonksiyonu (test edilebilir olsun
// diye handler'dan ayrı). now dışarıdan gelir ki test saate bağlı
// olmasın.
func evaluatorHealthFrom(hb *evaluator.Heartbeat, found bool, cacheOn bool, now time.Time) EvaluatorHealth {
	switch {
	case !cacheOn:
		return EvaluatorHealth{
			Status: "unknown", AgeSec: -1,
			Reason: "Redis bağlı değil — evaluator'ın çalışıp çalışmadığı ölçülemiyor.",
		}
	case !found:
		return EvaluatorHealth{
			Status: "unknown", AgeSec: -1,
			Reason: "Hiç kalp atışı yok — worker (COREMETRY_MODE=worker veya all) çalışmıyor olabilir.",
		}
	}

	// FinishedAt=0: tik başladı, bitmedi. Yaşı BAŞLANGIÇTAN sayarız —
	// bitmemiş bir tik tam da "takıldı" vakasıdır ve onu ölçmemek
	// bütün bu işi anlamsız kılardı.
	ref := hb.FinishedAt
	if ref <= 0 {
		ref = hb.StartedAt
	}
	age := now.Sub(time.Unix(0, ref))
	if age < 0 {
		age = 0 // saat kayması; negatif yaş göstermektense sıfırla
	}

	out := EvaluatorHealth{
		AgeSec:     int64(age.Seconds()),
		DurationMS: hb.DurationMS,
		Rules:      hb.Rules,
		Opened:     hb.Opened,
		Resolved:   hb.Resolved,
		Err:        hb.Err,
		Version:    hb.Version,
	}

	// Sıra önemli: bayatlık hatadan ÖNCE gelir. Hem bayat hem hatalı
	// bir kayıtta operatörün ilk bilmesi gereken şey kaydın eski
	// olduğudur — yoksa 20 dakika önceki bir hatayı şu anki durum
	// sanar.
	if age > evaluatorStaleAfter(hb.IntervalMS) {
		out.Status = "stale"
		out.Reason = "Son değerlendirme " + humanAgeTR(age) + " önce — evaluator takılmış veya durmuş olabilir."
		return out
	}
	if hb.Err != "" {
		out.Status = "failing"
		out.Reason = "Son değerlendirme hata ile bitti: " + hb.Err
		return out
	}
	out.Status = "ok"
	out.Reason = "Son değerlendirme " + humanAgeTR(age) + " önce · " +
		strconv.Itoa(hb.Rules) + " kural"
	return out
}

// humanAgeTR — yaşı kompakt Türkçe birimlerle yazar.
//
// anomaly.fmtAgeTR'nin ikizi; o dışa açık olmadığı için burada ayrı
// duruyor. Birim dallarının HEPSİ testte (sn/dk/sa/gün) — "Nh/Nd"
// şablonlarının her birimi ship anında test edilir kuralı, tam da bu
// sınıftan sessiz hatalardan doğdu.
func humanAgeTR(d time.Duration) string {
	s := int64(d.Seconds())
	if s < 0 {
		s = 0
	}
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + " sn"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + " dk"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + " sa"
	default:
		return strconv.FormatInt(s/86400, 10) + " gün"
	}
}

func (s *Server) getEvaluatorHealth(w http.ResponseWriter, r *http.Request) {
	// cacheWired: cache hiç bağlanmamışsa (nil) okumaya kalkmayız.
	// Noop cache ile "Redis var ama kayıt yok" halini AYIRT EDEMEYİZ —
	// Cache arayüzünde böyle bir sorgu yok ve sırf bunun için arayüze
	// metot eklemek gereksiz geniş bir dokunuş olurdu. İkisi de aynı
	// gözlemi verir (okuyamadık) ve operatörün atacağı adım da aynı,
	// bu yüzden tek dürüst mesajda birleştiriliyor.
	cacheWired := s.cache != nil

	var hb evaluator.Heartbeat
	found := false
	if cacheWired {
		if b, ok, err := s.cache.Get(r.Context(), evaluator.HeartbeatKey); err == nil && ok {
			if json.Unmarshal(b, &hb) == nil {
				found = true
			}
		}
	}

	writeJSON(w, evaluatorHealthFrom(&hb, found, cacheWired, time.Now()))
}
