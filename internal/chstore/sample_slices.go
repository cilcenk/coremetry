package chstore

// sample_slices.go — v0.10.195 (Operator-reported: /system/k8s pod
// envanterinde 252 span'lik pod "0 sn boyunca görüldü"; kapsama kartı
// %81-99 "kısmi" satırlarla dolu).
//
// ── KÖK NEDEN ──────────────────────────────────────────────────────────
//
// v0.10.56 servis-başına kotayı `LIMIT n BY service_name` ile verdi ve
// "alfabetik ilk servisler" kusurunu kapattı — ama ZAMAN eksenindeki ikizi
// açık kaldı: `spans` birincil anahtarı (service_name, time), ORDER BY'sız
// LIMIT BY her servis için anahtarın ÖNEKİNİ, yani pencerenin İLK n
// span'ini döndürür. 400 span'lik kota yoğun bir serviste pencerenin ilk
// birkaç SANİYESİNDE dolar:
//   • pod envanteri "1 dk / 0 sn boyunca görüldü" der — pod 60 dk ayaktayken;
//   • pencerenin ilerisinde başlayan pod hiç görünmez;
//   • kapsama yargısı ("bu servis namespace yayıyor mu") 60 dakikalık bir
//     iddia gibi sunulan 3 saniyelik bir dilimden çıkar.
//
// ── ÇARE ───────────────────────────────────────────────────────────────
//
// Kota ZAMAN DİLİMİNE bölünür: `LIMIT m BY service_name,
// toStartOfInterval(time, INTERVAL b SECOND)`. Pencere sabit sayıda dilime
// (sampleSliceCount) kesilir, servis kotası dilimlere eşit dağıtılır;
// toplam ≈ aynı (34×12 = 408 ≈ 400), tarama maliyeti aynı (LIMIT BY zaten
// budamıyordu; dilim ifadesi satır başına bir toStartOfInterval). Sonuç:
// her servis pencerenin HER dilimiyle temsil edilir.
//
// Dış tavan (k8sCoverageSampleRows / podInventorySampleRows) yerinde.

// sampleSliceCount — pencerenin kaç zaman dilimine kesileceği. 12: 1 saatte
// 5 dk, 6 saatte 30 dk, 24 saatte 2 saat — pod yaşam döngüsü ölçeğinde
// anlamlı; daha ince dilim kota/dilim oranını 1-2'ye düşürür (gürültü).
const sampleSliceCount = 12

// sampleSliceMinSec — dilim tabanı (15 dk penceresi 75 s; 1 dk'lık pencere
// tek dilim kalır — kota bölünmez).
const sampleSliceMinSec = 60

// sampleSlices — SAF: pencere (s) + servis kotası → (dilim uzunluğu s,
// dilim başına kota). Dilim başına kota YUKARI yuvarlanır ki toplam kota
// asla servis kotasının altına düşmesin (400/12 = 33.3 → 34).
func sampleSlices(windowSec int64, perService int) (bucketSec int64, perBucket int) {
	if perService < 1 {
		perService = 1
	}
	bucketSec = windowSec / sampleSliceCount
	if bucketSec < sampleSliceMinSec {
		bucketSec = sampleSliceMinSec
	}
	slices := int(windowSec / bucketSec)
	if slices < 1 {
		slices = 1
	}
	perBucket = (perService + slices - 1) / slices
	return bucketSec, perBucket
}
