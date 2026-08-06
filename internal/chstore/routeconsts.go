package chstore

// routeconsts.go — sorgu yönlendirme EŞİKLERİNİN tek sahibi (v0.9.705,
// parite FAZ 1 dilim 1).
//
// Parite taban çizgisi (docs/charts/parity-baseline.md §C) yönlendirme
// mantığının BEŞ ayrı karar noktasına dağıldığını ölçtü: PickRollup,
// rollup_fastpath, selectMetricTier, op-MV kapısı (spanmetric.go'da
// AYNI literal ÜÇ kez) ve TS aynası (resolverEligibility.ts +
// chartStep.ts). Karar noktaları farklı KAYNAKLARA hizmet ediyor —
// tek fonksiyona indirmek bu dilimin işi değil; bu dosyanın işi daha
// mütevazı ve daha acil: noktaların paylaştığı EŞİK SABİTLERİNİN
// birbirinden habersiz kopyalar olmasını bitirmek.
//
// Drift KURGU DEĞİL — bu dosya yazılırken pin testi gerçek bir tanesini
// buldu: FE STEP_RUNGS'ta 14400 (4 sa) vardı, metricStepLadder'da yoktu;
// FE 4 saatlik adım isteyince metrik tarafı 21600'e (6 sa) yuvarlıyor ve
// span ile metrik yüzeyleri FARKLI kafese oturuyordu (v0.9.705'te
// merdivene eklendi).
//
// FE aynası route_pins_test.go ile çivili: chartStep.ts'in STEP_RUNGS'ı
// buradaki merdivenle uyumsuzlaşırsa Go testi kırılır.

// opMVMinStepSec — operation_summary_5m MV'sinin bucket granülü (5 dk).
// Bu adımın ALTINDAKİ istekler o MV'den cevaplanamaz (5 dk'lık bucket'ı
// daha inceye bölemezsin) → ham spans yoluna düşerler. spanmetric.go'da
// üç karar noktası da bu sabiti kullanır; 300 literalinin üç kopyası
// v0.9.705'te buraya indirildi.
//
// narrowTiers'ın 5m kademesiyle (rollupselect.go) AYNI sayı olmak
// zorunda — pin testi bunu da çiviler: MV granülü ile rollup kademesi
// ayrışırsa yönlendirme sessizce tutarsızlaşır.
const opMVMinStepSec = 300
