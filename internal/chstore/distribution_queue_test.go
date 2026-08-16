// v0.9.985 — Distributed spool sağlık sinyalinin regresyon testleri.
//
// ORİJİNAL SEMPTOM (lokal küme, 2026-08-12): `spans_local`'a son yazım
// 08:22/08:49 iken CH saati 12:02 — üç buçuk saat boyunca İKİ shard'a da
// tek satır inmedi. Aynı anda /api/health: clickhouse "ok",
// spans_write_failed 0, spans_queued 0, spans_accepted 109814 ve
// tırmanıyor. Distributed motoru INSERT'i diske spool'layıp hemen OK
// döndüğü için uygulama katmanının HİÇBİR sayacı arızayı göremiyordu.
//
// Buradaki testler üç şeyi çiviliyor:
//  1. Saf verdict'in tam kararı (özellikle "artıyor → degraded" dalı,
//     arızayı yakalayan tek dal).
//  2. Kümülatif error_count'ın tek başına degraded ÜRETMEMESİ — üç gün
//     önceki bir blip sinyali kalıcı olarak yakardı.
//  3. Tek-düğümde HİÇ SORGU ÇALIŞMAMASI (SQL "" döner) — dağıtık olmayan
//     kurulumda davranış bayt-bayt değişmemeli.
package chstore

import (
	"strings"
	"testing"
	"time"
)

// measured — Measured=true bir örnek kurar (probe koştu, sonuç okundu).
func measured(files, errs uint64, tables ...DistributionQueueEntry) *DistributionQueue {
	q := &DistributionQueue{Measured: true, Files: files, ErrorCount: errs, Tables: tables}
	return q
}

