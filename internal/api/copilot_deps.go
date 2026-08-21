package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// copilot_deps.go — bağımlılık sağlığı bundle'ları (v0.9.420, CoSRE
// fikir #5): "hangi db yavaş?", "kafka lag nasıl?". İkisi de MV-destekli
// tek okuma (db_summary_5m ailesi / messaging MV'leri) — spans taraması
// yok. Servisli soruda not düşülür (kırılımlar global; servis-özel detay
// Databases/Messaging sayfasında).
//
// v0.9.1147 (AI Faz 3.4, D6) — OKUMA BURADAN ÇIKTI. Aynı iki okuma artık
// mcptools'un ortak katmanında yaşıyor (ReadDBHealth / ReadMessagingHealth)
// ve get_db_health / get_messaging_health tool'ları da oradan besleniyor.
// Bu dosyada kalan iş TEK: yapısal veriyi Türkçe kanıt metnine çevirmek.
// Bölüm şöyle:
//
//	bundle  = adım çipi + ortak okuma + renderer  (Server metodu)
//	renderer = SAF fonksiyon (veri → metin)       (tablo testli, byte-pinli)
//
// Metin BİLEREK bayt-bayt eskisi: guided cevap kalitesi bu bloklara
// göre kalibre edildi. Tek ekleme, okuma STORE TAVANINA dayandığında
// düşen ek not — eski hâl 200 destination'lık tavanı "tam 200 var" gibi
// gösteriyordu (v0.9.813'ün ısırdığı sınıf) ve bayrak zaten elimizdeydi.

// guidedDepRows — kanıt bloğunda gösterilen satır sayısı. Ortak katmana
// LIMIT olarak gidiyor, yani kesme okumanın değil GÖSTERİMİN kararı.
const guidedDepRows = 10

// guidedDepSlowest — p95 kısayolunda kaç ad anılır.
const guidedDepSlowest = 3

func (s *Server) guidedDBHealthBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time, rangeS int64) (string, string, error) {
	nDB := emitGuidedStep(emit, "db_summary", "")
	// v0.9.821 — çağıran listesi ve receiver keşfi bu bundle'a hiç
	// girmiyor (aşağıda yalnız sayaç/quantile okunuyor), o yüzden HAFİF
	// ikiz: dört katalog probu + dört metric_points taraması + bir tam
	// çağıran taraması boşuna ödenmiyor. (v0.9.1147: aynı hafif çağrı
	// artık ReadDBHealth'in içinde — zarfın RowsCapped bayrağı bonus.)
	data, err := mcptools.ReadDBHealth(ctx, s.mcpDeps(), from, to, guidedDepRows)
	if err != nil {
		emitGuidedStepResult(emit, nDB, "db_summary", "", err)
		return "", "", err
	}
	ev, src, rerr := renderDBHealthEvidenceTR(data, service, rangeS)
	emitGuidedStepResult(emit, nDB, "db_summary", ev, rerr)
	return ev, src, rerr
}

