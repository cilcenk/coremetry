package api

import (
	"fmt"
	"time"
)

// service_dbqueries_key.go — /api/services/{name}/db-queries cache
// anahtarı (v0.10.16, F0.2a).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Anahtar `from.UnixNano()` / `to.UnixNano()` taşıyordu. `to` ise ön
// yüzün aralık seçicisinden geliyor ve şimdi-tabanlı, yani her istekte
// farklı. Sonuç: anahtar HER İSTEKTE benzersiz.
//
// Bunun anlamı, `serveCached`in dört mekanizmasının DÖRDÜNÜN de ölü
// olması — TTL bir dakikaymış gibi görünüyor ama hiçbir zaman
// kullanılmıyor:
//
//	L1              → anahtar yeni, tutmaz
//	L2 (Redis)      → anahtar yeni, tutmaz
//	stale-while-rev → aynı anahtar altında eski kayıt gerekir, yok
//	singleflight    → yalnız AYNI anahtarı birleştirir, anahtarlar ayrı
//
// Yani her tık, `spans` üzerinde iki iç içe regex + üç quantile içeren
// bir toplamayı sıfırdan çalıştırıyordu (`max_execution_time = 25`).
// Uç nokta soğuk görünmüyordu, ÖLÇÜLMÜYORDU: cache oranı panelinde
// düşük değil, hiç yok.
//
// ── NEDEN ANAHTAR "ZAMANSIZ" YAPILAMIYOR ────────────────────────────────
//
// v0.9.1082 (/rootcause) aynı sınıftan bir kusurdu ve orada çözüm
// anahtarı zaman türevinden TAMAMEN kurtarmaktı — çünkü bir problemin
// `started`/`resolved` damgaları değişmez. Burada pencere GERÇEKTEN
// girdinin kendisi; zamanı atmak anahtarı yanlış yapardı. O yüzden
// çözüm kesme, kaldırma değil.
//
// Kalan maliyet dürüstçe: dakikada BİR soğuk hesap. Kova döndüğünde yeni
// anahtarın altında kayıt olmadığı için SWR devreye giremez — bu, zaman
// kovalı her anahtarın yapısal tavanı. Tamamen kaldırmak `spans` yerine
// `db_statement_summary_5m` okumayı gerektirir (F0.1/F0.4 kapsamı).
//
// ── NEDEN AŞAĞI DEĞİL, AŞAĞI+YUKARI ─────────────────────────────────────
//
// İki ucu da aşağı kesmek sub-dakika pencerelerde `from == to` üretir ve
// sorgu SIFIR satır döner — panel boşalır, hata da vermez. Bu tam olarak
// "boş küme kaybolur, sıfır olmaz" sınıfı. O yüzden `from` aşağı, `to`
// YUKARI yuvarlanıyor: pencere asla çökmez, istenen aralığı her zaman
// kapsar ve dakika sınırında hâlâ kararlıdır.

// dbQueriesFloor / dbQueriesCeil — pencereyi dakika kovasına oturtur.
// `to` yukarı yuvarlandığı için sorgu birkaç saniye "geleceği" isteyebilir;
// gelecekte veri olmadığından bu zararsız, pencerenin çökmemesi ise şart.
func dbQueriesFloor(t time.Time) time.Time { return t.Truncate(time.Minute) }

func dbQueriesCeil(t time.Time) time.Time {
	if f := t.Truncate(time.Minute); f.Equal(t) {
		return f
	} else {
		return f.Add(time.Minute)
	}
}

// serviceDBQueriesWindow — sorgunun GERÇEKTEN kullanacağı pencere.
//
// Anahtarla veri aynı kaynaktan gelmeli: yalnız anahtarı kovalayıp
// pencereyi ham bırakmak, aynı kovadaki iki isteğin farklı veriye aynı
// adı vermesi demek olurdu (ilk gelen neyi hesapladıysa o kalır).
func serviceDBQueriesWindow(from, to time.Time) (time.Time, time.Time) {
	return dbQueriesFloor(from), dbQueriesCeil(to)
}

// serviceDBQueriesKey — TÜM girdileri taşıyan anahtar: servis, kovalanmış
// pencere, limit. Saf tutuluyor ki kovalamanın kendisi test edilebilsin
// (canlı CH'siz).
func serviceDBQueriesKey(service string, from, to time.Time, limit int) string {
	bf, bt := serviceDBQueriesWindow(from, to)
	return fmt.Sprintf("service-db-queries:svc=%s:from=%d:to=%d:limit=%d",
		service, bf.Unix(), bt.Unix(), limit)
}