func TestDistributionVerdict(t *testing.T) {
	spans := DistributionQueueEntry{Table: "spans", Files: 12702, ErrorCount: 1145,
		LastError: "Code: 241. DB::Exception: Memory limit (total) exceeded"}
	metrics := DistributionQueueEntry{Table: "metric_points", Files: 26132, ErrorCount: 298}

	cases := []struct {
		name string
		// in — ÖNCEKİ karar durumu (histerezis girdisi, v0.9.987).
		// Sıfır değer = "henüz sinyal yok" ile aynı şey.
		in        DistributionState
		cur, prev *DistributionQueue
		// wantDegraded / wantRecovering — dönen durumun tamamı.
		wantDegraded   bool
		wantRecovering int
		// wantDetail: "" = neden metni de BOŞ olmalı (sinyal yok).
		// Aksi hâlde detail'in içermesi gereken ayırt edici parça.
		wantDetail string
	}{
		// ── Sinyal ÜRETİLMEYEN hâller ────────────────────────────────
		{
			// Tek düğüm: CollectDistributionQueue nil döner (sorgu YOK).
			name: "tek düğüm — nil girdi, sinyal yok",
			cur:  nil, prev: nil,
			wantDegraded: false, wantDetail: "",
		},
		{
			// Fail-open dersi (v0.9.984): "ölçemedim" ≠ "temiz". Probe
			// düşmüşken degraded İDDİA ETME ama "ok" diye de sayma —
			// v0.9.987'den beri detail bunu AÇIKÇA söylüyor (eskiden
			// sessiz kalıyordu ve sessizlik "ok" gibi okunuyordu).
			name: "probe düştü — teşhis uydurma, ama sessiz de kalma",
			cur:  &DistributionQueue{Measured: false, ProbeError: "timeout"}, prev: nil,
			wantDegraded: false, wantDetail: "'temiz' anlamına GELMEZ",
		},
		{
			name: "kuyruk boş",
			cur:  measured(0, 0), prev: measured(0, 0),
			wantDegraded: false, wantDetail: "",
		},
		{
			// Sağlıklı kümede anlık derinlik normaldir — canlı ölçümde
			// sağlam tablolar 0-1 dosyadaydı. Eşiğin altı sessiz.
			name: "eşik altı çalkantı — büyüse bile sessiz",
			cur:  measured(40, 0), prev: measured(10, 0),
			wantDegraded: false, wantDetail: "",
		},
		{
			// KRİTİK: error_count sunucu açılışından beri kümülatif ve
			// AZALMAZ. Kuyruk boşken geçmişteki hata alarm üretmemeli.
			name: "boş kuyruk + eski hatalar — kümülatif sayaç alarm ÜRETMEZ",
			cur:  measured(0, 5000), prev: measured(0, 5000),
			wantDegraded: false, wantDetail: "",
		},

		// ── Bilgi ver, ama arıza İDDİA ETME ──────────────────────────
		{
			// TABAN ALTINDA ilk ölçüm: derinlik biliniyor, yön bilinmiyor.
			// (Taban ÜSTÜNDE aynı girdi degraded olur — aşağıdaki pin.)
			name: "ilk ölçüm — derinlik var, yön bilinmiyor",
			cur:  measured(1200, 1145, spans), prev: nil,
			wantDegraded: false, wantDetail: "trend henüz ölçülmedi",
		},
		{
			// Operatör 241'i çözdü, kuyruk MUTLAK TABANIN ALTINDA eriyor:
			// bu İYİ HABER, alarm değil. Sayılar v0.9.987'de küçültüldü —
			// eski hâli (9000 → 12702) artık degraded, çünkü 9 bin dosya
			// yönü ne olursa olsun arızadır.
			name: "drene oluyor — taban altında azalan kuyruk ok",
			cur:  measured(300, 1145, spans), prev: measured(900, 1145),
			wantDegraded: false, wantDetail: "drene oluyor",
		},
		{
			name: "sabit ve hatasız",
			cur:  measured(500, 0), prev: measured(500, 0),
			wantDegraded: false, wantDetail: "sabit, gönderim hatası yok",
		},

		// ── ARIZA: 2026-08-12'nin ta kendisi ─────────────────────────
		{
			name: "BÜYÜYOR — canlı arıza (3s39d ölü ingest, health yeşildi)",
			cur:  measured(38834, 1443, spans, metrics), prev: measured(38713, 1440),
			wantDegraded: true, wantDetail: "BÜYÜYOR",
		},
		{
			name: "sabit + gönderim hatası — gönderici takılı",
			cur:  measured(1200, 1145, spans), prev: measured(1200, 1100),
			wantDegraded: true, wantDetail: "SABİT",
		},

		// ── v0.9.987 MUTLAK TABAN — FLAP PİNLERİ ─────────────────────
		// CANLI FLAP (2026-08-12 16:07): 44.320 dosya "BÜYÜYOR/degraded",
		// 11 saniye sonra 44.318 dosya "drene oluyor/ok". İki dosyalık
		// erime tüm sinyali çevirdi; arıza aralıksız sürüyordu.
		{
			name: "FLAP PİNİ — 44 binde 2 dosya erimesi ok DEMEZ",
			cur:  measured(44318, 1505, spans, metrics), prev: measured(44320, 1505),
			wantDegraded: true, wantDetail: "hâlâ mutlak tabanın",
		},
		{
			name: "FLAP PİNİ — 44 bin SABİT ok DEMEZ",
			cur:  measured(44320, 1505, spans, metrics), prev: measured(44320, 1505),
			wantDegraded: true, wantDetail: "SABİT",
		},
		{
			// Kümülatif error_count SIFIR olsa bile: derinliğin kendisi
			// arızanın kanıtı. Eski kod bu girdide "sabit, gönderim hatası
			// yok" deyip OK dönerdi.
			name: "FLAP PİNİ — 44 bin sabit + hata sayacı 0 yine degraded",
			cur:  measured(44320, 0, metrics), prev: measured(44320, 0),
			wantDegraded: true, wantDetail: "SABİT",
		},
		{
			// Mutlak taban trend GEREKTİRMEZ: ilk ölçümde bile hüküm kesin.
			// Eski kod "trend henüz ölçülmedi" deyip OK dönerdi — pod
			// yeniden başladığında arıza 30 sn boyunca görünmezdi.
			name: "FLAP PİNİ — taban üstü ilk ölçüm trend BEKLEMEZ",
			cur:  measured(12702, 1145, spans), prev: nil,
			wantDegraded: true, wantDetail: "yön henüz ölçülmedi",
		},
		{
			// Sınır: tam eşikte degraded, bir altında giriş kapısına düşer.
			name: "taban SINIRI — tam 2000 degraded",
			cur:  measured(2000, 0), prev: measured(2000, 0),
			wantDegraded: true, wantDetail: "SABİT",
		},
		{
			name: "taban SINIRI — 1999 sabit + hatasız sessiz",
			cur:  measured(1999, 0), prev: measured(1999, 0),
			wantDegraded: false, wantDetail: "sabit, gönderim hatası yok",
		},

		// ── v0.9.987 HİSTEREZİS — degraded'dan çıkış ─────────────────
		{
			// Taban altına indi ama TEK ölçümle geri dönüş YOK.
			name: "histerezis — ilk iyi ölçüm degraded'ı düşürmez",
			in:   DistributionState{Degraded: true},
			cur:  measured(50, 1505), prev: measured(2400, 1505),
			wantDegraded: true, wantRecovering: 1, wantDetail: "HENÜZ DOĞRULANMADI",
		},
		{
			name: "histerezis — ikinci iyi ölçüm de yetmez",
			in:   DistributionState{Degraded: true, Recovering: 1},
			cur:  measured(50, 1505), prev: measured(120, 1505),
			wantDegraded: true, wantRecovering: 2, wantDetail: "HENÜZ DOĞRULANMADI",
		},
		{
			// ÜÇÜNCÜ ardışık iyileşme: sinyal temizlenir ve karar normal
			// giriş kapısına düşer (burada eşik altı → tam sessizlik).
			name: "histerezis — üçüncü iyi ölçümde temizlenir",
			in:   DistributionState{Degraded: true, Recovering: 2},
			cur:  measured(50, 1505), prev: measured(80, 1505),
			wantDegraded: false, wantRecovering: 0, wantDetail: "",
		},
		{
			// Arada TEK kötü örnek sayacı sıfırlar — 90 sn KESİNTİSİZ
			// iyileşme isteniyor, "üç kere iyi gördüm" değil.
			name: "histerezis — arada kötü örnek sayacı sıfırlar",
			in:   DistributionState{Degraded: true, Recovering: 2},
			cur:  measured(900, 1505), prev: measured(700, 1505),
			wantDegraded: true, wantRecovering: 0, wantDetail: "HENÜZ DOĞRULANMADI",
		},
		{
			// KURAL 3: "ölçemedim" iyileşme SAYILMAZ ve yerleşik arızayı
			// TEMİZLEMEZ. Bu olmadan, probe'un düştüğü her tur sayacı
			// ilerletir ve arıza 90 sn'de kendini "iyileşti" ilan ederdi.
			name: "histerezis — ölçülemeyen örnek arızayı temizlemez",
			in:   DistributionState{Degraded: true, Recovering: 2},
			cur:  &DistributionQueue{Measured: false, ProbeError: "context deadline exceeded"},
			prev: measured(44320, 1505),
			wantDegraded: true, wantRecovering: 0, wantDetail: "son bilinen arıza hâli KORUNUYOR",
		},
		{
			// Kapsam kilidinin histerezisteki karşılığı: küme→yerel
			// daralması iyileşme sayılmaz (v0.9.986 dersi, [[fallback-must-carry-scope]]).
			name: "histerezis — kapsam değişimi iyileşme sayılmaz",
			in:   DistributionState{Degraded: true, Recovering: 2},
			cur: func() *DistributionQueue {
				q := measured(800, 700, spans)
				q.Partial = true
				return q
			}(),
			prev:         measured(1900, 1443),
			wantDegraded: true, wantRecovering: 0, wantDetail: "HENÜZ DOĞRULANMADI",
		},
		{
			// Tek düğüme dönüş (cur nil): spool kavramı yok, durum sıfırlanır.
			name: "histerezis — tek düğümde durum sıfırlanır",
			in:   DistributionState{Degraded: true, Recovering: 2},
			cur:  nil, prev: nil,
			wantDegraded: false, wantRecovering: 0, wantDetail: "",
		},

		// ── v0.9.986 KAPSAM KİLİDİ ───────────────────────────────────
		// Fan-out düşünce ölçüm yalnız BU DÜĞÜMÜ kapsar. Küme geneli bir
		// önceki örnekle kıyaslamak sahte rahatlama üretirdi.
		{
			// CANLI SAYILAR: küme geneli 41.274 → yerel 19.020. Kilit
			// olmasa "drene oluyor" derdi; oysa arıza aynen sürüyordu.
			// v0.9.987: artık degraded — 19 bin dosya MUTLAK olarak arıza,
			// kilit yalnız YÖN İDDİASINI susturur (kararı değil).
			name: "küme → yerel daralması 'drene oluyor' SAYILMAZ",
			cur: func() *DistributionQueue {
				q := measured(19020, 700, spans)
				q.Partial = true
				return q
			}(),
			prev:         measured(41274, 1443),
			wantDegraded: true, wantDetail: "yön henüz ölçülmedi",
		},
		{
			// Ters yön de aynı: yerelden kümeye geçiş sahte BÜYÜME üretir.
			// Degraded doğru (41 bin dosya), ama GEREKÇE "BÜYÜYOR" OLAMAZ.
			name: "yerel → küme genişlemesi sahte BÜYÜME üretmez",
			cur:  measured(41274, 1443, spans),
			prev: func() *DistributionQueue {
				q := measured(19020, 700)
				q.Partial = true
				return q
			}(),
			wantDegraded: true, wantDetail: "yön henüz ölçülmedi",
		},
		{
			// Taban ALTINDA kapsam kilidi hâlâ tek karar ölçüsü: kıyas
			// yapılamıyorsa arıza İDDİA EDİLMEZ.
			name: "taban altında kapsam değişimi — arıza iddia edilmez",
			cur: func() *DistributionQueue {
				q := measured(300, 700, spans)
				q.Partial = true
				return q
			}(),
			prev:         measured(1500, 1443),
			wantDegraded: false, wantDetail: "trend henüz ölçülmedi",
		},
		{
			// İki örnek de yerel ise trend GEÇERLİ — kısmi ama tutarlı.
			name: "iki yerel örnek — trend geçerli, arıza görülür",
			cur: func() *DistributionQueue {
				q := measured(19020, 700, spans)
				q.Partial = true
				return q
			}(),
			prev: func() *DistributionQueue {
				q := measured(18997, 690)
				q.Partial = true
				return q
			}(),
			wantDegraded: true, wantDetail: "BÜYÜYOR",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DistributionVerdict(c.in, c.cur, c.prev)
			if got.Degraded != c.wantDegraded {
				t.Fatalf("degraded = %v, want %v (detail=%q)",
					got.Degraded, c.wantDegraded, got.Detail)
			}
			if got.Recovering != c.wantRecovering {
				t.Fatalf("recovering = %d, want %d", got.Recovering, c.wantRecovering)
			}
			if c.wantDetail == "" {
				if got.Detail != "" {
					t.Fatalf("sinyal üretilmemeliydi, detail = %q", got.Detail)
				}
				return
			}
			if !strings.Contains(got.Detail, c.wantDetail) {
				t.Fatalf("detail = %q, %q içermeliydi", got.Detail, c.wantDetail)
			}
		})
	}
}