// renderDBHealthEvidenceTR — SAF. (kanıt metni, kaynak) döndürür; hata
// dönüşü imza uyumu içindir (bundle'lar üçlü döner) ve daima nil.
func renderDBHealthEvidenceTR(data mcptools.DBHealthData, service string, rangeS int64) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Veritabanı kırılımı — son %s, filo geneli.\n", fmtAgoTR(rangeS))
	if service != "" {
		fmt.Fprintf(&b, "Not: soru %s servisini anıyor; bu kırılım TÜM çağıranları kapsar — servis-özel detay Databases sayfasında.\n", service)
	}
	if len(data.Rows) == 0 {
		b.WriteString("Pencerede DB çağrısı görülmedi (db.system'li span yok).\n")
		return b.String(), "db_summary_5m (boş)", nil
	}
	// En yoğun N — hacim bağlamı; ardından p95'e göre en yavaş 3 ayrıca işaretlenir.
	if data.Truncated {
		fmt.Fprintf(&b, "(%d instance'tan en yoğun %d'u)\n", data.Total, len(data.Rows))
	}
	for _, d := range data.Rows {
		name := d.System + "/" + d.Instance
		if d.DBName != "" && d.DBName != "default" {
			name += "·" + d.DBName
		}
		fmt.Fprintf(&b, "- %s · %d çağrı · hata %%%.2f · avg %.1fms · p95 %.1fms\n",
			name, d.Calls, d.ErrorRatePct, d.AvgMs, d.P95Ms)
	}
	if slow := mcptools.DBHealthSlowestByP95(data.Rows, guidedDepSlowest); len(slow) > 0 {
		b.WriteString("p95'e göre en yavaşlar: ")
		for i, d := range slow {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s/%s (%.1fms)", d.System, d.Instance, d.P95Ms)
		}
		b.WriteString("\n")
	}
	if data.StoreCapped {
		fmt.Fprintf(&b, "Not: DB satır okuması tavana (%d) dayandı — liste EKSİK olabilir, sayılar alt sınırdır.\n", data.StoreRowLimit)
	}
	return b.String(), fmt.Sprintf("db_summary_5m MV (%d instance, %s)", data.Total, fmtAgoTR(rangeS)), nil
}

func (s *Server) guidedMessagingBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time, rangeS int64) (string, string, error) {
	nMsg := emitGuidedStep(emit, "messaging_summary", "")
	// v0.9.813 — GetMessaging artık zarf döndürüyor (RowsCapped ilanı
	// için); copilot bağlamı satırların kendisini istiyor. v0.9.1147 —
	// zarfı ortak katman açıyor ve bayrağı BURAYA da taşıyor.
	data, err := mcptools.ReadMessagingHealth(ctx, s.mcpDeps(), from, to, guidedDepRows)
	if err != nil {
		emitGuidedStepResult(emit, nMsg, "messaging_summary", "", err)
		return "", "", err
	}
	ev, src, rerr := renderMessagingEvidenceTR(data, service, rangeS)
	emitGuidedStepResult(emit, nMsg, "messaging_summary", ev, rerr)
	return ev, src, rerr
}

// renderMessagingEvidenceTR — SAF (bkz. renderDBHealthEvidenceTR).
func renderMessagingEvidenceTR(data mcptools.MessagingHealthData, service string, rangeS int64) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Mesajlaşma kırılımı — son %s, filo geneli.\n", fmtAgoTR(rangeS))
	if service != "" {
		fmt.Fprintf(&b, "Not: soru %s servisini anıyor; bu kırılım TÜM üretici/tüketicileri kapsar — servis-özel detay Messaging sayfasında.\n", service)
	}
	if len(data.Rows) == 0 {
		b.WriteString("Pencerede messaging çağrısı görülmedi (messaging.system'li span yok).\n")
		return b.String(), "messaging MV (boş)", nil
	}
	if data.Truncated {
		fmt.Fprintf(&b, "(%d destination'dan en yoğun %d'u)\n", data.Total, len(data.Rows))
	}
	for _, m := range data.Rows {
		fmt.Fprintf(&b, "- %s/%s · %s · %d mesaj-span · hata %%%.2f · avg %.1fms · p95 %.1fms\n",
			m.System, m.Cluster, m.Destination, m.Calls, m.ErrorRatePct, m.AvgMs, m.P95Ms)
	}
	if data.StoreCapped {
		fmt.Fprintf(&b, "Not: destination okuması tavana (%d) dayandı — liste EKSİK olabilir, sayılar alt sınırdır.\n", data.StoreRowLimit)
	}
	b.WriteString("Not: consumer-lag doğrudan ölçülmez (broker metriği ingest edilmiyor) — yüksek avg/p95 consumer tarafındaki işleme süresidir; lag şüphesinde Messaging sayfasındaki trend'e bak.\n")
	return b.String(), fmt.Sprintf("messaging MV (%d destination, %s)", data.Total, fmtAgoTR(rangeS)), nil
}
