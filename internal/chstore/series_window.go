package chstore

// series_window.go — MV-kovalı zaman serisi uçlarının ORTAK ZARF
// SÖZLEŞMESİ (v0.9.819). /api/endpoints/series ve /api/databases/series
// bunu paylaşır; ikisi de aynı iki yalanı kapatmak zorunda:
//
//  1. KAPSANAN ARALIK ≠ İSTENEN ARALIK. MV kovaları BAŞLANGIÇLARIYLA
//     etiketli, yani `time_bucket >= from` yüklemi from'u kovaya
//     hizalamadan yazılırsa baştaki KISMİ kova tamamen düşer (from=10:03
//     → 10:00-10:05 arasındaki her şey yok olur). Hizalarsak da bu kez
//     istenenden ÖNCE başlayan bir kova serinin içine girer. İkisi de
//     dürüst değil — dürüst olan, kapsanan aralığı SÖYLEMEK.
//
//  2. SON KOVA HÂLÂ DOLUYOR. Canlı bir pencerede en son kova henüz
//     tamamlanmamıştır; sayısı düşük gelir ve grafik her yenilemede
//     sonu aşağı kıvrılan sahte bir DÜŞÜŞ çizer. Operatör bunu "trafik
//     azalıyor" diye okur. Nokta atılmaz (veri gerçek, sadece eksik) —
//     işaretlenir, frontend soluk çizer.
//
// Ölçüm değil AÇIKLAMA taşır: hiçbir alanı bir sayıyı değiştirmez,
// hepsi o sayının NE OLDUĞUNU söyler.
type SeriesWindow struct {
	// BucketSeconds — noktalar arası gerçek adım. İstemci adım SEÇMEZ;
	// sunucu MV grenine göre kararını verir ve ne yaptığını söyler.
	BucketSeconds int `json:"bucketSeconds"`
	// CoveredFromNs / CoveredToNs — serinin GERÇEKTEN kapsadığı aralık
	// (unix ns). CoveredFromNs istenen from'dan ERKEN olabilir: kova
	// hizalaması aşağı yuvarlar.
	CoveredFromNs int64 `json:"coveredFromNs"`
	CoveredToNs   int64 `json:"coveredToNs"`
	// PartialLastBucket — son nokta DOLMAKTA olan bir kovadan geliyor.
	PartialLastBucket bool `json:"partialLastBucket"`
}

// seriesLastBucketPartial — son kova eksik mi? SAF; tablo-güdümlü test.
//
// İKİ ayrı sebep aynı bayrağa iner ve ikisi de gerçek:
//
//	kova SONU > to   → kova pencerenin dışına taşıyor, sorgu onu kesti
//	kova SONU > now  → kova henüz KAPANMADI, veri hâlâ akıyor
//
// `>` bilinçli (`>=` değil): sonu tam olarak to'ya ya da now'a oturan
// bir kova TAMDIR, işaretlenirse dürüst bir nokta sahte şüphe alır.
func seriesLastBucketPartial(lastBucketStartS, bucketSec, toS, nowS int64) bool {
	if bucketSec <= 0 {
		return false
	}
	end := lastBucketStartS + bucketSec
	return end > toS || end > nowS
}