// FLAP SENARYOSU — canlı diziyi olduğu gibi koştur (v0.9.987).
//
// 2026-08-12 16:07'de /api/health ardışık örneklerde şunu dedi:
//
//	16:07:40  degraded  44.320  "BÜYÜYOR"
//	16:07:51  ok        44.318  "drene oluyor"   ← YALAN
//
// Bu test durumu tur tur taşıyarak aynı diziyi tekrar oynatır: arıza
// süresince TEK BİR ok örneği bile çıkmamalı. Tablo testi tek turu
// çiviliyor, bu test ZİNCİRİ çiviliyor — flap zaten zincirde doğuyordu.
func TestDistributionVerdictNoFlapDuringOutage(t *testing.T) {
	metrics := DistributionQueueEntry{Table: "metric_points", Files: 29837,
		LastError: "Code: 241. DB::Exception: Memory limit (total) exceeded"}
	// Canlı gözlenen dizi: dalgalanıyor ama hep 44 bin civarında.
	series := []uint64{44299, 44311, 44320, 44320, 44320, 44318, 44318, 44305, 44340}

	var st DistributionState
	var prev *DistributionQueue
	for i, files := range series {
		cur := measured(files, 1505, metrics)
		st = DistributionVerdict(st, cur, prev)
		if !st.Degraded {
			t.Fatalf("örnek %d (files=%d): arıza sürerken ok DENDİ — flap. detail=%q",
				i, files, st.Detail)
		}
		prev = cur
	}
	// Arıza çözülüyor: taban altına iniş TEK örnekle ok yapmamalı,
	// üçüncü ardışık iyileşmede sinyal susmalı.
	for i, files := range []uint64{1500, 400, 60} {
		cur := measured(files, 1505)
		st = DistributionVerdict(st, cur, prev)
		prev = cur
		if i < distributedRecoverSamples-1 && !st.Degraded {
			t.Fatalf("iyileşme %d (files=%d): histerezis dolmadan ok DENDİ (detail=%q)",
				i, files, st.Detail)
		}
	}
	if st.Degraded {
		t.Fatalf("%d ardışık iyileşmeden sonra sinyal susmalıydı: %q",
			distributedRecoverSamples, st.Detail)
	}
}

