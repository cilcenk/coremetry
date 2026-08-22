// v0.9.1281 — OTOMATİK verdict üretimi (Dynatrace-parite B1 #1).
//
// NEDEN: verdict yalnız ✨ Explain TIKLAMASINDA üretiliyordu. Yani
// operatörün açmadığı her P1 için kök-neden hakemi hiç koşmuyordu —
// motorun en pahalı parçası, tam da en çok gerektiği anda (gece 03:00'te
// açılan bir P1) sessiz kalıyordu. Dynatrace'te Davis'in kararı olay
// açılırken hazırdır; operatör onu üretmez, OKUR.
//
// Bu dosya ile B1 #6 (kalıcı gövde) AYNI dilimde ve bu bir tercih değil
// zorunluluk: üretmeden kalıcılaştıracak bir şey yok, kalıcılaştırmadan
// da otomatik üretimin hiçbir okuyucusu yok (arka planda üretilen bir
// verdict'i tıklamayan operatör asla göremezdi — önbellek 30 dakikada
// düşer). İkisi ayrı gönderilseydi ikisi de ölü kod olurdu.
//
// A2 KARARI KORUNUYOR: insight kartı hâlâ otomatik LLM ATEŞLEMEZ.
// Üretimi tetikleyen tek şey derin soruşturma kapısı (worker, leader
// gated); kart ve çekmece yalnız KALICI SATIRI OKUR ve okumak üretim
// değildir.
package api

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// rcaVerdictAutoSurface — /ai atıf etiketi. rcaVerdictSurface'ten AYRI
// olması bu dilimin sözleşmesi: /ai sayfası "operatör tıkladı" ile
// "arka planda üretildi" trafiğini ayırt edebilmeli, yoksa otomatik
// üretimin maliyeti ve kalitesi tıklamalı trafiğin içinde kaybolur —
// ölçülemeyen bir maliyet, kapatılamayan bir maliyettir.
//
// ProblemExplainer'ın "problem-auto-explain" etiketiyle aynı gerekçe.
const rcaVerdictAutoSurface = "rootcause-auto"

// rcaAutoVerdictDedupWindow — aynı ankor için ne kadar süre yeniden
// üretilmez.
//
// 30dk, explain önbelleğinin TTL'iyle (10dk) DEĞİL, operatörün olayla
// ilgilendiği ölçekle hizalı. Sentezleyici 30 SANİYEDE bir koşuyor: kapı
// olmasaydı açık bir P1, çözülene kadar her yarım dakikada bir LLM
// çağrısı üretirdi (saatte 120). Kapıyla saatte 2.
//
// Hipotez sürümüne DEĞİL ZAMANA bağlı olması bilinçli: sentezleyici her
// tikte yeni bir sürüm damgalıyor (ReplacingMergeTree DEFAULT), yani
// sürüm bazlı bir dedup hiçbir zaman ısırmazdı.
const rcaAutoVerdictDedupWindow = 30 * time.Minute

// shouldGenerateAutoVerdict — bu ankor için şimdi üretilmeli mi? SAF.
//
// lastCreatedNs = 0 ⇒ hiç kayıt yok ⇒ üret.
// Taze kayıt (yaş ≤ pencere) ⇒ ÜRETME.
//
// Gelecek tarihli kayıt (saat kayması, çok podlu kurulumda beklenen)
// negatif yaş verir ve "taze" sayılır — yani ÜRETMEME tarafına düşer.
// Bu bilinçli güvenli yön: yanlış tarafa düşmenin bedeli bir eksik
// verdict (operatör ✨ ile üretebilir), diğer yönde ise kotayı yakan bir
// döngü olurdu.
func shouldGenerateAutoVerdict(lastCreatedNs, nowNs int64, window time.Duration) bool {
	if lastCreatedNs <= 0 {
		return true
	}
	return nowNs-lastCreatedNs > window.Nanoseconds()
}

// AutoRCAVerdict — arka plan işçisinin çağırdığı üretim yolu.
//
// İşçi (internal/anomaly) bu paketi IMPORT EDEMEZ — api zaten anomaly'yi
// import ediyor (insight.go), yani ters yön derleme döngüsü olurdu.
// Bu yüzden main.go kablolamasında fonksiyon DEĞERİ olarak enjekte
// ediliyor; paket taşıma refactor'ü yok.
//
// Leader disiplini ÇAĞIRANDA: bu fonksiyon sentezleyicinin kendi
// leader-gated tick'inin içinden koşar, yani ayrı bir kilide gerek yok
// (ikinci bir kilit, aynı işi iki kez kilitlemek olurdu).
//
// Hata dönmesi çağıranı DÜŞÜRMEZ (işçi logluyor): verdict üretimi
// hipotez yazımının yanında bir EK, ön koşul değil.
func (s *Server) AutoRCAVerdict(ctx context.Context, anchorKind string,
	h *chstore.RootCauseHypothesis, anchorStartNs int64) error {
	if s == nil || s.copilot == nil || h == nil {
		return nil
	}
	// Kota + ayar kapıları ProblemExplainer'ın SIRASININ AYNISI ve sıra
	// önemli: en ucuz kapı önce. AutoExplainEnabled operatörün "arka
	// planda AI koşmasın" vidası — kapalıyken YALNIZ otomatik yollar
	// susar, ✨ tıklaması etkilenmez.
	if !s.copilot.AutoExplainEnabled() || !s.copilot.Active() {
		return nil
	}
	// v0.9.200'ün devre-kesicisi: sağlayıcı 429 verdiyse arka plan
	// tüketicileri 1 saat susar ve kalan kota operatörün interaktif
	// çağrılarına kalır. Otomatik verdict tanımı gereği arka plandadır.
	if s.copilot.QuotaBackoffActive() {
		return nil
	}

	// DEDUP — taze kayıt varsa yeniden üretme. Okuma ucuz (küçük state
	// tablosu, LIMIT 1); LLM çağrısı değil. Okuma HATASI üretimi
	// engellemez: "bilmiyorum" ile "taze var" aynı şey değil, ve
	// bilmiyorken üretmek dürüst taraf (eksik verdict > sahte dedup).
	nowNs := time.Now().UnixNano()
	var lastNs int64
	if last, err := s.store.LatestRCAVerdictForAnchor(ctx, anchorKind, h.AnchorID); err != nil {
		log.Printf("[rca-auto] son verdict okunamadı (%s/%s): %v — dedup atlanıyor",
			anchorKind, h.AnchorID, err)
	} else if last != nil {
		lastNs = last.CreatedAt
	}
	if !shouldGenerateAutoVerdict(lastNs, nowNs, rcaAutoVerdictDedupWindow) {
		return nil
	}

	// Kimlik: tıklamalı yolun aynısı (newRandID(16)) — 👍/👎 rayı
	// exchange_id üzerinden çalışıyor ve otomatik üretilmiş bir verdict
	// de oylanabilmeli. Oylanamayan bir karar LEARN katmanına hiç
	// giremezdi.
	exchangeID := newRandID(16)
	ctx = copilot.WithMeta(ctx, copilot.CallMeta{ExchangeID: exchangeID})

	verdict, prose := s.buildRCAVerdictSurface(ctx, rcaVerdictAutoSurface, h, anchorStartNs)
	if verdict == nil {
		return nil
	}
	s.recordRCAVerdict(ctx, exchangeID, anchorKind, h.AnchorID,
		chstore.RCAVerdictSourceAuto, h, verdict, prose)
	return nil
}
