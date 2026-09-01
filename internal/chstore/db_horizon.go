package chstore

import "context"

// db_horizon.go — /databases sayfasının İKİ panelinin veri ufku
// (v0.10.18, F0.9a).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Sayfada iki panel ALT ALTA duruyor ve ufukları farklı:
//
//	"Called from services"    → db_summary_5m / db_caller_summary_5m → 90 GÜN
//	"DB receiver instances"   → ham metric_points                    →  7 GÜN
//
// Operatör `?range=30d` seçtiğinde üstteki dolu, alttaki boş çıkıyor.
// Bu tek başına kafa karıştırıcı olurdu; asıl kusur boş panelin BASTIĞI
// CÜMLE:
//
//	"No receiver-detected instances in this window. Point an
//	 OpenTelemetry database receiver … at one of your databases"
//
// Yani operatöre KURULUM eksik diyor. Gerçekte receiver çalışıyor,
// veri 23 gün önce TTL'e düşmüş. Bu beyan eksikliği değil, YANLIŞ TEŞHİS
// — operatörü var olmayan bir kurulum sorununu kovalamaya gönderiyor.
//
// ── NEDEN SUNUCUDAN ─────────────────────────────────────────────────────
//
// Arayüze "7 gün" yazmak en kolay yol ve YANLIŞ olur: 7, sabit kodlu bir
// sayı değil, `system_settings`'teki `retention.metrics` geçersiz
// kılmasının varsayılanı. Operatör onu 90d'ye çektiği an arayüz yalan
// söylemeye başlar — "doğruluk bir ayara asılı" sınıfı. O yüzden ETKİN
// değer sunucuda hesaplanıp zarfla gönderiliyor.
//
// ⚠ `s.ret.MetricsDays` TEK BAŞINA KULLANILAMAZ: SetRetention onu
// güncellemiyor (retention.go), yani boot yapılandırmasında donuyor.
// Canlı geçersiz kılma `GetRetention(ctx)`ten gelir — enforcer de tam
// olarak bunu yapıyor (retention_enforce.go), aynı sıra burada da
// izleniyor.

// dbMVHorizonDays — db_summary_5m / db_caller_summary_5m MV'lerinin TTL'i.
//
// ⚠ SABİT KODLU VE AYARLANAMAZ. SetRetention'ın planında hiçbir MV yok
// (retention.go), dolayısıyla operatör saklamayı kısaltsa bile bu iki MV
// 90 gün tutmaya devam eder. Sayı burada elle tekrarlanıyor çünkü CREATE
// ifadesindeki TTL bir dizge; ikisinin ayrışmasını `db_horizon_test.go`
// içindeki kaynak taraması engelliyor.
const dbMVHorizonDays = 90

// receiverHorizonDays — receiver panelinin ETKİN ufku (gün).
//
// Sıra: `retention.metrics` geçersiz kılması → boot yapılandırması.
// Ayrıştırma `parseRetentionDays` ile; o yardımcı hem `<n>d` hem `<n>h`
// biçimini biliyor ve testi var, yani "48h"i 48 gün sanma sınıfı
// (v0.6.36) burada yeniden doğmuyor.
//
// 0 dönerse ufuk BİLİNMİYOR demektir ve çağıran hiçbir şey ilan etmez —
// yanlış bir sayı basmaktansa susmak doğru.
// MetricsHorizonDays (v0.10.231, Influx D6) — metric_points'in ETKİN ufku
// (gün); receiverHorizonDays'in dışa açık adı. 0 = bilinmiyor. Dış seri
// mevsimsel baseline'ı bu kapıyla açılır: ufuk gün-çeşitliliği eşiğinin
// altındaysa mevsimsel okuma HİÇ yapılmaz (audit R7).
func (s *Store) MetricsHorizonDays(ctx context.Context) int { return s.receiverHorizonDays(ctx) }

func (s *Store) receiverHorizonDays(ctx context.Context) int {
	if ov, err := s.GetRetention(ctx); err == nil && ov.Metrics != "" {
		if d, err := parseRetentionDays(ov.Metrics); err == nil && d > 0 {
			return d
		}
	}
	if s.ret.MetricsDays > 0 {
		return s.ret.MetricsDays
	}
	return 0
}

// spanHorizonDays — üst panelin ETKİN ufku (gün).
//
// İki değerli, çünkü okuma yolu ikiye ayrılıyor:
//   - normal mod  → MV (db_summary_5m) → dbMVHorizonDays, ayarlanamaz
//   - env süzgeci → ham spans          → retention.spans, ayarlanabilir
//
// İkincisi denetimde YOKTU; doğrulama sırasında çıktı. Sayfa env modunda
// KAYNAĞIN değiştiğini zaten söylüyor ("ham spans") ama UFKUN kısaldığını
// söylemiyor — 90 → 30, yani aynı sınıftan ikinci bir sessiz daralma.
func (s *Store) spanHorizonDays(ctx context.Context, raw bool) int {
	if !raw {
		return dbMVHorizonDays
	}
	if ov, err := s.GetRetention(ctx); err == nil && ov.Spans != "" {
		if d, err := parseRetentionDays(ov.Spans); err == nil && d > 0 {
			return d
		}
	}
	if s.ret.SpansDays > 0 {
		return s.ret.SpansDays
	}
	return 0
}