// Ölçüm KESİNTİSİ arızayı temizleyemez (v0.9.987, kural 3).
//
// Probe'un düştüğü turlar "iyileşme" sayılsaydı, tam da ölçemediğimiz
// için arıza 90 saniyede kendini "çözüldü" ilan ederdi — fail-open'ın
// bir düzeltmeyi sessizce geri alması ([[fail-open-silently-unapplies]]).
func TestDistributionVerdictUnmeasuredNeverRecovers(t *testing.T) {
	st := DistributionState{Degraded: true, Recovering: 2}
	down := &DistributionQueue{Measured: false, ProbeError: "timeout"}
	for i := 0; i < 10; i++ {
		st = DistributionVerdict(st, down, nil)
		if !st.Degraded {
			t.Fatalf("tur %d: ölçülemeyen örnekler arızayı temizledi", i)
		}
		if st.Recovering != 0 {
			t.Fatalf("tur %d: ölçülemeyen örnek iyileşme saydı (%d)", i, st.Recovering)
		}
	}
	if !strings.Contains(st.Detail, "ÖLÇÜLEMEDİ") {
		t.Fatalf("detail ölçüm kesintisini söylemeli: %q", st.Detail)
	}
}

// Arızalı dalların neden metni operatörü DOĞRU KATMANA göndermeli:
// "spans_write_failed 0 ama veri yok" sorusunun cevabı spool
// sözleşmesidir ve CH'nin kendi istisnası zaten elimizdedir.
func TestDistributionVerdictDetailCarriesDiagnosis(t *testing.T) {
	spans := DistributionQueueEntry{Table: "spans", Files: 12702, ErrorCount: 1145,
		LastError: "Code: 241. DB::Exception: Received from chc-0.chc-headless:9000.\n" +
			"   Memory limit (total) exceeded: would use 2.82 GiB, maximum: 2.80 GiB."}
	cur := measured(12702, 1145, spans)
	prev := measured(12000, 1100)

	st := DistributionVerdict(DistributionState{}, cur, prev)
	if !st.Degraded {
		t.Fatalf("büyüyen kuyruk degraded olmalıydı")
	}
	for _, want := range []string{"spans", "Son hata", "Code: 241"} {
		if !strings.Contains(st.Detail, want) {
			t.Fatalf("detail %q içermeliydi: %q", want, st.Detail)
		}
	}
	// /api/health JSON'una giriyor — tek satır olmalı.
	if strings.ContainsAny(st.Detail, "\n\r") {
		t.Fatalf("detail satır sonu taşıyor: %q", st.Detail)
	}
	// Probe hatası da aynı gövdeye giriyor: çok satırlı bir CH istisnası
	// oradan da tek satır çıkmalı (v0.9.987 oneLine ortaklaşması).
	down := DistributionVerdict(DistributionState{}, &DistributionQueue{
		ProbeError: "Code: 159. DB::Exception: Timeout exceeded:\n  elapsed 64.0 s."}, nil)
	if strings.ContainsAny(down.Detail, "\n\r") {
		t.Fatalf("probe hatası satır sonu taşıyor: %q", down.Detail)
	}
}

// worstDistributionTable sıraya GÜVENMEMELİ: sorgu files DESC döndürüyor
// ama saf fonksiyon elle kurulan girdide de en derini bulmalı.
func TestWorstDistributionTableIgnoresInputOrder(t *testing.T) {
	got := worstDistributionTable([]DistributionQueueEntry{
		{Table: "logs", Files: 3},
		{Table: "metric_points", Files: 26132},
		{Table: "spans", Files: 12702},
	})
	if !strings.HasPrefix(got, "metric_points") {
		t.Fatalf("en derin tablo metric_points olmalıydı, got %q", got)
	}
	if worstDistributionTable(nil) != "" {
		t.Fatalf("boş girdi boş dönmeli")
	}
}

// SQL ŞEKLİ PİNİ — iki dal.
//
// Tek-düğüm dalı bu sürümün en önemli sözleşmesi: cluster_name boşken
// metin BOŞ döner, yani çağıran hiçbir sorgu çalıştırmaz. Dağıtık
// olmayan kurulumlar için ek maliyet SIFIR ve davranış değişmemiş olur.
func TestDistributionQueueSQLShape(t *testing.T) {
	if got := distributionQueueSQL("", true); got != "" {
		t.Fatalf("tek düğümde sorgu ÜRETİLMEMELİ, got %q", got)
	}
	if got := distributionQueueSQL("   ", true); got != "" {
		t.Fatalf("boşluktan ibaret küme adı da tek-düğüm sayılmalı, got %q", got)
	}

	q := distributionQueueSQL("coremetry", true)
	// clusterAllReplicas ŞART: spool her düğümün KENDİ diskinde durur;
	// cluster() shard başına tek replika okur ve 2×2'lik bir kümede
	// spool'un yarısını görünmez yapardı (v0.9.454 ile aynı bulgu).
	if !strings.Contains(q, "clusterAllReplicas('coremetry', system.distribution_queue)") {
		t.Fatalf("clusterAllReplicas dalı eksik:\n%s", q)
	}
	if strings.Contains(q, "cluster('") {
		t.Fatalf("cluster() kullanılmamalı (shard başına tek replika):\n%s", q)
	}
	// Sistem tablosu küçük ama sınırsız bırakılmaz: tıkanmış bir kümede
	// bu okuma sağlık yolunun önünde durur.
	if !strings.Contains(q, "max_execution_time") {
		t.Fatalf("max_execution_time eksik:\n%s", q)
	}
	if !strings.Contains(q, "skip_unavailable_shards = 1") {
		t.Fatalf("düşmüş bir düğüm tüm ölçümü düşürmemeli:\n%s", q)
	}
	// Tek round-trip: teşhis alanlarının hepsi aynı sorguda.
	for _, col := range []string{"data_files", "data_compressed_bytes",
		"broken_data_files", "error_count", "last_exception"} {
		if !strings.Contains(q, col) {
			t.Fatalf("%s kolonu eksik:\n%s", col, q)
		}
	}
	// last_exception kırpılmalı — CH istisnaları kilobaytlarca olabilir
	// ve bu metin /api/health gövdesine giriyor.
	if !strings.Contains(q, "substring(argMax(last_exception") {
		t.Fatalf("last_exception kırpılmamış:\n%s", q)
	}
}

// v0.9.986 — fan-out bütçesi ÖLÇÜLEREK 3→10 sn yükseltildi; yerel
// fallback dalı da aynı kolonları döndürmeli.
//
// v0.9.985'in 3 sn'si canlı arızada yetmedi: fan-out sağlıklıya yakınken
// 646 ms, küme baskı altındayken 2.6 sn, ağır çekişmede 14.3 sn sürüyor
// ve probe tam da ölçmek istediği anda körleşiyordu.
func TestDistributionQueueBudgets(t *testing.T) {
	fanout := distributionQueueSQL("coremetry", true)
	if !strings.Contains(fanout, "max_execution_time = 10") {
		t.Fatalf("fan-out bütçesi 10 sn olmalı (ölçüldü: tepe 2.6 sn):\n%s", fanout)
	}

	local := distributionQueueLocalSQL(true)
	if strings.Contains(local, "clusterAllReplicas") {
		t.Fatalf("yerel dal fan-out yapmamalı:\n%s", local)
	}
	if !strings.Contains(local, "FROM system.distribution_queue") {
		t.Fatalf("yerel dal doğrudan sistem tablosunu okumalı:\n%s", local)
	}
	if !strings.Contains(local, "max_execution_time = 3") {
		t.Fatalf("yerel bütçe 3 sn olmalı (ölçüldü: 0.2-1.1 sn):\n%s", local)
	}
	// İki dal aynı kolonları AYNI SIRADA döndürmeli — tek scan döngüsü
	// ikisini de okuyor, sıra kayması sessiz veri karışması olurdu.
	for _, col := range []string{"data_files", "data_compressed_bytes",
		"broken_data_files", "error_count", "last_exception"} {
		if !strings.Contains(local, col) {
			t.Fatalf("yerel dalda %s eksik:\n%s", col, local)
		}
	}
	cut := func(s string) string {
		i := strings.Index(s, "FROM ")
		return s[:i]
	}
	if cut(fanout) != cut(local) {
		t.Fatalf("iki dalın SELECT listesi ayrıştı:\n--- fan-out:\n%s\n--- yerel:\n%s",
			cut(fanout), cut(local))
	}
}

// Küme adındaki tek tırnak SQL'i kıramamalı (metin enterpolasyonu).
func TestDistributionQueueSQLStripsQuote(t *testing.T) {
	q := distributionQueueSQL("a'b", true)
	if strings.Contains(q, "'a'b'") {
		t.Fatalf("tek tırnak süzülmedi:\n%s", q)
	}
	if !strings.Contains(q, "clusterAllReplicas('ab', system.distribution_queue)") {
		t.Fatalf("beklenen temizlenmiş ad yok:\n%s", q)
	}
}

func TestFmtCount(t *testing.T) {
	for _, c := range []struct {
		in   uint64
		want string
	}{
		{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1.000"},
		{12702, "12.702"}, {26132, "26.132"}, {1234567, "1.234.567"},
	} {
		if got := fmtCount(c.in); got != c.want {
			t.Fatalf("fmtCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── v0.9.1077 — spool teşhisinde yaş etiketi ─────────────────────────
//
// 2026-08-16 prod olayı: 16 gün önceki bir 241 istisnası tarihsiz
// basılınca CANLI bir bellek krizi sanıldı; operatör yaşamayan arızayı
// kovaladı. Etiket artık istisnanın yaşını söyler.

// Unit-mixing dersi ([[feedback-unit-mixing-needs-both-branches]]):
// HER birim dalı ayrı vaka.
func TestFmtAgeShort(t *testing.T) {
	cases := []struct {
		name  string
		delta time.Duration
		want  string
	}{
		{"taze", 30 * time.Second, "az önce"},
		{"dakika dalı", 12 * time.Minute, "12 dk"},
		{"dakika üst sınırı sa'ya devretmez", 89 * time.Minute, "89 dk"},
		{"saat dalı", 3 * time.Hour, "3 sa"},
		{"gün dalı", 16*24*time.Hour + 5*time.Hour, "16 gün"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtAgeShort(int64(c.delta)); got != c.want {
				t.Errorf("fmtAgeShort(%v) = %q, beklenen %q", c.delta, got, c.want)
			}
		})
	}
}

func TestLastErrorHintAge(t *testing.T) {
	now := int64(1_000_000_000_000_000_000)
	sixteenDays := int64(16 * 24 * time.Hour)
	t.Run("zamanlı istisna yaşıyla basılır", func(t *testing.T) {
		got := lastErrorHint([]DistributionQueueEntry{{
			Table: "metric_points", Files: 100,
			LastError: "Code: 241. DB::Exception: memory limit exceeded",
			LastErrorAtNs: now - sixteenDays,
		}}, now)
		if !strings.Contains(got, "Son hata (16 gün önce):") {
			t.Errorf("yaş etiketi bekleniyordu: %q", got)
		}
	})
	t.Run("zaman yoksa eski biçim (yaş İDDİA EDİLMEZ)", func(t *testing.T) {
		got := lastErrorHint([]DistributionQueueEntry{{
			Table: "metric_points", Files: 100,
			LastError: "Code: 241. DB::Exception: memory limit exceeded",
		}}, now)
		if !strings.Contains(got, " Son hata: ") || strings.Contains(got, "önce)") {
			t.Errorf("tarihsiz biçim bekleniyordu: %q", got)
		}
	})
	t.Run("nowNs 0 (bilinmiyor) yaş üretmez", func(t *testing.T) {
		got := lastErrorHint([]DistributionQueueEntry{{
			Table: "metric_points", Files: 100,
			LastError: "x", LastErrorAtNs: 5,
		}}, 0)
		if strings.Contains(got, "önce)") {
			t.Errorf("nowNs=0 iken yaş basılmamalı: %q", got)
		}
	})
}

// Eski CH dalı: kolon yokken sorgu sabit toDateTime(0) seçmeli — kolon
// SAYISI değişmez, scan döngüsü tek kalır, yaş sessizce kapanır.
func TestDistributionQueueSelectOldCH(t *testing.T) {
	old := distributionQueueSelect(false)
	if strings.Contains(old, "last_exception_time") {
		t.Errorf("hasLastAt=false dalı last_exception_time seçmemeli:\n%s", old)
	}
	if !strings.Contains(old, "toDateTime(0)") {
		t.Errorf("hasLastAt=false dalı sabit toDateTime(0) seçmeli:\n%s", old)
	}
	if !strings.Contains(distributionQueueSelect(true), "argMax(last_exception_time,") {
		t.Errorf("hasLastAt=true dalı zamanı last_err ile AYNI argMax anahtarından seçmeli")
	}
}
